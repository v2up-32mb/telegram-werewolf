package telegram

import (
	"bytes"
	"errors"
	"image/color"
	"image/png"
	"testing"
)

// pngMagic 是 PNG 文件头。
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func TestInviteQRProducesDecodablePNG(t *testing.T) {
	link := "https://t.me/werewolf_bot?start=ROOM-ABC123"
	got, err := InviteQR(link, 512)
	if err != nil {
		t.Fatalf("InviteQR: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("InviteQR returned empty bytes")
	}
	if !bytes.HasPrefix(got, pngMagic) {
		t.Fatal("output does not start with PNG magic（输出不是 PNG）")
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("png.Decode: %v（输出必须是可解码 PNG）", err)
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("dimensions = %v, want positive", b)
	}
	if b.Dx() != b.Dy() {
		t.Fatalf("dimensions = %v, want square QR", b)
	}
	if b.Dx() > 512 {
		t.Fatalf("dimensions = %v, want <= requested size 512", b)
	}
	// 内容非空：黑白两种像素都应存在。
	var dark, light int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			g := color.GrayModel.Convert(img.At(x, y)).(color.Gray)
			if g.Y < 128 {
				dark++
			} else {
				light++
			}
		}
	}
	if dark == 0 {
		t.Fatal("no dark pixels（二维码内容为空）")
	}
	if light == 0 {
		t.Fatal("no light pixels（二维码背景为空）")
	}
}

func TestInviteQRRejectsEmptyDeepLink(t *testing.T) {
	for _, link := range []string{"", "   ", "\t\n  "} {
		if _, err := InviteQR(link, 512); !errors.Is(err, ErrEmptyDeepLink) {
			t.Fatalf("InviteQR(%q) error = %v, want ErrEmptyDeepLink", link, err)
		}
	}
}

func TestInviteQRRejectsNonPositiveSize(t *testing.T) {
	for _, size := range []int{0, -1} {
		if _, err := InviteQR("https://t.me/werewolf_bot?start=ROOM-1", size); err == nil {
			t.Fatalf("InviteQR(size=%d) = nil error, want error", size)
		}
	}
}

func TestInviteQRDeterministicOutput(t *testing.T) {
	link := "https://t.me/werewolf_bot?start=ROOM-DETERMINISTIC"
	first, err := InviteQR(link, 256)
	if err != nil {
		t.Fatalf("InviteQR first: %v", err)
	}
	second, err := InviteQR(link, 256)
	if err != nil {
		t.Fatalf("InviteQR second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("同一输入输出不一致（应为确定性渲染）")
	}
	if _, err := png.Decode(bytes.NewReader(second)); err != nil {
		t.Fatalf("second output decode: %v", err)
	}
}
