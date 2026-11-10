package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Beane-Big/vibebooth/packages/sdk-go/auth"
	"github.com/golang-jwt/jwt/v5"
)

// 测试里模拟平台：自己签令牌，用一个假的 JWKS 地址把公钥交给 SDK 校验，和真实环境走同一条校验路径。
type testPlatform struct {
	key  *rsa.PrivateKey
	jwks *httptest.Server
}

func newTestPlatform(t *testing.T) *testPlatform {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	body, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig",
		"n": enc.EncodeToString(key.N.Bytes()), "e": enc.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(body) }))
	t.Cleanup(srv.Close)
	return &testPlatform{key: key, jwks: srv}
}

func (p *testPlatform) token(t *testing.T, sub, name string, perms ...string) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "home-platform", "aud": moduleID, "sub": sub, "name": name, "menus": []string{"home"}, "perms": perms,
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	tok.Header["kid"] = "test"
	s, err := tok.SignedString(p.key)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + s
}

type client struct {
	t      *testing.T
	srv    *httptest.Server
	auth   string
	cookie []*http.Cookie
}

func (c *client) do(method, path string, body io.Reader, contentType string) *http.Response {
	req, _ := http.NewRequest(method, c.srv.URL+path, body)
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, ck := range c.cookie {
		req.AddCookie(ck)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { res.Body.Close() })
	return res
}

