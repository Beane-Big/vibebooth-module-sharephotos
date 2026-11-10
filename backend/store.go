package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// Album 是一次分享：一组照片、一个分享链接和它的有效期。
type Album struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	ShotDate   string    `json:"shotDate"` // 拍摄日期 YYYY-MM-DD，可以不填
	Note       string    `json:"note"`
	OwnerID    string    `json:"-"`
	OwnerName  string    `json:"ownerName"`
	CoverID    int64     `json:"coverId"` // 0 表示用第一张
	Sort       string    `json:"sort"`    // time：按拍摄时间；name：按文件名
	Token      string    `json:"-"`
	Code       string    `json:"-"` // 提取码，空表示不要
	ExpiresAt  time.Time `json:"expiresAt"`
	RetainDays int       `json:"retainDays"` // 链接到期后照片再保留几天
	Views      int       `json:"views"`
	CreatedAt  time.Time `json:"createdAt"`

	// 下面几项由照片表汇总。Cover 是实际用的封面：设了封面用封面，没设时用第一张处理好的照片，没有照片时是 0。
	Cover      int64 `json:"cover"`
	Count      int   `json:"count"`
	Ready      int   `json:"ready"`
	TotalBytes int64 `json:"totalBytes"`
}

// DeleteAt 是照片被自动删除的时间：链接到期后再过 RetainDays 天。
func (a *Album) DeleteAt() time.Time { return a.ExpiresAt.AddDate(0, 0, a.RetainDays) }

// Expired 表示链接已经失效：分享页打不开，门户里只有上传者看得到。
func (a *Album) Expired(now time.Time) bool { return !now.Before(a.ExpiresAt) }

// Photo 是相册里的一张照片。原图存在磁盘上，预览图和缩略图在上传后由后台生成。
type Photo struct {
	ID        int64      `json:"id"`
	AlbumID   int64      `json:"-"`
	Name      string     `json:"name"`
	Size      int64      `json:"size"`
	Width     int        `json:"width"`
	Height    int        `json:"height"`
	TakenAt   *time.Time `json:"takenAt"`
	Status    string     `json:"status"` // processing、ready、failed
	Downloads int        `json:"downloads"`
	CreatedAt time.Time  `json:"createdAt"`
}

const (
	statusProcessing = "processing"
	statusReady      = "ready"
	statusFailed     = "failed"
)

// Settings 是上传者共用的默认值，新建相册时带出来，每个相册还可以单独改。
type Settings struct {
	ExpireDays int `json:"expireDays"` // 链接默认几天到期
	RetainDays int `json:"retainDays"` // 到期后照片默认再保留几天
	RateMbps   int `json:"rateMbps"`   // 下载原图的总带宽上限，0 表示不限
}

var defaultSettings = Settings{ExpireDays: 7, RetainDays: 7, RateMbps: 4}

var errNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

