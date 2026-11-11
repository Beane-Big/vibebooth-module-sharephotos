package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	_ "image/gif" // 注册 GIF 解码（只取第一帧做预览图），image.Decode 用
	"image/jpeg"
	_ "image/png" // 注册 PNG 解码
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/webp" // 注册 WebP 解码
)

// 能上传的照片格式：手机自己就能打开的几种。原图原样保存（只去掉位置信息），预览图和缩略图统一生成 JPEG。
type format string

const (
	formatJPEG format = "jpeg"
	formatPNG  format = "png"
	formatWebP format = "webp"
	formatGIF  format = "gif"
	formatHEIF format = "heif" // HEIC 和 HEIF 是同一种文件结构，iPhone 拍的是 HEIC，相机（如索尼）存的 .HIF 也是它
)

// 每种格式下载时用的扩展名和 Content-Type；扩展名列表的第一个是默认的。
var formats = map[format]struct {
	exts []string
	mime string
}{
	formatJPEG: {[]string{".jpg", ".jpeg"}, "image/jpeg"},
	formatPNG:  {[]string{".png"}, "image/png"},
	formatWebP: {[]string{".webp"}, "image/webp"},
	formatGIF:  {[]string{".gif"}, "image/gif"},
	formatHEIF: {[]string{".heif", ".heic"}, "image/heif"}, // .heic 的 Content-Type 用 image/heic，见 mimeOf
}

const supportedText = "JPEG、PNG、WebP、HEIC/HEIF、GIF"

var errUnsupported = errors.New("不是支持的照片格式（" + supportedText + "）")

// sniff 按文件开头的字节判断格式，不看扩展名。
func sniff(head []byte) (format, bool) {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return formatJPEG, true
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return formatPNG, true
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return formatWebP, true
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return formatGIF, true
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		// ftyp 里的品牌说明是哪种 HEIF。AVIF 也是这种结构，但手机很少存，不收。
		n := int(binary.BigEndian.Uint32(head))
		if n < 16 || n > len(head) {
			n = len(head)
		}
		brands := []string{string(head[8:12])}
		for i := 16; i+4 <= n; i += 4 {
			brands = append(brands, string(head[i:i+4]))
		}
		if brands[0] == "avif" || brands[0] == "avis" {
			return "", false
		}
		for _, b := range brands {
			switch b {
			case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
				return formatHEIF, true
			}
		}
	}
	return "", false
}

// fileName 让文件名的扩展名和实际格式一致，手机靠扩展名决定用什么打开。比如相机的 .HIF 改成 .heif。
func fileName(name string, f format) string {
	ext := filepath.Ext(name)
	for _, e := range formats[f].exts {
		if strings.EqualFold(ext, e) {
			return name
		}
	}
	return strings.TrimSuffix(name, ext) + formats[f].exts[0]
}

// mimeOf 按文件名（上传时已经和格式对齐）给出下载原图的 Content-Type。
func mimeOf(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".heic" {
		return "image/heic"
	}
	for _, f := range formats {
		for _, e := range f.exts {
			if e == ext {
				return f.mime
			}
		}
	}
	return "application/octet-stream"
}

// cleanImage 把上传的照片写到 w，同时去掉位置信息，返回格式和 EXIF 里的方向、拍摄时间。
// HEIF 的元数据可能在文件任何位置，没法边读边改：先原样写下，调用方再用 cleanHEIF 就地处理。
func cleanImage(r io.Reader, w io.Writer) (format, Meta, error) {
	br := bufio.NewReaderSize(r, 4096)
	head, _ := br.Peek(64)
	f, ok := sniff(head)
	if !ok {
		return "", Meta{Orientation: 1}, errUnsupported
	}
	var meta Meta
	var err error
	switch f {
	case formatJPEG:
		meta, err = cleanJPEG(br, w)
	case formatPNG:
		meta, err = cleanPNG(br, w)
	case formatWebP:
		meta, err = cleanWebP(br, w)
	case formatGIF:
		meta = Meta{Orientation: 1}
		err = cleanGIF(br, w)
	case formatHEIF:
		meta = Meta{Orientation: 1}
		_, err = io.Copy(w, br)
	}
	return f, meta, err
}

// readMeta 只读出方向和拍摄时间，不改文件。HEIF 的方向由解码器处理，这里不需要。
func readMeta(path string) (format, Meta, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", Meta{Orientation: 1}, err
	}
	defer file.Close()
	return cleanImage(file, io.Discard)
}

// ---- PNG ----

