package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color/palette"
	"image/gif"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/image/webp"
)

func TestSniff(t *testing.T) {
	heic, err := os.ReadFile("testdata/gps.heic")
	if err != nil {
		t.Fatal(err)
	}
	webpData, err := os.ReadFile("testdata/tiny.webp")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		head []byte
		want format
		ok   bool
	}{
		{"jpeg", testJPEG(t, 8, 8, 1), formatJPEG, true},
		{"png", testPNG(t, nil), formatPNG, true},
		{"webp", webpData, formatWebP, true},
		{"heic", heic, formatHEIF, true},
		{"avif", []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00mif1miafMA1B"), "", false},
		{"gif", []byte("GIF89a\x01\x00\x01\x00"), formatGIF, true},
		{"bmp", []byte("BM\x00\x00\x00\x00"), "", false},
	}
	for _, c := range cases {
		got, ok := sniff(c.head[:min(64, len(c.head))])
		if got != c.want || ok != c.ok {
			t.Errorf("%s: sniff = %q %v，应为 %q %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestFileName(t *testing.T) {
	for _, c := range []struct {
		name string
		f    format
		want string
	}{
		{"IMG_0001.HEIC", formatHEIF, "IMG_0001.HEIC"},
		{"DSC00001.HIF", formatHEIF, "DSC00001.heif"},
		{"截图.PNG", formatPNG, "截图.PNG"},
		{"photo.jpg", formatPNG, "photo.png"},
		{"无扩展名", formatJPEG, "无扩展名.jpg"},
	} {
		if got := fileName(c.name, c.f); got != c.want {
			t.Errorf("fileName(%q) = %q，应为 %q", c.name, got, c.want)
		}
	}
	if got := mimeOf("DSC00001.heif"); got != "image/heif" {
		t.Errorf("mimeOf = %q", got)
	}
}

// pngChunk 拼一个带校验码的 PNG 块。
func pngChunk(typ string, data []byte) []byte {
	b := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(b, uint32(len(data)))
	copy(b[4:], typ)
	b = append(b, data...)
	crc := crc32.ChecksumIEEE(b[4:])
	return binary.BigEndian.AppendUint32(b, crc)
}

// testPNG 生成一张 8×8 的 PNG，在 IHDR 后面插入给定的块。
func testPNG(t *testing.T, extra [][]byte) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	ihdrEnd := 8 + 8 + 13 + 4
	out := append([]byte{}, raw[:ihdrEnd]...)
	for _, c := range extra {
		out = append(out, c...)
	}
	return append(out, raw[ihdrEnd:]...)
}

func TestCleanPNGRemovesLocation(t *testing.T) {
	exif := bytes.TrimPrefix(exifSegment(6), []byte("Exif\x00\x00"))
	in := testPNG(t, [][]byte{
		pngChunk("eXIf", exif),
		pngChunk("iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00<x:xmpmeta exif:GPSLatitude=\"34,15N\"/>")),
		pngChunk("tEXt", []byte("Software\x00test")),
	})
	var out bytes.Buffer
	f, meta, err := cleanImage(bytes.NewReader(in), &out)
	if err != nil || f != formatPNG {
		t.Fatal(f, err)
	}
	if meta.Orientation != 6 || meta.TakenAt == nil {
		t.Errorf("EXIF 没读出来：%+v", meta)
	}
	if bytes.Contains(out.Bytes(), gpsLatitude) || bytes.Contains(out.Bytes(), []byte("GPSLatitude")) {
		t.Error("位置信息还在文件里")
	}
	if !bytes.Contains(out.Bytes(), []byte("Software")) {
		t.Error("普通文本块不该去掉")
	}
	// 校验码重算过，标准解码器照样能读。
	if _, err := png.Decode(bytes.NewReader(out.Bytes())); err != nil {
		t.Errorf("清理后解码失败：%v", err)
	}
}

// testWebP 把 testdata 里的小图包进 VP8X 容器，加上 EXIF 和 XMP 块。
func testWebP(t *testing.T) []byte {
	raw, err := os.ReadFile("testdata/tiny.webp")
	if err != nil {
		t.Fatal(err)
	}
	chunk := func(typ string, data []byte) []byte {
		b := make([]byte, 8, 9+len(data))
		copy(b, typ)
		binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
		b = append(b, data...)
		if len(data)%2 == 1 {
			b = append(b, 0)
		}
		return b
	}
	vp8x := make([]byte, 10)
	vp8x[0] = 0x08 | 0x04   // 有 EXIF 和 XMP
	vp8x[4], vp8x[7] = 7, 7 // 画布 8×8，存的是宽高减 1
	body := []byte("WEBP")
	body = append(body, chunk("VP8X", vp8x)...)
	body = append(body, raw[12:]...) // 原文件里的 VP8L 块
	body = append(body, chunk("EXIF", exifSegment(3))...)
	body = append(body, chunk("XMP ", []byte("<x:xmpmeta exif:GPSLatitude=\"34,15N\"/>"))...)
	out := []byte("RIFF\x00\x00\x00\x00")
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	return append(out, body...)
}

func TestCleanWebPRemovesLocation(t *testing.T) {
	in := testWebP(t)
	if _, err := webp.Decode(bytes.NewReader(in)); err != nil {
		t.Fatalf("测试图本身解不开：%v", err)
	}
	var out bytes.Buffer
	f, meta, err := cleanImage(bytes.NewReader(in), &out)
	if err != nil || f != formatWebP {
		t.Fatal(f, err)
	}
	if meta.Orientation != 3 {
		t.Errorf("方向 = %d，应为 3", meta.Orientation)
	}
	b := out.Bytes()
	if bytes.Contains(b, gpsLatitude) || bytes.Contains(b, []byte("GPSLatitude")) {
		t.Error("位置信息还在文件里")
	}
	if got := int(binary.LittleEndian.Uint32(b[4:])); got != len(b)-8 {
		t.Errorf("RIFF 长度 = %d，应为 %d", got, len(b)-8)
	}
	if b[20]&0x04 != 0 {
		t.Error("XMP 标志没有清掉")
	}
	if _, err := webp.Decode(bytes.NewReader(b)); err != nil {
		t.Errorf("清理后解码失败：%v", err)
	}
}

func TestCleanWebPRejectsAnimation(t *testing.T) {
	in := testWebP(t)
	in[20] |= 0x02
	if _, _, err := cleanImage(bytes.NewReader(in), &bytes.Buffer{}); err == nil {
		t.Error("动图应该拒绝")
	}
}

func TestCleanHEIFRemovesLocation(t *testing.T) {
	raw, err := os.ReadFile("testdata/gps.heic") // heif-enc 从带 GPS 的 JPEG 转来，64×48
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, gpsLatitude) {
		t.Fatal("测试图里应该有 GPS")
	}
	path := filepath.Join(t.TempDir(), "1.jpg")
	var buf bytes.Buffer
	f, _, err := cleanImage(bytes.NewReader(raw), &buf)
	if err != nil || f != formatHEIF {
		t.Fatal(f, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, cfg, err := cleanHEIF(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.TakenAt == nil || meta.Orientation != 1 {
		t.Errorf("EXIF 没读对：%+v", meta)
	}
	if cfg.Width < 64 || cfg.Height < 48 {
		t.Errorf("尺寸 = %d×%d", cfg.Width, cfg.Height)
	}
	out, _ := os.ReadFile(path)
	if len(out) != len(raw) {
		t.Errorf("文件长度变了：%d → %d", len(raw), len(out))
	}
	if bytes.Contains(out, gpsLatitude) {
		t.Error("GPS 还在文件里")
	}
	if _, err := exec.LookPath(heifDecoder); err != nil {
		t.Skip("没有装 libheif（heif-dec），跳过解码检查")
	}
	dir := t.TempDir()
	w, h, err := makeDerivatives(path, filepath.Join(dir, "p.jpg"), filepath.Join(dir, "t.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if w != 64 || h != 48 {
		t.Errorf("解码后尺寸 = %d×%d，应为 64×48", w, h)
	}
	img, err := decodeFile(filepath.Join(dir, "p.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	// 左上角是红色，说明解出来的是原图而且方向没变。
	r, _, b, _ := img.At(4, 4).RGBA()
	if r < b {
		t.Error("左上角应该是红色")
	}
}

func TestCleanGIFKeepsFramesDropsXMP(t *testing.T) {
	anim := &gif.GIF{}
	for i := 0; i < 2; i++ {
		img := image.NewPaletted(image.Rect(0, 0, 8, 8), palette.Plan9)
		for j := range img.Pix {
			img.Pix[j] = uint8(i * 100)
		}
		anim.Image = append(anim.Image, img)
		anim.Delay = append(anim.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	// 在第一帧前面插入一个 XMP 应用扩展块。
	xmp := append([]byte("\x21\xff\x0bXMP DataXMP"), 30)
	xmp = append(xmp, []byte("<x:xmpmeta GPSLatitude=34,15N/>")[:30]...)
	xmp = append(xmp, 0)
	i := bytes.IndexByte(raw[13+3*256:], 0x21) + 13 + 3*256
	in := append(append(append([]byte{}, raw[:i]...), xmp...), raw[i:]...)
	if _, err := gif.DecodeAll(bytes.NewReader(in)); err != nil {
		t.Fatalf("测试图本身解不开：%v", err)
	}
	var out bytes.Buffer
	f, _, err := cleanImage(bytes.NewReader(in), &out)
	if err != nil || f != formatGIF {
		t.Fatal(f, err)
	}
	if bytes.Contains(out.Bytes(), []byte("GPSLatitude")) {
		t.Error("XMP 还在文件里")
	}
	got, err := gif.DecodeAll(bytes.NewReader(out.Bytes()))
	if err != nil || len(got.Image) != 2 {
		t.Fatalf("清理后应该还是两帧的动图：%v", err)
	}
}

func TestSubOffset(t *testing.T) {
	// free 和 idat 两个盒子的内容一样，按内容查找会找到前面那个，按位置算才对。
	buf := []byte("\x00\x00\x00\x0cfreeabcd\x00\x00\x00\x0cidatabcd")
	idat := boxes(buf)["idat"]
	if got := subOffset(buf, idat); got != 20 {
		t.Errorf("idat 数据的位置 = %d，应为 20", got)
	}
}
