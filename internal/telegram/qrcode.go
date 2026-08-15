package telegram

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"

	"github.com/yeqown/go-qrcode/v2"
)

// ErrEmptyDeepLink 表示邀请 deep link 为空，无法编码二维码。
var ErrEmptyDeepLink = errors.New("telegram: invite deep link is empty")

// qrModulePadding 是二维码四周的静区格数（QR 规范要求至少 4 格）。
const qrModulePadding = 4

// InviteQR 把邀请 deep link 渲染为内存 PNG 字节。
//
// size 是输出图片的边长上限（像素）；实际边长按「模块格数 × 整数倍率」
// 取不超过 size 的最大值，保证二维码不被拉伸模糊。空/纯空白链接返回
// ErrEmptyDeepLink，非正 size 返回错误。二维码内容为 deep link 原文，
// 供上层（Task 25 邀请消息）直接作为图片上传使用，不落盘。
func InviteQR(deepLink string, size int) ([]byte, error) {
	if strings.TrimSpace(deepLink) == "" {
		return nil, ErrEmptyDeepLink
	}
	if size <= 0 {
		return nil, fmt.Errorf("telegram: invite QR size must be positive, got %d", size)
	}
	qr, err := qrcode.NewWith(deepLink, qrcode.WithErrorCorrectionLevel(qrcode.ErrorCorrectionQuart))
	if err != nil {
		return nil, fmt.Errorf("telegram: encode invite QR: %w", err)
	}
	var buf bytes.Buffer
	dim := qr.Dimension()
	if dim <= 0 {
		return nil, errors.New("telegram: invite QR matrix is empty")
	}
	modules := dim + 2*qrModulePadding
	scale := size / modules
	if scale < 1 {
		scale = 1
	}
	w := &pngMatrixWriter{buf: &buf, scale: scale, padding: qrModulePadding}
	if err := qr.Save(w); err != nil {
		return nil, fmt.Errorf("telegram: render invite QR: %w", err)
	}
	return buf.Bytes(), nil
}

// pngMatrixWriter 是 yeqown 二维码矩阵的最小 PNG 渲染器。
//
// 依赖模块内建 Writer 契约（qrcode.Writer），不引入额外 writer 依赖；
// 背景白色、模块黑色，输出带静区的灰度 PNG。
type pngMatrixWriter struct {
	buf     *bytes.Buffer
	scale   int
	padding int
}

// Write 实现 qrcode.Writer，把矩阵绘制为灰度 PNG。
func (w *pngMatrixWriter) Write(mat qrcode.Matrix) error {
	bits := mat.Bitmap()
	dim := len(bits)
	if dim == 0 {
		return errors.New("telegram: QR matrix has no rows")
	}
	modules := dim + 2*w.padding
	edge := modules * w.scale
	img := image.NewGray(image.Rect(0, 0, edge, edge))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{Y: 0xff}), image.Point{}, draw.Src)
	black := color.Gray{Y: 0x00}
	for yy, row := range bits {
		if len(row) != dim {
			return fmt.Errorf("telegram: QR matrix row %d width %d != %d", yy, len(row), dim)
		}
		for xx, on := range row {
			if !on {
				continue
			}
			x0 := (xx + w.padding) * w.scale
			y0 := (yy + w.padding) * w.scale
			for dy := 0; dy < w.scale; dy++ {
				for dx := 0; dx < w.scale; dx++ {
					img.SetGray(x0+dx, y0+dy, black)
				}
			}
		}
	}
	return png.Encode(w.buf, img)
}

// Close 实现 qrcode.Writer（内存目标无需关闭资源）。
func (w *pngMatrixWriter) Close() error { return nil }
