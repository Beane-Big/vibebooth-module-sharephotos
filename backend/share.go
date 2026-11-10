package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// 分享页的接口：/api/sharephotos/s/<令牌>/...，不需要登录。
// 能不能看由三样东西决定：链接里 128 位的随机令牌（猜不到）、有效期（到期就打不开）、可选的 4 位提取码。
// 提取码输对以后发一个只对这个链接有效的 Cookie，图片请求带着它就能取图；换提取码或换链接后旧 Cookie 自动失效。

const shareCookiePath = "/api/sharephotos/s/"

func (app *App) shareRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sharephotos/s/{token}", app.withShare(false, app.shareInfo))
	mux.HandleFunc("POST /api/sharephotos/s/{token}/unlock", app.withShare(false, app.unlock))
	mux.HandleFunc("GET /api/sharephotos/s/{token}/photos/{photo}/{kind}", app.withShare(true, app.servePhoto))
}

// withShare 按令牌找到相册，到期的当作不存在；unlocked 为 true 时还要已经输对提取码。
func (app *App) withShare(unlocked bool, h func(http.ResponseWriter, *http.Request, *Album)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 分享页不让搜索引擎收录，也不把网址通过 Referer 带给别的网站。
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("Referrer-Policy", "no-referrer")
		a, err := app.store.AlbumByToken(r.Context(), r.PathValue("token"))
		if errors.Is(err, errNotFound) || (err == nil && a.Expired(time.Now())) {
			writeError(w, http.StatusNotFound, "链接已经失效，请联系分享的人重新发一个")
			return
		}
		if err != nil {
			slog.Error("读取相册失败", "err", err)
			writeError(w, http.StatusInternalServerError, "读取失败")
			return
		}
		if unlocked && !app.isUnlocked(r, a) {
			writeError(w, http.StatusUnauthorized, "请先输入提取码")
			return
		}
		h(w, r, a)
	}
}

// shareInfo 返回分享页要显示的内容。还没输提取码时只有名称和说明，没有照片列表。
func (app *App) shareInfo(w http.ResponseWriter, r *http.Request, a *Album) {
	out := map[string]any{
		"title": a.Title, "shotDate": a.ShotDate, "note": a.Note, "ownerName": a.OwnerName,
		"expiresAt": a.ExpiresAt, "count": a.Ready, "totalBytes": a.TotalBytes, "needCode": a.Code != "",
		"cover": a.Cover, "rateMbps": app.files.RateMbps(),
	}
	if app.isUnlocked(r, a) {
		photos, err := app.store.Photos(r.Context(), a)
		if err != nil {
			slog.Error("读取照片失败", "err", err)
			writeError(w, http.StatusInternalServerError, "读取失败")
			return
		}
		ready := []*Photo{}
		for _, p := range photos {
			if p.Status == statusReady {
				ready = append(ready, p)
			}
		}
		out["photos"] = ready
		if err := app.store.AddView(r.Context(), a.ID); err != nil {
			slog.Error("记录打开次数失败", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (app *App) unlock(w http.ResponseWriter, r *http.Request, a *Album) {
	if a.Code == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if wait := app.locks.blocked(a.ID); wait > 0 {
		writeError(w, http.StatusTooManyRequests, "输错次数太多，请 "+strconv.Itoa(int(wait.Minutes())+1)+" 分钟后再试")
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in)
	if subtle.ConstantTimeCompare([]byte(in.Code), []byte(a.Code)) != 1 {
		app.locks.fail(a.ID)
		writeError(w, http.StatusForbidden, "提取码不对")
		return
	}
	app.locks.reset(a.ID)
	http.SetCookie(w, &http.Cookie{
		Name:     app.cookieName(a),
		Value:    app.cookieValue(a),
		Path:     shareCookiePath,
		Expires:  a.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (app *App) isUnlocked(r *http.Request, a *Album) bool {
	if a.Code == "" {
		return true
	}
	c, err := r.Cookie(app.cookieName(a))
	return err == nil && hmac.Equal([]byte(c.Value), []byte(app.cookieValue(a)))
}

// 每个相册一个 Cookie，名字里带相册 id，同时打开几个分享链接也不会互相覆盖。
func (app *App) cookieName(a *Album) string { return "sp" + strconv.FormatInt(a.ID, 10) }

// cookieValue 用密钥签上令牌和提取码：两者有一个变了，旧 Cookie 就对不上。
func (app *App) cookieValue(a *Album) string {
	m := hmac.New(sha256.New, app.secret)
	m.Write([]byte(a.Token + "|" + a.Code))
	return hex.EncodeToString(m.Sum(nil))
}

// codeLocks 限制提取码的试错：一个相册 10 分钟内输错 10 次就锁 10 分钟。
// 按相册计数而不是按 IP：平台在反向代理后面时模块拿不到可靠的访客 IP。真正挡住外人的是猜不到的链接令牌，提取码是第二道门。
type codeLocks struct {
	mu   sync.Mutex
	list map[int64]*codeLock
}

type codeLock struct {
	since    time.Time // 这一轮计数的开始时间
	failures int
	until    time.Time // 锁到什么时候
}

const (
	codeMaxFailures = 10
	codeWindow      = 10 * time.Minute
)

func newCodeLocks() *codeLocks { return &codeLocks{list: map[int64]*codeLock{}} }

func (c *codeLocks) blocked(id int64) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l := c.list[id]; l != nil {
		return time.Until(l.until)
	}
	return 0
}

func (c *codeLocks) fail(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	l := c.list[id]
	if l == nil || now.Sub(l.since) > codeWindow {
		l = &codeLock{since: now}
		c.list[id] = l
	}
	l.failures++
	if l.failures >= codeMaxFailures {
		l.until = now.Add(codeWindow)
		l.since, l.failures = now, 0
	}
}

func (c *codeLocks) reset(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.list, id)
}
