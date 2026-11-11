package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"time"

	"golang.org/x/image/draw"
)

// 预览图和缩略图的长边（像素）。预览图用来在手机和电脑上看大图，缩略图用在网格里。
const (
	previewSize = 2048
	thumbSize   = 400
	// 超过这个像素数的图片不处理，免得解码时把内存用光（1 亿像素的 RGBA 约 400 MB）。
	maxPixels = 100_000_000
)

// Meta 是从照片的 EXIF 里读出来的信息。
type Meta struct {
	Orientation int        // 1–8，没有时为 1
	TakenAt     *time.Time // 拍摄时间（DateTimeOriginal），按相机时间原样记，不带时区
}

var errNotJPEG = errors.New("不是 JPEG 图片")

// cleanJPEG 复制一张 JPEG，去掉里面的位置信息：EXIF 里的 GPS 数据清零，XMP（Lightroom 导出时也会写 GPS）整段去掉。
// 图像数据原样复制，不重新压缩，所以下载到的原图和上传的画质一样。EXIF 解析不了时整段去掉，宁可丢信息也不漏位置。
func cleanJPEG(r io.Reader, w io.Writer) (Meta, error) {
	meta := Meta{Orientation: 1}
	br := bufio.NewReader(r)
	bw := bufio.NewWriter(w)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return meta, errNotJPEG
	}
	bw.Write(soi[:])
	for {
		b, err := br.ReadByte()
		if err != nil {
			return meta, fmt.Errorf("JPEG 不完整：%w", err)
		}
		if b != 0xFF {
			return meta, errors.New("JPEG 格式不对")
		}
		marker, err := br.ReadByte()
		if err != nil {
			return meta, err
		}
		// 段与段之间可以有多余的 0xFF 填充。
		for marker == 0xFF {
			if marker, err = br.ReadByte(); err != nil {
				return meta, err
			}
		}
		// 没有长度的标记。
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			bw.Write([]byte{0xFF, marker})
			continue
		}
		if marker == 0xD9 {
			bw.Write([]byte{0xFF, marker})
			return meta, bw.Flush()
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return meta, err
		}
		n := int(binary.BigEndian.Uint16(lenBuf[:]))
		if n < 2 {
			return meta, errors.New("JPEG 段长度不对")
		}
		data := make([]byte, n-2)
		if _, err := io.ReadFull(br, data); err != nil {
			return meta, err
		}
		keep := true
		if marker == 0xE1 {
			switch {
			case bytes.HasPrefix(data, []byte("Exif\x00\x00")):
				if !scrubExif(data[6:], &meta) {
					keep = false
				}
			case bytes.HasPrefix(data, []byte("http://ns.adobe.com/xap/1.0/\x00")),
				bytes.HasPrefix(data, []byte("http://ns.adobe.com/xmp/extension/\x00")):
				keep = false
			}
		}
		if keep {
			bw.Write([]byte{0xFF, marker})
			bw.Write(lenBuf[:])
			bw.Write(data)
		}
		// 扫描开始以后是压缩的图像数据，原样复制到文件末尾。
		if marker == 0xDA {
			if _, err := io.Copy(bw, br); err != nil {
				return meta, err
			}
			return meta, bw.Flush()
		}
	}
}

// tiff 是 EXIF 里的 TIFF 结构，按字节序读写，所有读取都检查边界。
type tiff struct {
	b   []byte
	ord binary.ByteOrder
}

func (t tiff) u16(off int) (int, bool) {
	if off < 0 || off+2 > len(t.b) {
		return 0, false
	}
	return int(t.ord.Uint16(t.b[off:])), true
}

func (t tiff) u32(off int) (int, bool) {
	if off < 0 || off+4 > len(t.b) {
		return 0, false
	}
	return int(t.ord.Uint32(t.b[off:])), true
}

// entry 是 IFD 里的一项：标签、类型、个数，以及值或值所在的偏移。
type entry struct{ pos, tag, typ, count, value int }

func (t tiff) entries(ifd int) ([]entry, bool) {
	n, ok := t.u16(ifd)
	if !ok || ifd+2+n*12 > len(t.b) {
		return nil, false
	}
	list := make([]entry, n)
	for i := range list {
		pos := ifd + 2 + i*12
		tag, _ := t.u16(pos)
		typ, _ := t.u16(pos + 2)
		count, _ := t.u32(pos + 4)
		value, _ := t.u32(pos + 8)
		list[i] = entry{pos, tag, typ, count, value}
	}
	return list, true
}