// 表结构在启动时创建；以后加字段用 ALTER TABLE，并保证老数据还能读。
const schema = `
CREATE TABLE IF NOT EXISTS albums (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	title       TEXT NOT NULL,
	shot_date   TEXT NOT NULL DEFAULT '',
	note        TEXT NOT NULL DEFAULT '',
	owner_id    TEXT NOT NULL,
	owner_name  TEXT NOT NULL,
	cover_id    INTEGER NOT NULL DEFAULT 0,
	sort        TEXT NOT NULL DEFAULT 'time',
	token       TEXT NOT NULL UNIQUE,
	code        TEXT NOT NULL DEFAULT '',
	expires_at  TIMESTAMP NOT NULL,
	retain_days INTEGER NOT NULL,
	views       INTEGER NOT NULL DEFAULT 0,
	created_at  TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS photos (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	album_id   INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	width      INTEGER NOT NULL DEFAULT 0,
	height     INTEGER NOT NULL DEFAULT 0,
	taken_at   TIMESTAMP,
	status     TEXT NOT NULL,
	downloads  INTEGER NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS photos_album ON photos(album_id);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// OpenStore 打开模块自己的 SQLite 文件；数据库只属于本模块，放在本模块的卷里。
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite 同一时间只允许一个写入者，单连接可以避免 "database is locked"。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

const albumColumns = `a.id, a.title, a.shot_date, a.note, a.owner_id, a.owner_name, a.cover_id, a.sort, a.token, a.code,
	a.expires_at, a.retain_days, a.views, a.created_at,
	COALESCE(NULLIF(a.cover_id, 0), (SELECT id FROM photos p WHERE p.album_id = a.id AND p.status = 'ready'
		ORDER BY CASE WHEN a.sort = 'name' THEN 0 ELSE p.taken_at IS NULL END, CASE WHEN a.sort = 'name' THEN NULL ELSE p.taken_at END, p.name, p.id LIMIT 1), 0),
	(SELECT COUNT(*) FROM photos p WHERE p.album_id = a.id),
	(SELECT COUNT(*) FROM photos p WHERE p.album_id = a.id AND p.status = 'ready'),
	(SELECT COALESCE(SUM(size), 0) FROM photos p WHERE p.album_id = a.id)`

func scanAlbum(row interface{ Scan(...any) error }) (*Album, error) {
	var a Album
	err := row.Scan(&a.ID, &a.Title, &a.ShotDate, &a.Note, &a.OwnerID, &a.OwnerName, &a.CoverID, &a.Sort, &a.Token, &a.Code,
		&a.ExpiresAt, &a.RetainDays, &a.Views, &a.CreatedAt, &a.Cover, &a.Count, &a.Ready, &a.TotalBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return &a, err
}

// Albums 返回全部相册，新的在前；谁能看到哪些由调用方按权限过滤。
func (s *Store) Albums(ctx context.Context) ([]*Album, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+albumColumns+` FROM albums a ORDER BY a.created_at DESC, a.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []*Album{}
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

func (s *Store) Album(ctx context.Context, id int64) (*Album, error) {
	return scanAlbum(s.db.QueryRowContext(ctx, `SELECT `+albumColumns+` FROM albums a WHERE a.id = ?`, id))
}

func (s *Store) AlbumByToken(ctx context.Context, token string) (*Album, error) {
	return scanAlbum(s.db.QueryRowContext(ctx, `SELECT `+albumColumns+` FROM albums a WHERE a.token = ?`, token))
}

func (s *Store) CreateAlbum(ctx context.Context, a *Album) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	a.Token, a.CreatedAt = token, time.Now().UTC()
	if a.Sort == "" {
		a.Sort = "time"
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO albums (title, shot_date, note, owner_id, owner_name, sort, token, code, expires_at, retain_days, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Title, a.ShotDate, a.Note, a.OwnerID, a.OwnerName, a.Sort, a.Token, a.Code, a.ExpiresAt, a.RetainDays, a.CreatedAt)
	if err != nil {
		return err
	}
	a.ID, err = res.LastInsertId()
	return err
}

// UpdateAlbum 保存相册里可以改的字段（名称、说明、封面、排序、链接、提取码、有效期）。
func (s *Store) UpdateAlbum(ctx context.Context, a *Album) error {
	_, err := s.db.ExecContext(ctx, `UPDATE albums SET title = ?, shot_date = ?, note = ?, cover_id = ?, sort = ?, token = ?, code = ?,
		expires_at = ?, retain_days = ? WHERE id = ?`,
		a.Title, a.ShotDate, a.Note, a.CoverID, a.Sort, a.Token, a.Code, a.ExpiresAt, a.RetainDays, a.ID)
	return err
}

func (s *Store) DeleteAlbum(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM albums WHERE id = ?`, id)
	return err
}

func (s *Store) AddView(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE albums SET views = views + 1 WHERE id = ?`, id)
	return err
}