func (c *client) json(method, path string, in any, out any) int {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	res := c.do(method, path, body, "application/json")
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func (c *client) upload(albumID int64, name string, data []byte) *http.Response {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	return c.do("POST", "/api/sharephotos/albums/"+itoa(albumID)+"/photos", &buf, mw.FormDataContentType())
}

func itoa(n int64) string { return big.NewInt(n).String() }

func newTestApp(t *testing.T) (*App, *httptest.Server, *testPlatform) {
	dir := t.TempDir()
	store, err := OpenStore(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := store.Secret(context.Background())
	app := &App{store: store, files: NewFiles(dir, store), secret: secret, locks: newCodeLocks()}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go app.files.Work(ctx)
	p := newTestPlatform(t)
	srv := httptest.NewServer(app.handler(auth.NewVerifier(p.jwks.URL, moduleID).Middleware))
	t.Cleanup(srv.Close)
	return app, srv, p
}

func waitReady(t *testing.T, app *App, a *Album) {
	for i := 0; i < 100; i++ {
		photos, _ := app.store.Photos(context.Background(), a)
		if len(photos) > 0 && photos[0].Status == statusReady {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("预览图一直没生成好")
}

func TestShareFlow(t *testing.T) {
	app, srv, p := newTestApp(t)
	owner := &client{t: t, srv: srv, auth: p.token(t, "u1", "爸爸", permUpload, permView)}
	viewer := &client{t: t, srv: srv, auth: p.token(t, "u2", "妈妈", permView)}
	otherUploader := &client{t: t, srv: srv, auth: p.token(t, "u3", "舅舅", permUpload)}
	guest := &client{t: t, srv: srv}

	// 没有“上传照片”权限的人不能建相册。
	if code := viewer.json("POST", "/api/sharephotos/albums", map[string]any{"title": "x"}, nil); code != http.StatusForbidden {
		t.Fatalf("没有权限建相册返回 %d", code)
	}
	var created albumView
	if code := owner.json("POST", "/api/sharephotos/albums", map[string]any{"title": "公园", "withCode": true}, &created); code != http.StatusCreated {
		t.Fatalf("建相册返回 %d", code)
	}
	if created.Token == "" || len(created.Code) != 4 || !created.Mine {
		t.Fatalf("新相册应该带链接和提取码：%+v", created)
	}
	if d := time.Until(created.ExpiresAt); d < 6*24*time.Hour || d > 7*24*time.Hour {
		t.Errorf("默认有效期应为 7 天，实际 %v", d)
	}
	id := itoa(created.ID)

	if res := viewer.upload(created.ID, "a.jpg", testJPEG(t, 64, 48, 1)); res.StatusCode != http.StatusForbidden {
		t.Errorf("别人往相册里传照片返回 %d", res.StatusCode)
	}
	if res := owner.upload(created.ID, "a.png", []byte("\x89PNG")); res.StatusCode != http.StatusBadRequest {
		t.Errorf("传 PNG 返回 %d", res.StatusCode)
	}
	res := owner.upload(created.ID, "a.jpg", testJPEG(t, 64, 48, 1))
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("上传返回 %d：%s", res.StatusCode, b)
	}
	var photo Photo
	json.NewDecoder(res.Body).Decode(&photo)
	album, _ := app.store.Album(context.Background(), created.ID)
	waitReady(t, app, album)
	pid := itoa(photo.ID)

	// 预览照片的人看得到相册，但看不到链接和提取码，也不能改。
	var list []albumView
	viewer.json("GET", "/api/sharephotos/albums", nil, &list)
	if len(list) != 1 || list[0].Token != "" || list[0].Code != "" || list[0].Mine {
		t.Fatalf("预览的人看到的列表不对：%+v", list)
	}
	if code := viewer.json("PATCH", "/api/sharephotos/albums/"+id, map[string]any{"title": "改"}, nil); code != http.StatusForbidden {
		t.Errorf("预览的人改相册返回 %d", code)
	}
	if r := viewer.do("GET", "/api/sharephotos/albums/"+id+"/photos/"+pid+"/thumb", nil, ""); r.StatusCode != http.StatusOK {
		t.Errorf("预览的人取缩略图返回 %d", r.StatusCode)
	}
	// 没有“预览照片”权限的另一个上传者看不到别人的相册。
	otherUploader.json("GET", "/api/sharephotos/albums", nil, &list)
	if len(list) != 0 {
		t.Errorf("别的上传者不应看到这个相册：%+v", list)
	}
	if code := otherUploader.json("DELETE", "/api/sharephotos/albums/"+id, nil, nil); code != http.StatusNotFound {
		t.Errorf("别的上传者删相册返回 %d", code)
	}

	// 分享页：没登录的人凭链接打开，先要提取码。
	share := "/api/sharephotos/s/" + created.Token
	var info map[string]any
	if code := guest.json("GET", share, nil, &info); code != http.StatusOK || info["needCode"] != true || info["photos"] != nil {
		t.Fatalf("没输提取码时分享页返回 %d %v", code, info)
	}
	if r := guest.do("GET", share+"/photos/"+pid+"/preview", nil, ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("没输提取码取图返回 %d", r.StatusCode)
	}
	if code := guest.json("POST", share+"/unlock", map[string]string{"code": "xxxx"}, nil); code != http.StatusForbidden {
		t.Errorf("提取码不对返回 %d", code)
	}
	r := guest.do("POST", share+"/unlock", strings.NewReader(`{"code":"`+created.Code+`"}`), "application/json")
	if r.StatusCode != http.StatusNoContent || len(r.Cookies()) != 1 {
		t.Fatalf("提取码正确返回 %d，Cookie %v", r.StatusCode, r.Cookies())
	}
	guest.cookie = r.Cookies()
	guest.json("GET", share, nil, &info)
	if photos, _ := info["photos"].([]any); len(photos) != 1 {
		t.Fatalf("输对提取码后应看到 1 张照片：%v", info)
	}
	orig := guest.do("GET", share+"/photos/"+pid+"/original", nil, "")
	data, _ := io.ReadAll(orig.Body)
	if orig.StatusCode != http.StatusOK || !strings.HasPrefix(orig.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("下载原图返回 %d %s", orig.StatusCode, orig.Header.Get("Content-Disposition"))
	}
	if bytes.Contains(data, gpsLatitude) || !bytes.Contains(data, []byte("Exif")) {
		t.Error("下载的原图应保留 EXIF、去掉 GPS")
	}
	zr := guest.do("GET", share+"/zip", nil, "")
	zdata, _ := io.ReadAll(zr.Body)
	zf, err := zip.NewReader(bytes.NewReader(zdata), int64(len(zdata)))
	if err != nil || len(zf.File) != 1 || zf.File[0].Name != "a.jpg" {
		t.Fatalf("打包下载不对：%v", err)
	}
	// 分享链接下没有挂的路径照常要登录。
	if r := guest.do("GET", share+"/other", nil, ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("分享前缀下的其他路径返回 %d", r.StatusCode)
	}

	// 换提取码后旧 Cookie 失效。
	owner.json("PATCH", "/api/sharephotos/albums/"+id, map[string]any{"newCode": true}, nil)
	if r := guest.do("GET", share+"/photos/"+pid+"/thumb", nil, ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("换提取码后旧 Cookie 还能用：%d", r.StatusCode)
	}
	// 换链接后旧链接立即失效。
	owner.json("PATCH", "/api/sharephotos/albums/"+id, map[string]any{"resetLink": true}, nil)
	if code := guest.json("GET", share, nil, nil); code != http.StatusNotFound {
		t.Errorf("换链接后旧链接返回 %d", code)
	}

	// 到期：分享页和预览的人都看不到了，上传者本人还看得到。
	album, _ = app.store.Album(context.Background(), created.ID)
	album.ExpiresAt = time.Now().Add(-time.Minute)
	album.RetainDays = 0
	app.store.UpdateAlbum(context.Background(), album)
	if code := guest.json("GET", "/api/sharephotos/s/"+album.Token, nil, nil); code != http.StatusNotFound {
		t.Errorf("到期后分享页返回 %d", code)
	}
	viewer.json("GET", "/api/sharephotos/albums", nil, &list)
	if len(list) != 0 {
		t.Error("到期后预览的人不应再看到")
	}
	owner.json("GET", "/api/sharephotos/albums", nil, &list)
	if len(list) != 1 || !list[0].Expired {
		t.Errorf("到期后上传者应看到已到期的相册：%+v", list)
	}

	// 保留期满后自动删除，连同文件。
	expired, _ := app.store.ExpiredAlbums(context.Background(), time.Now())
	if len(expired) != 1 {
		t.Fatalf("应有 1 个要删的相册，实际 %d", len(expired))
	}
	app.deleteAlbum(context.Background(), expired[0].ID)
	if _, err := os.Stat(app.files.albumDir(created.ID)); !os.IsNotExist(err) {
		t.Error("相册目录没删掉")
	}
}
