package main

import (
	"encoding/json"
	"errors"
	"image"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Beane-Big/vibebooth/packages/sdk-go/auth"
)

const (
	maxUploadBytes = 80 << 20 // 单张照片最大 80 MB
	// 上传后至少要给系统和其他应用留下的磁盘空间。
	diskReserve = 1 << 30
	maxDays     = 365
)

var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// albumView 是门户里看到的相册。分享链接和提取码只给上传者本人看。
type albumView struct {
	*Album
	Mine     bool      `json:"mine"`
	Expired  bool      `json:"expired"`
	DeleteAt time.Time `json:"deleteAt"`
	Token    string    `json:"token,omitempty"`
	Code     string    `json:"code,omitempty"`
}

func view(u *auth.User, a *Album, now time.Time) albumView {
	v := albumView{Album: a, Mine: canManage(u, a), Expired: a.Expired(now), DeleteAt: a.DeleteAt()}
	if v.Mine {
		v.Token, v.Code = a.Token, a.Code
	}
	return v
}

// canManage：有“上传照片”权限的人管理自己建的相册。
func canManage(u *auth.User, a *Album) bool {
	return u.Has(permUpload) && a.OwnerID == u.ID
}

// canSee：有“预览照片”权限的人看所有还在有效期内的相册；链接到期以后只有上传者本人看得到，可以续期或删除。
func canSee(u *auth.User, a *Album, now time.Time) bool {
	return canManage(u, a) || (u.Has(permView) && !a.Expired(now))
}

func (app *App) adminRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/sharephotos/albums", app.listAlbums)
	api.HandleFunc("POST /api/sharephotos/albums", app.createAlbum)
	api.HandleFunc("GET /api/sharephotos/albums/{id}", app.withAlbum(false, app.getAlbum))
	api.HandleFunc("PATCH /api/sharephotos/albums/{id}", app.withAlbum(true, app.updateAlbum))
	api.HandleFunc("DELETE /api/sharephotos/albums/{id}", app.withAlbum(true, app.removeAlbum))
	api.HandleFunc("POST /api/sharephotos/albums/{id}/photos", app.withAlbum(true, app.uploadPhoto))
	api.HandleFunc("DELETE /api/sharephotos/albums/{id}/photos/{photo}", app.withAlbum(true, app.removePhoto))
	api.HandleFunc("GET /api/sharephotos/albums/{id}/photos/{photo}/{kind}", app.withAlbum(false, app.servePhoto))
	api.HandleFunc("GET /api/sharephotos/settings", app.getSettings)
	api.HandleFunc("PUT /api/sharephotos/settings", app.saveSettings)
}

// withAlbum 按网址里的 id 取出相册并检查权限：manage 为 true 时要是上传者本人，否则能看就行。
// 没权限和不存在一样返回 404，不让人借此猜相册 id。
func (app *App) withAlbum(manage bool, h func(http.ResponseWriter, *http.Request, *Album)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFrom(r.Context())
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusNotFound, "没有这个相册")
			return
		}
		a, err := app.store.Album(r.Context(), id)
		if errors.Is(err, errNotFound) || (err == nil && !canSee(u, a, time.Now())) {
			writeError(w, http.StatusNotFound, "没有这个相册，或者它已经到期")
			return
		}
		if err != nil {
			slog.Error("读取相册失败", "err", err)
			writeError(w, http.StatusInternalServerError, "读取失败")
			return
		}
		if manage && !canManage(u, a) {
			writeError(w, http.StatusForbidden, "只有上传者本人能修改这个相册")
			return
		}
		h(w, r, a)
	}
}

