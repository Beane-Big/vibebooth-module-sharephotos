package main

import (
	"archive/zip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// 门户里看相册和分享页看相册用同一套取图、打包下载的处理，只是找相册和判断能不能看的方式不同。

// servePhoto 返回一张照片的缩略图、预览图或原图。原图按总带宽上限限速，支持断点续传；
// 默认作为附件下载，带 inline=1 时直接显示（微信里长按保存要用这种）。
func (app *App) servePhoto(w http.ResponseWriter, r *http.Request, a *Album) {
	id, err := strconv.ParseInt(r.PathValue("photo"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "没有这张照片")
		return
	}
	kind := r.PathValue("kind")
	if kind != kindOriginal && kind != kindPreview && kind != kindThumb {
		writeError(w, http.StatusNotFound, "没有这种图")
		return
	}
	p, err := app.store.Photo(r.Context(), a.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "没有这张照片")
		return
	}
	if kind != kindOriginal && p.Status != statusReady {
		writeError(w, http.StatusNotFound, "预览图还在生成")
		return
	}
	f, err := os.Open(app.files.path(a.ID, p.ID, kind))
	if err != nil {
		writeError(w, http.StatusNotFound, "文件不见了")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取失败")
		return
	}
	// 照片的 id 不会复用、内容不会变，浏览器可以一直缓存；private 表示只缓存在本人的浏览器里。
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	if kind != kindOriginal {
		http.ServeContent(w, r, "", info.ModTime(), f)
		return
	}
	disposition := "attachment"
	inline := r.URL.Query().Get("inline") == "1"
	if inline {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename*=UTF-8''"+url.PathEscape(p.Name))
	// 只在从头开始的下载时计数，断点续传和直接显示不算。
	if !inline && (r.Header.Get("Range") == "" || strings.HasPrefix(r.Header.Get("Range"), "bytes=0-")) && r.Method == http.MethodGet {
		if err := app.store.AddDownloads(r.Context(), []int64{p.ID}); err != nil {
			slog.Error("记录下载次数失败", "err", err)
		}
	}
	http.ServeContent(app.files.Throttle(w, r), r, "", info.ModTime(), f)
}

// serveZip 把选中的照片（ids=1,2,3，不带时是全部）打成 zip 下载。JPEG 已经压缩过，zip 里不再压缩，打包几乎不占 CPU；
// 边打包边发送，不在服务器上留临时文件。
func (app *App) serveZip(w http.ResponseWriter, r *http.Request, a *Album) {
	photos, err := app.store.Photos(r.Context(), a)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取照片失败")
		return
	}
	want := map[int64]bool{}
	if ids := r.URL.Query().Get("ids"); ids != "" {
		for _, s := range strings.Split(ids, ",") {
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				want[id] = true
			}
		}
	}
	var list []*Photo
	for _, p := range photos {
		if p.Status == statusReady && (len(want) == 0 || want[p.ID]) {
			list = append(list, p)
		}
	}
	if len(list) == 0 {
		writeError(w, http.StatusNotFound, "没有可以下载的照片")
		return
	}
	var ids []int64
	var total int64
	for _, p := range list {
		ids = append(ids, p.ID)
		total += p.Size
	}
	if err := app.store.AddDownloads(r.Context(), ids); err != nil {
		slog.Error("记录下载次数失败", "err", err)
	}

	name := strings.TrimSpace(a.Title)
	if name == "" {
		name = "照片"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name+".zip"))
	// 告诉前端原图一共多大，用来估计下载时间；zip 本身比它多一点目录信息。
	w.Header().Set("X-Photos-Bytes", strconv.FormatInt(total, 10))
	zw := zip.NewWriter(app.files.Throttle(w, r))
	used := map[string]int{}
	for _, p := range list {
		modified := p.CreatedAt
		if p.TakenAt != nil {
			modified = *p.TakenAt
		}
		hdr := &zip.FileHeader{Name: uniqueName(used, p.Name), Method: zip.Store, Modified: modified.In(time.Local)}
		dst, err := zw.CreateHeader(hdr)
		if err != nil {
			return
		}
		f, err := os.Open(app.files.path(a.ID, p.ID, kindOriginal))
		if err != nil {
			slog.Error("打包时读不到原图", "photo", p.ID, "err", err)
			continue
		}
		_, err = io.Copy(dst, f)
		f.Close()
		if err != nil {
			// 多半是下载的人取消了，不用记错误。
			return
		}
	}
	zw.Close()
}

// uniqueName 让 zip 里的文件名不重复：重名的在扩展名前面加 (2)、(3)。
func uniqueName(used map[string]int, name string) string {
	key := strings.ToLower(name)
	used[key]++
	if used[key] == 1 {
		return name
	}
	ext := path.Ext(name)
	return uniqueName(used, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), used[key], ext))
}
