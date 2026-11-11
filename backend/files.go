package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Files 管理照片文件：每个相册一个目录，原图、预览图、缩略图各一份，文件名用照片 id，不用上传时的文件名。
type Files struct {
	dir   string
	store *Store
	queue chan [2]int64 // 待生成预览图的照片：相册 id、照片 id

	mu      sync.Mutex
	limiter *rate.Limiter // 所有原图下载共用的带宽上限
	mbps    int
}

const (
	kindOriginal = "original"
	kindPreview  = "preview"
	kindThumb    = "thumb"
)

func NewFiles(dir string, store *Store) *Files {
	return &Files{dir: dir, store: store, queue: make(chan [2]int64, 10000), limiter: rate.NewLimiter(rate.Inf, 0)}
}

func (f *Files) albumDir(albumID int64) string {
	return filepath.Join(f.dir, "albums", fmt.Sprint(albumID))
}

// path 是照片文件的位置。原图不管什么格式都存成 <id>.jpg（老数据就是这样），格式按文件内容判断；预览图和缩略图都是 JPEG。
func (f *Files) path(albumID, photoID int64, kind string) string {
	name := fmt.Sprintf("%d.jpg", photoID)
	if kind != kindOriginal {
		name = fmt.Sprintf("%d.%s.jpg", photoID, kind)
	}
	return filepath.Join(f.albumDir(albumID), name)
}

func (f *Files) RemovePhoto(albumID, photoID int64) {
	for _, kind := range []string{kindOriginal, kindPreview, kindThumb} {
		os.Remove(f.path(albumID, photoID, kind))
	}
}

func (f *Files) RemoveAlbum(albumID int64) error {
	return os.RemoveAll(f.albumDir(albumID))
}

// Enqueue 把刚上传的照片排进后台队列，生成预览图和缩略图。
func (f *Files) Enqueue(albumID, photoID int64) {
	f.queue <- [2]int64{albumID, photoID}
}

// Work 在后台一张一张地生成预览图。一次只处理一张：解码一张 3000 万像素的照片要占一两百 MB 内存，服务器只有 4 GB。
// 启动时先把上次没处理完的照片重新排队。
func (f *Files) Work(ctx context.Context) {
	pending, err := f.store.PhotosByStatus(ctx, statusProcessing)
	if err != nil {
		slog.Error("读取待处理的照片失败", "err", err)
	}
	go func() {
		for _, p := range pending {
			f.Enqueue(p.AlbumID, p.ID)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-f.queue:
			albumID, photoID := job[0], job[1]
			start := time.Now()
			w, h, err := makeDerivatives(f.path(albumID, photoID, kindOriginal), f.path(albumID, photoID, kindPreview), f.path(albumID, photoID, kindThumb))
			status := statusReady
			if err != nil {
				// 照片可能在排队时已经被删了，这时什么都不用做。
				if _, statErr := os.Stat(f.path(albumID, photoID, kindOriginal)); os.IsNotExist(statErr) {
					continue
				}
				slog.Error("生成预览图失败", "photo", photoID, "err", err)
				status = statusFailed
			} else {
				slog.Info("生成预览图", "photo", photoID, "ms", time.Since(start).Milliseconds())
			}
			if err := f.store.SetPhotoStatus(ctx, photoID, status, w, h); err != nil {
				slog.Error("保存照片状态失败", "photo", photoID, "err", err)
			}
		}
	}
}

// SetRate 设置原图下载的总带宽上限（Mbps），0 表示不限。几个人同时下载时平分这个带宽，给门户和其他应用留出余量。
func (f *Files) SetRate(mbps int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mbps = max(mbps, 0)
	if mbps <= 0 {
		f.limiter.SetLimit(rate.Inf)
		return
	}
	bytesPerSec := mbps * 125_000
	f.limiter.SetLimit(rate.Limit(bytesPerSec))
	f.limiter.SetBurst(throttleChunk)
}

// RateMbps 返回当前的下载限速，前端用它估计下载要多久。
func (f *Files) RateMbps() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mbps
}

// throttleChunk 是限速时每次写出的字节数，也是令牌桶的容量。
const throttleChunk = 32 << 10

// throttled 包一层 ResponseWriter，按总带宽上限慢慢写。下载的人断开时 ctx 结束，不再等。
type throttled struct {
	http.ResponseWriter
	ctx     context.Context
	limiter *rate.Limiter
}

func (f *Files) Throttle(w http.ResponseWriter, r *http.Request) http.ResponseWriter {
	return &throttled{ResponseWriter: w, ctx: r.Context(), limiter: f.limiter}
}

func (t *throttled) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), throttleChunk)
		if t.limiter.Limit() != rate.Inf {
			if err := t.limiter.WaitN(t.ctx, n); err != nil {
				return written, err
			}
		}
		m, err := t.ResponseWriter.Write(p[:n])
		written += m
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}