// cleanPNG 逐块复制 PNG：eXIf 块里的 GPS 清零后重算校验码，XMP 文本块去掉。
func cleanPNG(r io.Reader, w io.Writer) (Meta, error) {
	meta := Meta{Orientation: 1}
	bw := bufio.NewWriter(w)
	var sig [8]byte
	if _, err := io.ReadFull(r, sig[:]); err != nil {
		return meta, errUnsupported
	}
	bw.Write(sig[:])
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return meta, fmt.Errorf("PNG 不完整：%w", err)
		}
		n := binary.BigEndian.Uint32(hdr[:4])
		if n > maxUploadBytes {
			return meta, errors.New("PNG 块长度不对")
		}
		typ := string(hdr[4:8])
		body := make([]byte, int(n)+4) // 数据加 4 字节校验码
		if _, err := io.ReadFull(r, body); err != nil {
			return meta, fmt.Errorf("PNG 不完整：%w", err)
		}
		keep := true
		switch typ {
		case "eXIf":
			if scrubExif(body[:n], &meta) {
				crc := crc32.NewIEEE()
				crc.Write(hdr[4:8])
				crc.Write(body[:n])
				binary.BigEndian.PutUint32(body[n:], crc.Sum32())
			} else {
				keep = false
			}
		case "iTXt", "tEXt", "zTXt":
			keep = !bytes.HasPrefix(body, []byte("XML:com.adobe.xmp\x00"))
		}
		if keep {
			bw.Write(hdr[:])
			bw.Write(body)
		}
		if typ == "IEND" {
			return meta, bw.Flush()
		}
	}
}

// ---- WebP ----

// cleanWebP 复制 WebP：EXIF 块里的 GPS 清零，XMP 块去掉。WebP 头里记着总长度，所以整个读进内存再写。
func cleanWebP(r io.Reader, w io.Writer) (Meta, error) {
	meta := Meta{Orientation: 1}
	data, err := io.ReadAll(r)
	if err != nil {
		return meta, err
	}
	if len(data) < 12 {
		return meta, errUnsupported
	}
	out := make([]byte, 12, len(data))
	copy(out, data[:12])
	vp8x := -1
	for off := 12; off < len(data); {
		if off+8 > len(data) {
			return meta, errors.New("WebP 不完整")
		}
		typ := string(data[off : off+4])
		n := int(binary.LittleEndian.Uint32(data[off+4:]))
		end := off + 8 + n + n%2 // 奇数长度后面补一个字节
		if n < 0 || end > len(data) {
			if off+8+n == len(data) { // 有的文件最后一块没有补齐
				end = len(data)
			} else {
				return meta, errors.New("WebP 不完整")
			}
		}
		chunk := data[off:end]
		keep := true
		switch typ {
		case "VP8X":
			vp8x = len(out)
			if n >= 1 && chunk[8]&0x02 != 0 {
				return meta, errors.New("不支持动图")
			}
		case "ANIM", "ANMF":
			return meta, errors.New("不支持动图")
		case "EXIF":
			exif := chunk[8 : 8+n]
			exif = bytes.TrimPrefix(exif, []byte("Exif\x00\x00"))
			keep = scrubExif(exif, &meta)
		case "XMP ":
			keep = false
		}
		if keep {
			out = append(out, chunk...)
		} else if vp8x >= 0 {
			// VP8X 的标志位里记着有没有 EXIF（0x08）和 XMP（0x04），去掉的块要把标志也清掉。
			if typ == "EXIF" {
				out[vp8x+8] &^= 0x08
			} else {
				out[vp8x+8] &^= 0x04
			}
		}
		off = end
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	_, err = w.Write(out)
	return meta, err
}

// ---- GIF ----

// cleanGIF 逐块复制 GIF（动图的每一帧都保留），去掉 XMP 扩展块。GIF 没有 EXIF。
func cleanGIF(r *bufio.Reader, w io.Writer) error {
	bw := bufio.NewWriter(w)
	bad := errors.New("GIF 不完整")
	var hdr [13]byte // 文件头 6 字节 + 逻辑屏幕描述 7 字节
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return bad
	}
	bw.Write(hdr[:])
	// 全局颜色表
	if err := copyColorTable(r, bw, hdr[10]); err != nil {
		return bad
	}
	for {
		b, err := r.ReadByte()
		if err != nil {
			return bad
		}
		switch b {
		case 0x3B: // 文件结束
			bw.WriteByte(b)
			return bw.Flush()
		case 0x21: // 扩展块：标签，然后是一串子块
			label, err := r.ReadByte()
			if err != nil {
				return bad
			}
			var blocks bytes.Buffer
			if err := copySubBlocks(r, &blocks); err != nil {
				return bad
			}
			// 应用扩展的第一个子块是 11 字节的应用名，"XMP DataXMP" 是 XMP。
			if label == 0xFF && bytes.HasPrefix(blocks.Bytes(), []byte("\x0bXMP DataXMP")) {
				continue
			}
			bw.Write([]byte{b, label})
			bw.Write(blocks.Bytes())
		case 0x2C: // 一帧图像：描述 9 字节、局部颜色表、LZW 码长、数据子块
			var desc [9]byte
			if _, err := io.ReadFull(r, desc[:]); err != nil {
				return bad
			}
			bw.WriteByte(b)
			bw.Write(desc[:])
			if err := copyColorTable(r, bw, desc[8]); err != nil {
				return bad
			}
			lzw, err := r.ReadByte()
			if err != nil {
				return bad
			}
			bw.WriteByte(lzw)
			if err := copySubBlocks(r, bw); err != nil {
				return bad
			}
		default:
			return errors.New("GIF 格式不对")
		}
	}
}