// typeSizes 是各 TIFF 类型每个值的字节数。
var typeSizes = map[int]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8}

// scrubExif 读出方向和拍摄时间，并把 GPS 数据清零。返回 false 表示结构不对，调用方应整段去掉。
func scrubExif(b []byte, meta *Meta) bool {
	if len(b) < 8 {
		return false
	}
	t := tiff{b: b}
	switch string(b[:2]) {
	case "II":
		t.ord = binary.LittleEndian
	case "MM":
		t.ord = binary.BigEndian
	default:
		return false
	}
	ifd0, _ := t.u32(4)
	list, ok := t.entries(ifd0)
	if !ok {
		return false
	}
	for _, e := range list {
		switch e.tag {
		case 0x0112: // Orientation，SHORT，值就在项里
			if v, ok := t.u16(e.pos + 8); ok && v >= 1 && v <= 8 {
				meta.Orientation = v
			}
		case 0x8769: // Exif 子 IFD，里面有拍摄时间
			if sub, ok := t.entries(e.value); ok {
				for _, s := range sub {
					if s.tag == 0x9003 && s.typ == 2 && s.count >= 19 && s.value+19 <= len(b) {
						if tm, err := time.Parse("2006:01:02 15:04:05", string(b[s.value:s.value+19])); err == nil {
							meta.TakenAt = &tm
						}
					}
				}
			}
		case 0x8825: // GPS IFD
			gps, ok := t.entries(e.value)
			if !ok {
				return false
			}
			for _, g := range gps {
				size := typeSizes[g.typ] * g.count
				if size > 4 {
					if g.value < 0 || g.value+size > len(b) {
						return false
					}
					clear(b[g.value : g.value+size])
				}
				clear(b[g.pos : g.pos+12])
			}
			// 项数改成 0，读 EXIF 的软件看到的是一个空的 GPS 目录。
			t.ord.PutUint16(b[e.value:], 0)
		}
	}
	return true
}

// imageConfig 读出照片的尺寸，用来在上传时挡掉太大的图。HEIF 的尺寸由 cleanHEIF 读出。
func imageConfig(path string) (image.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Config{}, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return cfg, errors.New("照片读不出来，可能已经损坏")
	}
	return cfg, checkPixels(cfg)
}

// checkPixels 挡掉像素太多的图。
func checkPixels(cfg image.Config) error {
	if cfg.Width*cfg.Height > maxPixels {
		return fmt.Errorf("图片太大（%d×%d），最多 1 亿像素", cfg.Width, cfg.Height)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return errors.New("照片读不出尺寸，可能已经损坏")
	}
	return nil
}

// makeDerivatives 从原图生成预览图和缩略图，按 EXIF 方向转正，返回转正后原图的宽高。
func makeDerivatives(origPath, previewPath, thumbPath string) (int, int, error) {
	f, meta, err := readMeta(origPath)
	if err != nil {
		return 0, 0, err
	}
	var src image.Image
	if f == formatHEIF {
		src, err = decodeHEIF(origPath)
	} else {
		src, err = decodeFile(origPath)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("解码失败：%w", err)
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	preview := orient(scaleTo(src, previewSize), meta.Orientation)
	src = nil // 原图解码后很大，尽早让它可以被回收
	if err := writeJPEG(previewPath, preview, 82); err != nil {
		return 0, 0, err
	}
	if err := writeJPEG(thumbPath, scaleTo(preview, thumbSize), 78); err != nil {
		return 0, 0, err
	}
	if meta.Orientation >= 5 {
		w, h = h, w
	}
	return w, h, nil
}

func decodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(bufio.NewReader(f))
	return img, err
}

// scaleTo 等比缩小到长边不超过 size；本来就小的图只转成 RGBA，不放大。
func scaleTo(src image.Image, size int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > size || h > size {
		if w >= h {
			w, h = size, max(1, h*size/b.Dx())
		} else {
			w, h = max(1, w*size/b.Dy()), size
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// orient 按 EXIF 方向（1–8）把图转正。
func orient(src *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			si := src.PixOffset(x, y)
			di := dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

// writeJPEG 先写到临时文件再改名，处理到一半时别人读不到半张图。
func writeJPEG(path string, img image.Image, quality int) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	err = jpeg.Encode(bw, img, &jpeg.Options{Quality: quality})
	if err == nil {
		err = bw.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
