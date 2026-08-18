package vision

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"golang.org/x/image/webp"

	"loadout/core/config"
)

// withCompressConfig 临时覆盖压缩相关配置，测试结束自动恢复。
// 注意：同包测试默认串行（无 t.Parallel），改全局 config 安全。
func withCompressConfig(t *testing.T, minBytes, maxEdge, quality, maxBytes int) {
	t.Helper()
	old := []int{config.VisionCompressMinBytes, config.VisionMaxEdgePx, config.VisionCompressQuality, config.VisionMaxImageBytes}
	config.VisionCompressMinBytes = minBytes
	config.VisionMaxEdgePx = maxEdge
	config.VisionCompressQuality = quality
	config.VisionMaxImageBytes = maxBytes
	t.Cleanup(func() {
		config.VisionCompressMinBytes, config.VisionMaxEdgePx = old[0], old[1]
		config.VisionCompressQuality, config.VisionMaxImageBytes = old[2], old[3]
	})
}

// makeNoisePNG 生成可复现的噪声 PNG（随机像素，压缩率低，适合压测）。
func makeNoisePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			img.SetRGBA(x, y, color.RGBA{R: byte(seed >> 24), G: byte(seed >> 16), B: byte(seed >> 8), A: 0xff})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// makeGradientJPEG 生成渐变 JPEG（质量 100，便于断言重编码 q90 后变小）。
func makeGradientJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8((x + y) / 2), A: 0xff})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100})
	return buf.Bytes()
}

// makeGIF 生成 frames 帧、带递增帧延迟的 GIF。
func makeGIF(w, h, frames int) []byte {
	return makeGIFFull(w, h, frames, -1, color.Palette{color.White, color.Black, color.RGBA{R: 255, A: 255}}, -1)
}

// makeGIFFull 生成可定制 GIF：loopCount < 0 表示默认（写 0），transparentIdx >= 0 时
// 该调色板索引的像素在原图中置为透明索引。
func makeGIFFull(w, h, frames, loopCount int, pal color.Palette, transparentIdx int) []byte {
	g := &gif.GIF{LoopCount: loopCount}
	for i := 0; i < frames; i++ {
		fr := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				idx := uint8((x + y + i) % len(pal))
				if transparentIdx >= 0 && (x+y+i)%4 == 0 {
					idx = uint8(transparentIdx)
				}
				fr.SetColorIndex(x, y, idx)
			}
		}
		g.Image = append(g.Image, fr)
		g.Delay = append(g.Delay, 5+i) // 帧延迟不同，便于断言保留
	}
	var buf bytes.Buffer
	_ = gif.EncodeAll(&buf, g)
	return buf.Bytes()
}