// copyColorTable 按标志字节复制颜色表（最高位表示有没有，低 3 位是大小）。
func copyColorTable(r io.Reader, w io.Writer, flags byte) error {
	if flags&0x80 == 0 {
		return nil
	}
	_, err := io.CopyN(w, r, 3<<((flags&0x07)+1))
	return err
}

// copySubBlocks 复制一串子块，直到长度为 0 的结束块。
func copySubBlocks(r *bufio.Reader, w io.Writer) error {
	for {
		n, err := r.ReadByte()
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte{n}); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if _, err := io.CopyN(w, r, int64(n)); err != nil {
			return err
		}
	}
}

// ---- HEIF ----

// cleanHEIF 就地处理已经存下的 HEIF 文件：Exif 里的 GPS 清零，XMP 用空格盖掉，文件长度不变。
// 同时读出拍摄时间，以及图像尺寸（取 ispe 里最大的那个，网格拼接的照片整张的尺寸最大）。
// 找不到 Exif 的位置时不报错：没有 Exif 也就没有位置；但 Exif 存在却改不了时报错，宁可不收也不漏位置。
func cleanHEIF(path string) (Meta, image.Config, error) {
	meta := Meta{Orientation: 1}
	var cfg image.Config
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return meta, cfg, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return meta, cfg, err
	}
	size := info.Size()
	// 顶层找 meta 盒子；它不大，整个读进来。
	var metaBox []byte
	var metaOff int64
	for off := int64(0); off+8 <= size; {
		var hdr [16]byte
		if _, err := file.ReadAt(hdr[:8], off); err != nil {
			return meta, cfg, err
		}
		n := int64(binary.BigEndian.Uint32(hdr[:4]))
		hlen := int64(8)
		switch n {
		case 1:
			if _, err := file.ReadAt(hdr[8:16], off+8); err != nil {
				return meta, cfg, err
			}
			n, hlen = int64(binary.BigEndian.Uint64(hdr[8:16])), 16
		case 0:
			n = size - off
		}
		if n < hlen || off+n > size {
			return meta, cfg, errors.New("HEIF 文件不完整")
		}
		if string(hdr[4:8]) == "meta" {
			if n > 16<<20 {
				return meta, cfg, errors.New("HEIF 元数据太大")
			}
			metaBox = make([]byte, n-hlen)
			if _, err := file.ReadAt(metaBox, off+hlen); err != nil {
				return meta, cfg, err
			}
			metaOff = off + hlen
			break
		}
		off += n
	}
	if len(metaBox) < 4 {
		return meta, cfg, errors.New("HEIF 文件里没有图像信息")
	}
	children := boxes(metaBox[4:]) // meta 是 FullBox，前 4 字节是版本和标志
	exifItems, xmpItems := map[uint32]bool{}, map[uint32]bool{}
	if iinf, ok := children["iinf"]; ok && len(iinf) >= 6 {
		start := 6
		if iinf[0] != 0 {
			start = 8
		}
		if start <= len(iinf) {
			for _, infe := range boxList(iinf[start:], "infe") {
				id, typ, mime := parseInfe(infe)
				switch {
				case typ == "Exif":
					exifItems[id] = true
				case typ == "mime" && strings.Contains(mime, "rdf+xml"):
					xmpItems[id] = true
				}
			}
		}
	}
	if iprp, ok := children["iprp"]; ok {
		if ipco, ok := boxes(iprp)["ipco"]; ok {
			for _, ispe := range boxList(ipco, "ispe") {
				if len(ispe) >= 12 {
					w, h := int(binary.BigEndian.Uint32(ispe[4:])), int(binary.BigEndian.Uint32(ispe[8:]))
					if w*h > cfg.Width*cfg.Height {
						cfg.Width, cfg.Height = w, h
					}
				}
			}
		}
	}
	if len(exifItems) == 0 && len(xmpItems) == 0 {
		return meta, cfg, nil
	}
	var idat int64 = -1 // idat 盒子数据的位置，construction_method 1 时用
	if b, ok := children["idat"]; ok {
		idat = metaOff + 4 + int64(subOffset(metaBox[4:], b))
	}
	locs, err := parseIloc(children["iloc"])
	if err != nil {
		return meta, cfg, err
	}
	for id := range exifItems {
		ext, ok := locs[id]
		if !ok {
			return meta, cfg, errors.New("HEIF 里的 Exif 找不到")
		}
		data, err := readExtents(file, ext, idat, size)
		if err != nil {
			return meta, cfg, err
		}
		// Exif 项开头是 4 字节偏移，后面跳过这么多字节才是 TIFF 头（通常是 "Exif\0\0"）。
		if len(data) < 4 {
			return meta, cfg, errors.New("HEIF 里的 Exif 不完整")
		}
		skip := 4 + int(binary.BigEndian.Uint32(data))
		if skip > len(data) || !scrubExif(data[skip:], &meta) {
			// 结构看不懂就整段清零。
			clear(data[4:])
		}
		if err := writeExtents(file, ext, idat, data); err != nil {
			return meta, cfg, err
		}
	}
	for id := range xmpItems {
		ext, ok := locs[id]
		if !ok {
			continue
		}
		data, err := readExtents(file, ext, idat, size)
		if err != nil {
			return meta, cfg, err
		}
		for i := range data {
			data[i] = ' '
		}
		if err := writeExtents(file, ext, idat, data); err != nil {
			return meta, cfg, err
		}
	}
	meta.Orientation = 1 // HEIF 的旋转写在 irot/imir 里，解码器会处理；EXIF 里的方向不能再用一次
	return meta, cfg, file.Sync()
}

