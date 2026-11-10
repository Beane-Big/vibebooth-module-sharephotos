package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// gpsLatitude 是测试用的纬度原始字节（3 个 RATIONAL），清理后不应该再出现在文件里。
var gpsLatitude = []byte{0, 0, 0, 34, 0, 0, 0, 1, 0, 0, 0, 15, 0, 0, 0, 1, 0, 0, 0x30, 0x39, 0, 0, 0, 100}

// exifSegment 拼一个最小的 EXIF：IFD0 里有方向、Exif 子目录（拍摄时间）和 GPS 目录（纬度）。用大端字节序。
func exifSegment(orientation int) []byte {
	be := binary.BigEndian
	t := []byte("MM\x00\x2a\x00\x00\x00\x08")
	entry := func(tag, typ, count, value int) []byte {
		b := make([]byte, 12)
		be.PutUint16(b, uint16(tag))
		be.PutUint16(b[2:], uint16(typ))
		be.PutUint32(b[4:], uint32(count))
		be.PutUint32(b[8:], uint32(value))
		return b
	}
	// IFD0 在 8，3 项：8 + 2 + 36 + 4 = 50
	exifIFD := 50
	// Exif IFD：1 项，2 + 12 + 4 = 18，数据（拍摄时间 20 字节）紧跟在后面
	dateOff := exifIFD + 18
	gpsIFD := dateOff + 20
	// GPS IFD：1 项，数据（24 字节）紧跟在后面
	latOff := gpsIFD + 18

	t = append(t, 0, 3)
	o := entry(0x0112, 3, 1, 0)
	be.PutUint16(o[8:], uint16(orientation))
	t = append(t, o...)
	t = append(t, entry(0x8769, 4, 1, exifIFD)...)
	t = append(t, entry(0x8825, 4, 1, gpsIFD)...)
	t = append(t, 0, 0, 0, 0)

	t = append(t, 0, 1)
	t = append(t, entry(0x9003, 2, 20, dateOff)...)
	t = append(t, 0, 0, 0, 0)
	t = append(t, []byte("2026:11:08 15:04:05\x00")...)

	t = append(t, 0, 1)
	t = append(t, entry(0x0002, 5, 3, latOff)...)
	t = append(t, 0, 0, 0, 0)
	t = append(t, gpsLatitude...)

	return append([]byte("Exif\x00\x00"), t...)
}

func segment(marker byte, data []byte) []byte {
	b := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(len(data)+2))
	return append(b, data...)
}

// testJPEG 生成一张 w×h 的图，左上角是红色，其余是蓝色，再插入 EXIF 和一段 XMP。
func testJPEG(t *testing.T, w, h, orientation int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{0, 0, 255, 255}
			if x < w/4 && y < h/4 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	out := append([]byte{}, raw[:2]...)
	out = append(out, segment(0xE1, exifSegment(orientation))...)
	out = append(out, segment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta exif:GPSLatitude=\"34,15N\"/>"))...)
	return append(out, raw[2:]...)
}

func TestCleanJPEGRemovesLocation(t *testing.T) {
	in := testJPEG(t, 64, 48, 6)
	var out bytes.Buffer
	meta, err := cleanJPEG(bytes.NewReader(in), &out)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Orientation != 6 {
		t.Errorf("方向 = %d，应为 6", meta.Orientation)
	}
	if meta.TakenAt == nil || meta.TakenAt.Format("2006-01-02 15:04:05") != "2026-11-08 15:04:05" {
		t.Errorf("拍摄时间 = %v", meta.TakenAt)
	}
	if bytes.Contains(out.Bytes(), gpsLatitude) {
		t.Error("GPS 纬度还在文件里")
	}
	if bytes.Contains(out.Bytes(), []byte("GPSLatitude")) || bytes.Contains(out.Bytes(), []byte("ns.adobe.com")) {
		t.Error("XMP 没有去掉")
	}
	// 去掉位置后方向和拍摄时间还在，图片照样能解码。
	again, err := cleanJPEG(bytes.NewReader(out.Bytes()), &bytes.Buffer{})
	if err != nil || again.Orientation != 6 || again.TakenAt == nil {
		t.Errorf("清理后的 EXIF 读不出来：%+v %v", again, err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(out.Bytes())); err != nil {
		t.Errorf("清理后解码失败：%v", err)
	}
}

func TestCleanJPEGDropsBrokenExif(t *testing.T) {
	raw := testJPEG(t, 16, 16, 1)
	// 把 GPS 目录的偏移改到文件外面：解析不了时整段 EXIF 去掉。
	i := bytes.Index(raw, []byte{0x88, 0x25, 0, 4})
	binary.BigEndian.PutUint32(raw[i+8:], 0xFFFF)
	var out bytes.Buffer
	if _, err := cleanJPEG(bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("Exif\x00\x00")) || bytes.Contains(out.Bytes(), gpsLatitude) {
		t.Error("解析不了的 EXIF 应整段去掉")
	}
}

func TestCleanJPEGRejectsOtherFiles(t *testing.T) {
	if _, err := cleanJPEG(bytes.NewReader([]byte("\x89PNG\r\n")), &bytes.Buffer{}); err != errNotJPEG {
		t.Errorf("err = %v", err)
	}
}

func TestMakeDerivatives(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "1.jpg")
	// 方向 6：相机竖着拍，像素是横的，要顺时针转 90 度。
	if err := os.WriteFile(orig, testJPEG(t, 3000, 2000, 6), 0o644); err != nil {
		t.Fatal(err)
	}
	w, h, err := makeDerivatives(orig, filepath.Join(dir, "p.jpg"), filepath.Join(dir, "t.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if w != 2000 || h != 3000 {
		t.Errorf("转正后的尺寸 = %d×%d，应为 2000×3000", w, h)
	}
	check := func(name string, wantW, wantH int) image.Image {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		img, err := jpeg.Decode(f)
		if err != nil {
			t.Fatal(err)
		}
		if b := img.Bounds(); b.Dx() != wantW || b.Dy() != wantH {
			t.Errorf("%s 尺寸 = %d×%d，应为 %d×%d", name, b.Dx(), b.Dy(), wantW, wantH)
		}
		return img
	}
	preview := check("p.jpg", 1365, 2048)
	check("t.jpg", 266, 400)
	// 原图左上角的红块转 90 度后在右上角。
	r, _, b, _ := preview.At(preview.Bounds().Dx()-20, 20).RGBA()
	if r < b {
		t.Error("预览图没有按方向转正：右上角应该是红色")
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"DSC01234.jpg":     "DSC01234.jpg",
		"../../etc/x.jpg":  "x.jpg",
		`C:\Users\a\b.JPG`: "b.JPG",
		"a\x00b.jpg":       "a_b.jpg",
		"":                 "photo.jpg",
		"公园 拍照 001.jpeg":   "公园 拍照 001.jpeg",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q，应为 %q", in, got, want)
		}
	}
}

func TestCodeLocks(t *testing.T) {
	c := newCodeLocks()
	for i := 0; i < codeMaxFailures-1; i++ {
		c.fail(1)
	}
	if c.blocked(1) > 0 {
		t.Fatal("还没到次数就锁了")
	}
	c.fail(1)
	if c.blocked(1) <= 0 {
		t.Fatal("输错 10 次应该锁住")
	}
	if c.blocked(2) > 0 {
		t.Error("别的相册不受影响")
	}
	c.reset(1)
	if c.blocked(1) > 0 {
		t.Error("输对以后应解锁")
	}
}