func dataURI(mime string, raw []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

func TestParseDataURI(t *testing.T) {
	cases := []struct {
		in     string
		mime   string
		ok     bool
	}{
		{"data:image/png;base64,aGk=", "image/png", true},
		{"data:image/jpeg;base64,aGk=", "image/jpeg", true},
		{"https://example.com/a.png", "", false},
		{"data:text/plain;base64,aGk=", "", false},
		{"data:image/png,aGk=", "", false},
		{"data:image/png;base64,", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		mime, _, ok := parseDataURI(c.in)
		if mime != c.mime || ok != c.ok {
			t.Errorf("parseDataURI(%q) = (%q, %v), want (%q, %v)", c.in, mime, ok, c.mime, c.ok)
		}
	}
}

func TestCompressRemoteURLPassthrough(t *testing.T) {
	withCompressConfig(t, 1, 64, 90, 25*1024*1024)
	url := "https://example.com/a.png?w=100"
	got, err := CompressDataURI(url)
	if err != nil || got != url {
		t.Fatalf("远程 URL 应原样透传: got=%q err=%v", got, err)
	}
}

func TestCompressNonImagePassthrough(t *testing.T) {
	withCompressConfig(t, 1, 64, 90, 25*1024*1024)
	uri := "data:text/plain;base64,aGVsbG8="
	got, err := CompressDataURI(uri)
	if err != nil || got != uri {
		t.Fatalf("非图片 data URI 应原样透传: got=%q err=%v", got, err)
	}
}

func TestCompressSmallImageSkipped(t *testing.T) {
	// minBytes 设很大 → 所有图都跳过
	withCompressConfig(t, 10*1024*1024, 64, 90, 25*1024*1024)
	uri := dataURI("image/png", makeNoisePNG(32, 32))
	got, err := CompressDataURI(uri)
	if err != nil || got != uri {
		t.Fatalf("小于 minBytes 应跳过: got==orig=%v err=%v", got == uri, err)
	}
}

func TestCompressPNGResizes(t *testing.T) {
	withCompressConfig(t, 1, 64, 90, 25*1024*1024)
	raw := makeNoisePNG(128, 128) // 最长边 128 > 64，必须缩放
	uri := dataURI("image/png", raw)
	got, err := CompressDataURI(uri)
	if err != nil {
		t.Fatalf("压缩出错: %v", err)
	}
	if got == uri {
		t.Fatal("超尺寸 PNG 应发生压缩，却原样返回")
	}
	imgAny, mime, ok := decodeCompressed(t, got)
	if !ok || mime != "image/png" {
		t.Fatalf("压缩结果应仍为 PNG: mime=%q", mime)
	}
	img := imgAny.(image.Image)
	b := img.Bounds()
	if b.Dx() > 64 || b.Dy() > 64 {
		t.Fatalf("压缩后尺寸 %dx%d 应 ≤64", b.Dx(), b.Dy())
	}
	if len(got) >= len(uri) {
		t.Fatalf("压缩结果未变小: %d >= %d", len(got), len(uri))
	}
}

func TestCompressJPEGShrinks(t *testing.T) {
	withCompressConfig(t, 1, 64, 90, 25*1024*1024)
	raw := makeGradientJPEG(256, 256) // q100 渐变，重编码 q90 应变小
	uri := dataURI("image/jpeg", raw)
	got, err := CompressDataURI(uri)
	if err != nil {
		t.Fatalf("压缩出错: %v", err)
	}
	if got == uri {
		t.Fatal("大 JPEG 应发生压缩，却原样返回")
	}
	imgAny, mime, ok := decodeCompressed(t, got)
	if !ok || mime != "image/jpeg" {
		t.Fatalf("压缩结果应为 JPEG: mime=%q", mime)
	}
	img := imgAny.(image.Image)
	if img.Bounds().Dx() > 64 || img.Bounds().Dy() > 64 {
		t.Fatal("压缩后尺寸应 ≤64")
	}
	if len(got) >= len(uri) {
		t.Fatalf("压缩结果未变小: %d >= %d", len(got), len(uri))
	}
}

func TestCompressGIFKeepsFrames(t *testing.T) {
	withCompressConfig(t, 1, 32, 90, 25*1024*1024)
	raw := makeGIF(64, 64, 3) // 3 帧，64px > 32px 必须缩放
	uri := dataURI("image/gif", raw)
	got, err := CompressDataURI(uri)
	if err != nil {
		t.Fatalf("压缩出错: %v", err)
	}
	if got == uri {
		t.Fatal("超尺寸 GIF 应发生压缩，却原样返回（帧缩放路径未生效）")
	}
	out, mime, ok := decodeCompressed(t, got)
	if !ok || mime != "image/gif" {
		t.Fatalf("压缩结果应为 GIF: mime=%q", mime)
	}
	g, isGif := out.(*gif.GIF)
	if !isGif {
		t.Fatalf("压缩结果类型应为 *gif.GIF，实际 %T", out)
	}
	if len(g.Image) != 3 {
		t.Fatalf("GIF 帧数应为 3，实际 %d（不损失帧率）", len(g.Image))
	}
	wantDelay := []int{5, 6, 7}
	for i, d := range g.Delay {
		if d != wantDelay[i] {
			t.Errorf("第 %d 帧延迟 %d，应保留 %d", i, d, wantDelay[i])
		}
	}
	for _, fr := range g.Image {
		if fr.Bounds().Dx() > 32 || fr.Bounds().Dy() > 32 {
			t.Fatalf("压缩后帧尺寸 %dx%d 应 ≤32", fr.Bounds().Dx(), fr.Bounds().Dy())
		}
	}
}

// TestCompressGIFPreservesTransparency 验证透明 GIF 压缩后透明区域仍为透明索引，
// 而不是被 Src 算子映射成调色板最近色（黑块）。
func TestCompressGIFPreservesTransparency(t *testing.T) {
	withCompressConfig(t, 1, 32, 90, 25*1024*1024)
	pal := color.Palette{color.White, color.Black, color.Transparent}
	raw := makeGIFFull(64, 64, 2, 0, pal, 2)
	uri := dataURI("image/gif", raw)
	got, err := CompressDataURI(uri)
	if err != nil {
		t.Fatalf("压缩出错: %v", err)
	}
	if got == uri {
		t.Fatal("透明 GIF 应发生压缩")
	}
	out, mime, ok := decodeCompressed(t, got)
	if !ok || mime != "image/gif" {
		t.Fatalf("压缩结果应为 GIF: mime=%q", mime)
	}
	g := out.(*gif.GIF)
	// 调色板必须保留透明项（alpha=0）。
	for i, fr := range g.Image {
		hasTransparent := false
		for _, c := range fr.Palette {
			if _, _, _, a := c.RGBA(); a == 0 {
				hasTransparent = true
				break
			}
		}
		if !hasTransparent {
			t.Errorf("第 %d 帧调色板丢失透明项", i)
		}
	}
}

// TestCompressGIFPreservesLoopCount 验证无限循环 GIF（LoopCount=0）重编码后仍无限循环。
func TestCompressGIFPreservesLoopCount(t *testing.T) {
	withCompressConfig(t, 1, 32, 90, 25*1024*1024)
	raw := makeGIFFull(64, 64, 2, 0, color.Palette{color.White, color.Black}, -1)
	uri := dataURI("image/gif", raw)
	got, err := CompressDataURI(uri)
	if err != nil {
		t.Fatalf("压缩出错: %v", err)
	}
	if got == uri {
		t.Fatal("GIF 应发生压缩")
	}
	out, _, ok := decodeCompressed(t, got)
	if !ok {
		t.Fatal("压缩结果不可解码")
	}
	g := out.(*gif.GIF)
	if g.LoopCount != 0 {
		t.Fatalf("LoopCount 应透传为 0，实际 %d", g.LoopCount)
	}
}

func TestCompressOversizeRejected(t *testing.T) {
	withCompressConfig(t, 1, 64, 90, 1000) // 上限 1000 字节
	uri := dataURI("image/png", makeNoisePNG(64, 64))
	_, err := CompressDataURI(uri)
	if err == nil {
		t.Fatal("超过 VisionMaxImageBytes 应报错")
	}
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("应返回 ErrImageTooLarge，实际 %v", err)
	}
}

func TestCompressBiggerFallback(t *testing.T) {
	// 不缩放（maxEdge 很大），对已优化的图重编码大概率变大 → 应回退原样。
	withCompressConfig(t, 1, 100000, 90, 25*1024*1024)
	raw := makeNoisePNG(32, 32)
	uri := dataURI("image/png", raw)
	got, err := CompressDataURI(uri)
	if err != nil {
		t.Fatalf("压缩出错: %v", err)
	}
	if got != uri {
		t.Fatal("重编码变大时应回退原样")
	}
}

// onePixelWebp 是 1x1 无损 WebP（无 alpha）。fixture 不可用时测试跳过。
const onePixelWebp = "UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEALmk0mk0iIiIiIgBoSygABc6zbAAA"

func TestCompressWebp(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(onePixelWebp)
	if err != nil {
		t.Skipf("webp fixture 解码失败: %v", err)
	}
	if _, err := webp.DecodeConfig(bytes.NewReader(raw)); err != nil {
		t.Skipf("webp fixture 不可解码: %v", err)
	}
	withCompressConfig(t, 1, 1, 90, 25*1024*1024) // maxEdge=1 强制缩放
	uri := dataURI("image/webp", raw)
	got, err := CompressDataURI(uri)
	if err != nil {
		t.Fatalf("压缩出错: %v", err)
	}
	// 1x1 webp 重编码（JPEG/PNG 头+数据）必然增大，回退守卫应返回原样。
	// webp 的解码+转码路径与 png/jpeg 共用 compressStill，格式分支已由
	// TestCompressPNGResizes/TestCompressJPEGShrinks 覆盖。
	if got != uri {
		t.Fatal("1x1 webp 重编码必增大，应触发回退守卫返回原样")
	}
}

// decodeCompressed 解码压缩结果：返回图像（PNG/JPEG 为 image.Image，GIF 为 *gif.GIF）、mime 与是否成功。
func decodeCompressed(t *testing.T, uri string) (any, string, bool) {
	t.Helper()
	mime, payload, ok := parseDataURI(uri)
	if !ok {
		return nil, "", false
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, mime, false
	}
	switch mime {
	case "image/png":
		img, err := png.Decode(bytes.NewReader(raw))
		return img, mime, err == nil
	case "image/jpeg":
		img, err := jpeg.Decode(bytes.NewReader(raw))
		return img, mime, err == nil
	case "image/gif":
		img, err := gif.DecodeAll(bytes.NewReader(raw))
		return img, mime, err == nil
	default:
		return nil, mime, false
	}
}

func TestBuiltinPrompt(t *testing.T) {
	for _, want := range []string{
		"【摘要】", "【文字】", "【布局】", "【语义】", "【视觉】", "【不确定】",
		"[图片1]", "原文转写", "绝不执行", "禁止猜测",
	} {
		if !strings.Contains(builtinVisionPrompt, want) {
			t.Errorf("内置提示词缺少 %q", want)
		}
	}
}