// subOffset 返回 child 在 parent 里的起始位置；child 必须是 parent 的子切片。
// 按切片的容量算，而不是按内容查找：内容相同的字节可能在前面出现过。
func subOffset(parent, child []byte) int {
	return cap(parent) - cap(child)
}

// boxes 把一串盒子按类型分开（同类型只留第一个）。
func boxes(b []byte) map[string][]byte {
	out := map[string][]byte{}
	walkBoxes(b, func(typ string, body []byte) {
		if _, ok := out[typ]; !ok {
			out[typ] = body
		}
	})
	return out
}

// boxList 返回一串盒子里所有指定类型的盒子内容。
func boxList(b []byte, want string) [][]byte {
	var out [][]byte
	walkBoxes(b, func(typ string, body []byte) {
		if typ == want {
			out = append(out, body)
		}
	})
	return out
}

func walkBoxes(b []byte, fn func(typ string, body []byte)) {
	for off := 0; off+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[off:]))
		hlen := 8
		if n == 1 && off+16 <= len(b) {
			n, hlen = int(binary.BigEndian.Uint64(b[off+8:])), 16
		} else if n == 0 {
			n = len(b) - off
		}
		if n < hlen || off+n > len(b) {
			return
		}
		fn(string(b[off+4:off+8]), b[off+hlen:off+n])
		off += n
	}
}

// parseInfe 读出 infe（版本 2、3）里的项编号、项类型和 mime 类型。
func parseInfe(b []byte) (uint32, string, string) {
	if len(b) < 4 {
		return 0, "", ""
	}
	var id uint32
	p := 4
	switch b[0] {
	case 2:
		if len(b) < p+2 {
			return 0, "", ""
		}
		id, p = uint32(binary.BigEndian.Uint16(b[p:])), p+2
	case 3:
		if len(b) < p+4 {
			return 0, "", ""
		}
		id, p = binary.BigEndian.Uint32(b[p:]), p+4
	default:
		return 0, "", ""
	}
	p += 2 // item_protection_index
	if len(b) < p+4 {
		return 0, "", ""
	}
	typ := string(b[p : p+4])
	p += 4
	var mime string
	if typ == "mime" {
		// 后面是以 0 结尾的 item_name，再是 content_type。
		rest := b[p:]
		if i := bytes.IndexByte(rest, 0); i >= 0 {
			rest = rest[i+1:]
			if j := bytes.IndexByte(rest, 0); j >= 0 {
				mime = string(rest[:j])
			}
		}
	}
	return id, typ, mime
}