// ExpiredAlbums 返回保留期已满、该删除的相册。
func (s *Store) ExpiredAlbums(ctx context.Context, now time.Time) ([]*Album, error) {
	all, err := s.Albums(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Album
	for _, a := range all {
		if !now.Before(a.DeleteAt()) {
			out = append(out, a)
		}
	}
	return out, nil
}

const photoColumns = `id, album_id, name, size, width, height, taken_at, status, downloads, created_at`

func scanPhoto(row interface{ Scan(...any) error }) (*Photo, error) {
	var p Photo
	var taken sql.NullTime
	err := row.Scan(&p.ID, &p.AlbumID, &p.Name, &p.Size, &p.Width, &p.Height, &taken, &p.Status, &p.Downloads, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if taken.Valid {
		t := taken.Time
		p.TakenAt = &t
	}
	return &p, err
}

// Photos 按相册的排序方式列出照片：按拍摄时间时，没有拍摄时间的排在后面，再按文件名。
func (s *Store) Photos(ctx context.Context, a *Album) ([]*Photo, error) {
	order := `taken_at IS NULL, taken_at, name, id`
	if a.Sort == "name" {
		order = `name, id`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+photoColumns+` FROM photos WHERE album_id = ? ORDER BY `+order, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []*Photo{}
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (s *Store) Photo(ctx context.Context, albumID, id int64) (*Photo, error) {
	return scanPhoto(s.db.QueryRowContext(ctx, `SELECT `+photoColumns+` FROM photos WHERE album_id = ? AND id = ?`, albumID, id))
}

// PhotosByStatus 用于启动时把上次没处理完的照片重新排队。
func (s *Store) PhotosByStatus(ctx context.Context, status string) ([]*Photo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+photoColumns+` FROM photos WHERE status = ? ORDER BY id`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (s *Store) AddPhoto(ctx context.Context, p *Photo) error {
	p.CreatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO photos (album_id, name, size, width, height, taken_at, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, p.AlbumID, p.Name, p.Size, p.Width, p.Height, p.TakenAt, p.Status, p.CreatedAt)
	if err != nil {
		return err
	}
	p.ID, err = res.LastInsertId()
	return err
}

func (s *Store) SetPhotoStatus(ctx context.Context, id int64, status string, width, height int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE photos SET status = ?, width = ?, height = ? WHERE id = ?`, status, width, height, id)
	return err
}

func (s *Store) DeletePhoto(ctx context.Context, albumID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM photos WHERE album_id = ? AND id = ?`, albumID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	// 删的是封面时改回默认（第一张）。
	_, err = s.db.ExecContext(ctx, `UPDATE albums SET cover_id = 0 WHERE id = ? AND cover_id = ?`, albumID, id)
	return err
}

func (s *Store) AddDownloads(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE photos SET downloads = downloads + 1 WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) setSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) Settings(ctx context.Context) (Settings, error) {
	out := defaultSettings
	for key, dst := range map[string]*int{"expire_days": &out.ExpireDays, "retain_days": &out.RetainDays, "rate_mbps": &out.RateMbps} {
		v, err := s.setting(ctx, key)
		if err != nil {
			return out, err
		}
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
	return out, nil
}

func (s *Store) SaveSettings(ctx context.Context, in Settings) error {
	for key, v := range map[string]int{"expire_days": in.ExpireDays, "retain_days": in.RetainDays, "rate_mbps": in.RateMbps} {
		if err := s.setSetting(ctx, key, strconv.Itoa(v)); err != nil {
			return err
		}
	}
	return nil
}

// Secret 返回用来签提取码 Cookie 的密钥，第一次用时随机生成并存进数据库。
func (s *Store) Secret(ctx context.Context) ([]byte, error) {
	v, err := s.setting(ctx, "cookie_secret")
	if err != nil {
		return nil, err
	}
	if v == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		v = hex.EncodeToString(b)
		if err := s.setSetting(ctx, "cookie_secret", v); err != nil {
			return nil, err
		}
	}
	return hex.DecodeString(v)
}

// newToken 生成分享链接里的令牌：128 位随机数，猜不到。
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newCode 生成 4 位数字提取码。
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", n.Int64()), nil
}
