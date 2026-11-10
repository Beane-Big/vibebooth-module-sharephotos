// 照片分享的后端。网关已经校验过登录，这里用 SDK 拿到当前用户；数据存在本模块卷里的 SQLite 文件，照片存在同一个卷里。
// 分享页的接口在 /api/sharephotos/s/ 下，模块声明了 public-share 特权，网关对这个前缀不做登录校验，
// 所以这些接口不套 SDK 的校验，由链接里的令牌、有效期和提取码控制访问（见 share.go）。
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Beane-Big/vibebooth/packages/sdk-go/auth"
)

const moduleID = "sharephotos"

// 功能权限，声明在 module.yaml 的 permissions 里，管理员在门户“用户”页按人分配。
const (
	permUpload = "upload" // 建相册、上传、分享、续期、删除自己的相册
	permView   = "view"   // 在门户里看所有还在有效期内的相册
)

type App struct {
	store  *Store
	files  *Files
	secret []byte
	locks  *codeLocks
}

func main() {
	dataDir := env("DATA_DIR", "/data")
	store, err := OpenStore(filepath.Join(dataDir, "sharephotos.db"))
	if err != nil {
		slog.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	ctx := context.Background()
	secret, err := store.Secret(ctx)
	if err != nil {
		slog.Error("读取密钥失败", "err", err)
		os.Exit(1)
	}
	app := &App{store: store, files: NewFiles(dataDir, store), secret: secret, locks: newCodeLocks()}
	if s, err := store.Settings(ctx); err == nil {
		app.files.SetRate(s.RateMbps)
	}
	go app.files.Work(ctx)
	go app.cleanupLoop(ctx)

	// 独立模式（npm run standalone）：离开平台单独运行时不校验令牌，当成一个拥有全部权限的本地用户。平台不会设这个变量。
	guard := auth.NewVerifier(env("JWKS_URL", "http://core:8080/api/platform/.well-known/jwks.json"), moduleID).Middleware
	if os.Getenv("STANDALONE") == "1" {
		slog.Warn("独立模式：不校验令牌，所有请求都是拥有全部权限的本地用户")
		guard = auth.Standalone("本地用户")
	}

	// 上传大文件和慢速下载都可能很久，所以只限制读请求头的时间。
	srv := &http.Server{Addr: ":8000", Handler: app.handler(guard), ReadHeaderTimeout: 10 * time.Second}
	slog.Info("sharephotos 模块已启动", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("服务退出", "err", err)
		os.Exit(1)
	}
}

// handler 组装全部接口。guard 是登录校验：平台里用 SDK 校验网关注入的令牌，独立模式和测试里换成固定的用户。
func (app *App) handler(guard func(http.Handler) http.Handler) http.Handler {
	// 对内接口：只给本模块前端用，需要登录。
	api := http.NewServeMux()
	api.HandleFunc("GET /api/sharephotos/me", func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFrom(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"name": user.Name, "menus": user.Menus, "perms": user.Perms, "all": user.All})
	})
	app.adminRoutes(api)

	mux := http.NewServeMux()
	// 健康检查由平台在内部网络直接调用，不经过网关，也不需要令牌。
	mux.HandleFunc("GET /api/sharephotos/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// 分享页的接口不需要登录，只挂这几个具体的路径；/api/sharephotos/s/ 下的其他网址仍然走下面的登录校验。
	app.shareRoutes(mux)
	mux.Handle("/api/sharephotos/", guard(api))
	return mux
}

// cleanupLoop 每小时删除一次保留期已满的相册，连同照片文件。
func (app *App) cleanupLoop(ctx context.Context) {
	for {
		albums, err := app.store.ExpiredAlbums(ctx, time.Now())
		if err != nil {
			slog.Error("检查过期相册失败", "err", err)
		}
		for _, a := range albums {
			if err := app.deleteAlbum(ctx, a.ID); err != nil {
				slog.Error("删除过期相册失败", "album", a.ID, "err", err)
				continue
			}
			slog.Info("删除过期相册", "album", a.ID, "title", a.Title)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// deleteAlbum 先删数据库记录再删文件：记录没了别人就打不开，文件删到一半也不会留下能访问的残缺相册。
func (app *App) deleteAlbum(ctx context.Context, id int64) error {
	if err := app.store.DeleteAlbum(ctx, id); err != nil {
		return err
	}
	return app.files.RemoveAlbum(id)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
