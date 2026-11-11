package main

import (
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// 门户里看相册和分享页看相册用同一套取图的处理，只是找相册和判断能不能看的方式不同。

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
	if kind != kindOriginal {
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeContent(w, r, "", info.ModTime(), f)
		return
	}
	// 原图保持上传时的格式，上传时已让文件名的扩展名和格式一致。
	w.Header().Set("Content-Type", mimeOf(p.Name))
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