func (app *App) listAlbums(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	all, err := app.store.Albums(r.Context())
	if err != nil {
		slog.Error("读取相册失败", "err", err)
		writeError(w, http.StatusInternalServerError, "读取失败")
		return
	}
	now := time.Now()
	list := []albumView{}
	for _, a := range all {
		if canSee(u, a, now) {
			list = append(list, view(u, a, now))
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (app *App) getAlbum(w http.ResponseWriter, r *http.Request, a *Album) {
	photos, err := app.store.Photos(r.Context(), a)
	if err != nil {
		slog.Error("读取照片失败", "err", err)
		writeError(w, http.StatusInternalServerError, "读取失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"album": view(auth.UserFrom(r.Context()), a, time.Now()), "photos": photos, "rateMbps": app.files.RateMbps()})
}

// albumInput 是新建和修改相册时提交的内容；修改时没写的字段不变。
type albumInput struct {
	Title      *string `json:"title"`
	ShotDate   *string `json:"shotDate"`
	Note       *string `json:"note"`
	CoverID    *int64  `json:"coverId"`
	Sort       *string `json:"sort"`
	ExpireDays *int    `json:"expireDays"` // 从现在起再过几天到期（新建和续期）
	RetainDays *int    `json:"retainDays"`
	WithCode   *bool   `json:"withCode"`  // 打开时生成提取码，关闭时去掉
	NewCode    bool    `json:"newCode"`   // 换一个提取码
	ResetLink  bool    `json:"resetLink"` // 换一个链接，旧链接立即失效
}

// apply 校验并写进相册，返回给用户看的错误。
func (in *albumInput) apply(a *Album) (string, error) {
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" || utf8.RuneCountInString(t) > 60 {
			return "名称不能为空，最多 60 个字", nil
		}
		a.Title = t
	}
	if in.ShotDate != nil {
		d := strings.TrimSpace(*in.ShotDate)
		if d != "" && !datePattern.MatchString(d) {
			return "拍摄日期格式不对", nil
		}
		a.ShotDate = d
	}
	if in.Note != nil {
		n := strings.TrimSpace(*in.Note)
		if utf8.RuneCountInString(n) > 300 {
			return "说明最多 300 个字", nil
		}
		a.Note = n
	}
	if in.CoverID != nil {
		a.CoverID = *in.CoverID
	}
	if in.Sort != nil {
		if *in.Sort != "time" && *in.Sort != "name" {
			return "排序方式不对", nil
		}
		a.Sort = *in.Sort
	}
	if in.ExpireDays != nil {
		if *in.ExpireDays < 1 || *in.ExpireDays > maxDays {
			return "有效期要在 1 到 365 天之间", nil
		}
		a.ExpiresAt = time.Now().UTC().AddDate(0, 0, *in.ExpireDays)
	}
	if in.RetainDays != nil {
		if *in.RetainDays < 0 || *in.RetainDays > maxDays {
			return "到期后保留天数要在 0 到 365 之间", nil
		}
		a.RetainDays = *in.RetainDays
	}
	if in.WithCode != nil {
		switch {
		case !*in.WithCode:
			a.Code = ""
		case a.Code == "":
			in.NewCode = true
		}
	}
	if in.NewCode && (a.Code != "" || (in.WithCode != nil && *in.WithCode)) {
		code, err := newCode()
		if err != nil {
			return "", err
		}
		a.Code = code
	}
	if in.ResetLink {
		token, err := newToken()
		if err != nil {
			return "", err
		}
		a.Token = token
	}
	return "", nil
}

func (app *App) createAlbum(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if !u.Has(permUpload) {
		writeError(w, http.StatusForbidden, "没有“上传照片”权限")
		return
	}
	var in albumInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || in.Title == nil {
		writeError(w, http.StatusBadRequest, "请填写相册名称")
		return
	}
	settings, err := app.store.Settings(r.Context())
	if err != nil {
		slog.Error("读取设置失败", "err", err)
	}
	if in.ExpireDays == nil {
		in.ExpireDays = &settings.ExpireDays
	}
	if in.RetainDays == nil {
		in.RetainDays = &settings.RetainDays
	}
	a := &Album{OwnerID: u.ID, OwnerName: u.Name, Sort: "time"}
	msg, err := in.apply(a)
	if err == nil && msg == "" {
		err = app.store.CreateAlbum(r.Context(), a)
	}
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err != nil {
		slog.Error("新建相册失败", "err", err)
		writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusCreated, view(u, a, time.Now()))
}

func (app *App) updateAlbum(w http.ResponseWriter, r *http.Request, a *Album) {
	var in albumInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "格式不对")
		return
	}
	if in.CoverID != nil && *in.CoverID != 0 {
		if _, err := app.store.Photo(r.Context(), a.ID, *in.CoverID); err != nil {
			writeError(w, http.StatusBadRequest, "封面不是这个相册里的照片")
			return
		}
	}
	msg, err := in.apply(a)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err == nil {
		err = app.store.UpdateAlbum(r.Context(), a)
	}
	if err != nil {
		slog.Error("保存相册失败", "err", err)
		writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	app.getAlbum(w, r, a)
}

func (app *App) removeAlbum(w http.ResponseWriter, r *http.Request, a *Album) {
	if err := app.deleteAlbum(r.Context(), a.ID); err != nil {
		slog.Error("删除相册失败", "err", err)
		writeError(w, http.StatusInternalServerError, "删除失败")
		return
	}
	slog.Info("删除相册", "album", a.ID, "title", a.Title, "by", auth.UserFrom(r.Context()).Name)
	w.WriteHeader(http.StatusNoContent)
}

// uploadPhoto 接收一张照片（表单字段 file）。边接收边去掉位置信息写进相册目录，然后排队生成预览图，不等生成完就返回。
func (app *App) uploadPhoto(w http.ResponseWriter, r *http.Request, a *Album) {
	if free, _, ok := diskSpace(app.files.dir); ok && free-r.ContentLength < diskReserve {
		writeError(w, http.StatusInsufficientStorage, "服务器的硬盘空间不够了，先删掉一些旧相册")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "格式不对")
		return
	}
	var part interface {
		io.Reader
		FileName() string
		FormName() string
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			writeError(w, http.StatusBadRequest, "没有收到照片")
			return
		}
		if p.FormName() == "file" {
			part = p
			break
		}
	}
	name := cleanName(part.FileName())
	dir := app.files.albumDir(a.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Error("建相册目录失败", "err", err)
		writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	tmp, err := os.CreateTemp(dir, "upload-*.tmp")
	if err != nil {
		slog.Error("建临时文件失败", "err", err)
		writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	defer os.Remove(tmp.Name())
	// 格式按文件内容判断，不看扩展名；写入时顺便去掉位置信息（HEIF 写完后再就地处理）。
	format, meta, err := cleanImage(part, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			writeError(w, http.StatusRequestEntityTooLarge, name+" 超过 80 MB")
		case errors.Is(err, errUnsupported), errors.Is(err, errNotJPEG):
			writeError(w, http.StatusBadRequest, name+" "+errUnsupported.Error())
		default:
			slog.Warn("上传的照片读取失败", "name", name, "err", err)
			writeError(w, http.StatusBadRequest, name+" 没有传完整，请重试")
		}
		return
	}
	var cfg image.Config
	if format == formatHEIF {
		meta, cfg, err = cleanHEIF(tmp.Name())
		if err == nil {
			err = checkPixels(cfg)
		}
	} else {
		cfg, err = imageConfig(tmp.Name())
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, name+"："+err.Error())
		return
	}
	info, err := os.Stat(tmp.Name())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	width, height := cfg.Width, cfg.Height
	if meta.Orientation >= 5 {
		width, height = height, width
	}
	name = fileName(name, format)
	p := &Photo{AlbumID: a.ID, Name: name, Size: info.Size(), Width: width, Height: height, TakenAt: meta.TakenAt, Status: statusProcessing}
	if err := app.store.AddPhoto(r.Context(), p); err != nil {
		slog.Error("保存照片记录失败", "err", err)
		writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	if err := os.Rename(tmp.Name(), app.files.path(a.ID, p.ID, kindOriginal)); err != nil {
		slog.Error("保存照片文件失败", "err", err)
		app.store.DeletePhoto(r.Context(), a.ID, p.ID)
		writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	app.files.Enqueue(a.ID, p.ID)
	writeJSON(w, http.StatusCreated, p)
}

// cleanName 只留文件名本身，去掉路径和控制字符；下载时用它作文件名。
func cleanName(name string) string {
	if decoded, err := new(mime.WordDecoder).DecodeHeader(name); err == nil {
		name = decoded
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." {
		name = "photo.jpg"
	}
	if utf8.RuneCountInString(name) > 120 {
		ext := filepath.Ext(name)
		name = string([]rune(name)[:100]) + ext
	}
	return name
}

func (app *App) removePhoto(w http.ResponseWriter, r *http.Request, a *Album) {
	id, err := strconv.ParseInt(r.PathValue("photo"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "没有这张照片")
		return
	}
	err = app.store.DeletePhoto(r.Context(), a.ID, id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "没有这张照片")
		return
	}
	if err != nil {
		slog.Error("删除照片失败", "err", err)
		writeError(w, http.StatusInternalServerError, "删除失败")
		return
	}
	app.files.RemovePhoto(a.ID, id)
	w.WriteHeader(http.StatusNoContent)
}

// settingsView 是设置页看到的内容：默认值和服务器硬盘的空间。
type settingsView struct {
	Settings
	DiskFree  int64 `json:"diskFree"`
	DiskTotal int64 `json:"diskTotal"`
}

func (app *App) getSettings(w http.ResponseWriter, r *http.Request) {
	if !auth.UserFrom(r.Context()).Has(permUpload) {
		writeError(w, http.StatusForbidden, "没有“上传照片”权限")
		return
	}
	s, err := app.store.Settings(r.Context())
	if err != nil {
		slog.Error("读取设置失败", "err", err)
		writeError(w, http.StatusInternalServerError, "读取失败")
		return
	}
	free, total, _ := diskSpace(app.files.dir)
	writeJSON(w, http.StatusOK, settingsView{s, free, total})
}

func (app *App) saveSettings(w http.ResponseWriter, r *http.Request) {
	if !auth.UserFrom(r.Context()).Has(permUpload) {
		writeError(w, http.StatusForbidden, "没有“上传照片”权限")
		return
	}
	var in Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "格式不对")
		return
	}
	switch {
	case in.ExpireDays < 1 || in.ExpireDays > maxDays:
		writeError(w, http.StatusBadRequest, "默认有效期要在 1 到 365 天之间")
		return
	case in.RetainDays < 0 || in.RetainDays > maxDays:
		writeError(w, http.StatusBadRequest, "到期后保留天数要在 0 到 365 之间")
		return
	case in.RateMbps < 0 || in.RateMbps > 10000:
		writeError(w, http.StatusBadRequest, "下载限速不对")
		return
	}
	if err := app.store.SaveSettings(r.Context(), in); err != nil {
		slog.Error("保存设置失败", "err", err)
		writeError(w, http.StatusInternalServerError, "保存失败")
		return
	}
	app.files.SetRate(in.RateMbps)
	app.getSettings(w, r)
}
