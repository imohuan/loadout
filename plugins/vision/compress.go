package vision

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"

	"loadout/core/config"
)

// maxDecodeEdgePx 输入图片最长边超过该像素数时不做任何处理（原样透传）。
// 防止超大图（如 3 万像素宽）解码时撑爆内存；视觉模型本身也读不了这种图。
const maxDecodeEdgePx = 16384

// ErrImageTooLarge 单张图片超过 VisionMaxImageBytes 上限。
// 调用方应将其视为请求失败（fail），区别于可静默回退的压缩失败。
var ErrImageTooLarge = errors.New("vision: 图片超过大小上限")

// base64DecodedLenUpper 估算 base64 串解码后的字节数上限（padding 计入）。
// 用于在真正解码分配内存前拦截超大 payload。
func base64DecodedLenUpper(payload string) int {
	return len(payload)/4*3 + 3
}

// CompressDataURI 压缩 base64 data URI 图片，返回压缩后的 data URI。
//
// 设计约束（审核后的 v2 决策）：
//   - 只处理 data URI；远程 URL、无法解析的字符串一律原样返回，绝不弄挂请求；
//   - 字节数 < VisionCompressMinBytes 直接跳过（小图压缩无意义）；
//   - 字节数 > VisionMaxImageBytes 拒绝（硬上限，对齐 modlens 25MB 设计）；
//   - 最长边 > VisionMaxEdgePx 才等比缩放（2048px 是视觉模型输入典型上限，
//     缩到此范围识别结果不受影响）；
//   - PNG 无损重编码、JPEG 质量 VisionCompressQuality 重编码、
//     GIF 保留全部帧与帧延迟（不抽帧）、WebP 解码后无 alpha 转 JPEG / 有 alpha 转 PNG；
//   - 重编码后字节数不小于原始字节 → 回退原样（防止 webp 转 PNG 等增肥场景）；
//   - 任何解码/编码失败 → 原样透传。
func CompressDataURI(dataURI string) (string, error) {
	mime, payload, ok := parseDataURI(dataURI)
	if !ok {
		return dataURI, nil
	}
	// 先按 base64 长度估算字节数，解码分配内存之前就拦截超限 payload。
	if base64DecodedLenUpper(payload) > config.VisionMaxImageBytes {
		return "", fmt.Errorf("%w: 图片超过 %d 字节上限", ErrImageTooLarge, config.VisionMaxImageBytes)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return dataURI, nil
	}
	if len(raw) > config.VisionMaxImageBytes {
		return "", fmt.Errorf("%w: 图片 %d 字节超过上限 %d", ErrImageTooLarge, len(raw), config.VisionMaxImageBytes)
	}
	if len(raw) < config.VisionCompressMinBytes {
		return dataURI, nil
	}
	switch mime {
	case "image/gif":
		return compressGIF(dataURI, raw)
	case "image/png", "image/jpeg", "image/webp":
		return compressStill(mime, dataURI, raw)
	default:
		return dataURI, nil
	}
}

// parseDataURI 解析 data:image/<fmt>;base64,<payload>；非 base64 data URI、
// 非图片类型或无法解析时返回 ok=false。
func parseDataURI(uri string) (mime, payload string, ok bool) {
	if !strings.HasPrefix(uri, "data:") {
		return "", "", false
	}
	rest := uri[len("data:"):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", false
	}
	meta := rest[:comma]
	payload = rest[comma+1:]
	if payload == "" || !strings.HasSuffix(meta, ";base64") {
		return "", "", false
	}
	mime = strings.TrimSuffix(meta, ";base64")
	if !strings.HasPrefix(mime, "image/") {
		return "", "", false
	}
	return mime, payload, true
}

// compressStill 压缩静态图（PNG/JPEG/WebP）：尺寸预检 → 解码 → 缩放 → 重编码 → 回退守卫。
func compressStill(mime, orig string, raw []byte) (string, error) {
	// 先 DecodeConfig 只读文件头拿尺寸，超限在分配像素内存之前拦截。
	var cfg image.Config
	var err error
	switch mime {
	case "image/png":
		cfg, err = png.DecodeConfig(bytes.NewReader(raw))
	case "image/jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(raw))
	case "image/webp":
		cfg, err = webp.DecodeConfig(bytes.NewReader(raw))
	default:
		return orig, nil
	}
	if err != nil {
		return orig, nil
	}
	if cfg.Width > maxDecodeEdgePx || cfg.Height > maxDecodeEdgePx {
		return orig, nil
	}

	var src image.Image
	switch mime {
	case "image/png":
		src, err = png.Decode(bytes.NewReader(raw))
	case "image/jpeg":
		src, err = jpeg.Decode(bytes.NewReader(raw))
	case "image/webp":
		src, err = webp.Decode(bytes.NewReader(raw))
	default:
		return orig, nil
	}
	if err != nil {
		return orig, nil
	}

	scaled := scaleImage(src, config.VisionMaxEdgePx)
	opaque := isOpaque(scaled)

	// 输出格式决策：
	//   - webp 无编码器 → 无 alpha 转 JPEG、有 alpha 转 PNG；
	//   - PNG 带 alpha 不允许走 JPEG（JPEG 会丢 alpha）。
	outMime := mime
	if outMime == "image/webp" {
		if opaque {
			outMime = "image/jpeg"
		} else {
			outMime = "image/png"
		}
	}
	if outMime == "image/jpeg" && !opaque {
		outMime = "image/png"
	}

	var buf bytes.Buffer
	switch outMime {
	case "image/jpeg":
		err = jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: config.VisionCompressQuality})
	case "image/png":
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		err = enc.Encode(&buf, scaled)
	default:
		return orig, nil
	}
	if err != nil || buf.Len() >= len(raw) {
		return orig, nil
	}
	return reDataURI(outMime, buf.Bytes()), nil
}