// extent 是一项数据在文件（或 idat）里的一段。
type extent struct{ offset, length int64 }

type location struct {
	method  int // 0 在文件里，1 在 idat 里
	extents []extent
}

// parseIloc 读出每一项数据的位置。
func parseIloc(b []byte) (map[uint32]location, error) {
	bad := errors.New("HEIF 的 iloc 格式不对")
	if len(b) < 8 {
		return nil, bad
	}
	version := b[0]
	p := 4
	offSize, lenSize := int(b[p]>>4), int(b[p]&0x0F)
	baseSize, idxSize := int(b[p+1]>>4), int(b[p+1]&0x0F)
	if version == 0 {
		idxSize = 0
	}
	p += 2
	readN := func(n int) (int64, bool) {
		if p+n > len(b) {
			return 0, false
		}
		var v int64
		for i := 0; i < n; i++ {
			v = v<<8 | int64(b[p+i])
		}
		p += n
		return v, true
	}
	idSize := 2
	if version == 2 {
		idSize = 4
	}
	count, ok := readN(idSize)
	if !ok {
		return nil, bad
	}
	out := map[uint32]location{}
	for i := int64(0); i < count; i++ {
		id, ok := readN(idSize)
		if !ok {
			return nil, bad
		}
		loc := location{}
		if version == 1 || version == 2 {
			m, ok := readN(2)
			if !ok {
				return nil, bad
			}
			loc.method = int(m & 0x0F)
		}
		if _, ok := readN(2); !ok { // data_reference_index
			return nil, bad
		}
		base, ok := readN(baseSize)
		if !ok {
			return nil, bad
		}
		n, ok := readN(2)
		if !ok {
			return nil, bad
		}
		for j := int64(0); j < n; j++ {
			if _, ok := readN(idxSize); !ok {
				return nil, bad
			}
			off, ok1 := readN(offSize)
			length, ok2 := readN(lenSize)
			if !ok1 || !ok2 {
				return nil, bad
			}
			loc.extents = append(loc.extents, extent{base + off, length})
		}
		out[uint32(id)] = loc
	}
	return out, nil
}

func extentPos(loc location, e extent, idat int64) (int64, error) {
	switch loc.method {
	case 0:
		return e.offset, nil
	case 1:
		if idat < 0 {
			return 0, errors.New("HEIF 里没有 idat")
		}
		return idat + e.offset, nil
	}
	return 0, errors.New("HEIF 的数据位置格式不支持")
}

func readExtents(file *os.File, loc location, idat, size int64) ([]byte, error) {
	var out []byte
	for _, e := range loc.extents {
		pos, err := extentPos(loc, e, idat)
		if err != nil {
			return nil, err
		}
		length := e.length
		if length == 0 { // 0 表示到文件末尾
			length = size - pos
		}
		if pos < 0 || length < 0 || pos+length > size || length > 16<<20 {
			return nil, errors.New("HEIF 里的元数据位置不对")
		}
		buf := make([]byte, length)
		if _, err := file.ReadAt(buf, pos); err != nil {
			return nil, err
		}
		out = append(out, buf...)
	}
	return out, nil
}

func writeExtents(file *os.File, loc location, idat int64, data []byte) error {
	for _, e := range loc.extents {
		pos, err := extentPos(loc, e, idat)
		if err != nil {
			return err
		}
		n := min(int64(len(data)), e.length)
		if e.length == 0 {
			n = int64(len(data))
		}
		if _, err := file.WriteAt(data[:n], pos); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// decodeHEIF 用 libheif 的 heif-dec 把 HEIF 转成一张临时 JPEG 再解码；Go 没有现成的 HEVC 解码器。
// heif-dec 会按 irot/imir 把图转正。
func decodeHEIF(path string) (image.Image, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "heif-*.jpg")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, heifDecoder, "--quality", "95", path, tmp.Name()).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("HEIF 解码失败：%v %s", err, strings.TrimSpace(string(out)))
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jpeg.Decode(f)
}

// heifDecoder 是 libheif 带的命令行解码器；老版本叫 heif-convert。
var heifDecoder = func() string {
	for _, name := range []string{"heif-dec", "heif-convert"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return "heif-dec"
}()