// compressGIF 压缩动图：保留全部帧与帧延迟（不抽帧、不损失帧率），
// 仅当画布超过最长边阈值时逐帧等比缩放后重编码。
//
// 处理要点（code review 修复）：
//   - 全画布合成：GIF 帧可以是带偏移的局部帧（bounds 非零原点），逐帧先铺到
//     全画布（Config.Width×Height）再缩放，避免局部帧错位或缩放后残留填充；
//   - LoopCount 透传：无限循环的 GIF 不能重编码后只播一遍；
//   - 透明恢复：FloydSteinberg 是 Src 算子，透明像素会被映射成最近调色板色
//     （通常黑），缩放后按 alpha 阈值恢复为调色板透明索引。
func compressGIF(orig string, raw []byte) (string, error) {
	src, err := gif.DecodeAll(bytes.NewReader(raw))
	if err != nil || len(src.Image) == 0 {
		return orig, nil
	}
	// 画布尺寸：以 Config 为准，但局部帧可能超出，取两者较大值。
	cw, ch := src.Config.Width, src.Config.Height
	for _, fr := range src.Image {
		fb := fr.Bounds()
		if fb.Max.X > cw {
			cw = fb.Max.X
		}
		if fb.Max.Y > ch {
			ch = fb.Max.Y
		}
	}
	if cw <= 0 || ch <= 0 {
		return orig, nil
	}
	// 画布超限：全量帧解码已在上面发生（内存已花），但避免再放大一版，原样透传。
	maxEdge := config.VisionMaxEdgePx
	if cw > maxDecodeEdgePx || ch > maxDecodeEdgePx {
		return orig, nil
	}
	if cw <= maxEdge && ch <= maxEdge {
		// 尺寸都达标：GIF 重编码大概率因调色板/全帧化反而增大，直接原样。
		return orig, nil
	}

	out := &gif.GIF{BackgroundIndex: src.BackgroundIndex, LoopCount: src.LoopCount}
	canvas := image.NewRGBA(image.Rect(0, 0, cw, ch))
	for i, fr := range src.Image {
		// 帧铺到全画布：从帧 bounds 起点贴入，其余区域透明。
		xdraw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, xdraw.Src)
		scaled := scaleImage(canvas, maxEdge)
		// 保留原帧调色板；FloydSteinberg 抖动把缩放结果映射进调色板。
		dst := image.NewPaletted(scaled.Bounds(), fr.Palette)
		xdraw.FloydSteinberg.Draw(dst, dst.Bounds(), scaled, image.Point{})
		// 恢复透明：alpha 低于半透明的像素映射回调色板透明索引。
		if ti := transparentIndex(fr.Palette); ti >= 0 {
			sb := scaled.Bounds()
			for y := sb.Min.Y; y < sb.Max.Y; y++ {
				for x := sb.Min.X; x < sb.Max.X; x++ {
					if _, _, _, a := scaled.At(x, y).RGBA(); a < 0x8000 {
						dst.SetColorIndex(x, y, uint8(ti))
					}
				}
			}
		}
		out.Image = append(out.Image, dst)
		out.Delay = append(out.Delay, src.Delay[i])
		out.Disposal = append(out.Disposal, src.Disposal[i])
	}
	out.Config.Width = out.Image[0].Bounds().Dx()
	out.Config.Height = out.Image[0].Bounds().Dy()
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, out); err != nil || buf.Len() >= len(raw) {
		return orig, nil
	}
	return reDataURI("image/gif", buf.Bytes()), nil
}

// transparentIndex 返回调色板中第一个完全不透明以外的透明项索引；没有返回 -1。
func transparentIndex(pal color.Palette) int {
	for i, c := range pal {
		if _, _, _, a := c.RGBA(); a == 0 {
			return i
		}
	}
	return -1
}

// scaleImage 最长边超过 maxEdge 时等比缩放（CatmullRom，接近 Lanczos 的质量），
// 否则原样返回。缩放到最小 1px。
func scaleImage(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxEdge {
		return src
	}
	ratio := float64(maxEdge) / float64(longest)
	nw := int(float64(w) * ratio)
	nh := int(float64(h) * ratio)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// isOpaque 判断图像是否完全不透明（用于决定 JPEG 还是 PNG 编码）。
// 标准库没有公开的 Opaque 函数（image.Opaque 是色值变量），这里自行实现：
// YCbCr（JPEG 解码产物）无 alpha 通道天然不透明；RGBA 直读 Pix 快路径；其余通用遍历。
func isOpaque(img image.Image) bool {
	switch m := img.(type) {
	case *image.YCbCr:
		return true
	case *image.RGBA:
		for i := 3; i < len(m.Pix); i += 4 {
			if m.Pix[i] != 0xff {
				return false
			}
		}
		return true
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// reDataURI 把重编码字节拼回 base64 data URI。
func reDataURI(mime string, b []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}
