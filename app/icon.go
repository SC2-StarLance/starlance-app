package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"runtime"

	"golang.org/x/image/draw"
)

//go:embed assets/sl-logo.png
var iconSrc []byte // 300x300 square StarLance logo (same artwork as the website favicon)

// scaleLogo decodes the embedded logo, crops its transparent margins, and
// downscales it to fit size×size using Catmull-Rom (the highest-quality
// pure-Go scaler). The crop first removes the logo's generous whitespace so
// the mark fills the tray square instead of appearing as a small sliver; the
// fit preserves the aspect ratio (wide marks keep side margins). If decoding
// ever fails the tray gets a transparent dot rather than breaking rendering.
func scaleLogo(size int) *image.NRGBA {
	img, err := png.Decode(bytes.NewReader(iconSrc))
	if err != nil {
		return image.NewNRGBA(image.Rect(0, 0, size, size))
	}
	src, ok := img.(*image.NRGBA)
	if !ok {
		b := img.Bounds()
		src = image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	}
	return fitInSquare(src, size)
}

// fitInSquare crops an image to its solid content (alpha >= 128, cutting the
// translucent halo around the mark) and renders it centered at the largest
// scale that fits within a size×size square.
func fitInSquare(src *image.NRGBA, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))

	crop := alphaBounds(src, 128)
	if crop == nil {
		return dst // fully transparent: nothing to show
	}
	cropped := subImage(src, crop)

	// Scale the cropped mark to fit inside the square, keeping the aspect.
	scale := float64(size) / float64(max(crop.Dx(), crop.Dy()))
	rw := int(float64(crop.Dx()) * scale)
	rh := int(float64(crop.Dy()) * scale)
	dx := (size - rw) / 2
	dy := (size - rh) / 2
	if rw < 1 || rh < 1 {
		return dst
	}
	draw.CatmullRom.Scale(dst, image.Rect(dx, dy, dx+rw, dy+rh), cropped, cropped.Bounds(), draw.Over, nil)
	return dst
}

// alphaBounds returns the bounding box of pixels with alpha >= minAlpha, or
// nil when no pixel meets the threshold.
func alphaBounds(img *image.NRGBA, minAlpha uint8) *image.Rectangle {
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if a := img.NRGBAAt(x, y).A; a < minAlpha {
				continue
			}
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if maxX < minX || maxY < minY {
		return nil
	}
	return &image.Rectangle{Min: image.Point{minX, minY}, Max: image.Point{maxX + 1, maxY + 1}}
}

// subImage returns a view of img restricted to r (sharing pixel storage).
func subImage(img *image.NRGBA, r *image.Rectangle) *image.NRGBA {
	return img.SubImage(*r).(*image.NRGBA)
}

// iconBytes is the 48x48 rendered logo passed to systray.SetIcon. Windows
// gets a proper ICO container (LoadImage rejects raw PNG data); every other
// platform gets the PNG directly.
var iconBytes = func() []byte {
	png := encodePNG(48)
	if runtime.GOOS == "windows" {
		return icoFromPNG(png)
	}
	return png
}()

// templatePNG returns a macOS template-image twin of a PNG: every pixel is
// painted white while keeping its alpha, and the system tints the result to
// match the menu bar. macOS ignores the color channels of a template image,
// so feeding it the colored artwork would render as a solid block. Non-PNG
// input (the Windows ICO) or a decode failure falls back to the original.
func templatePNG(src []byte) []byte {
	if len(src) == 0 {
		return src
	}
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return src
	}
	b := img.Bounds()
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			out.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: uint8(a >> 8)})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}

func encodePNG(size int) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, scaleLogo(size))
	return buf.Bytes()
}

// icoFromPNG wraps a square PNG in a minimal single-image ICO container
// (Windows Vista and later accept PNG-compressed ICO entries).
func icoFromPNG(png []byte) []byte {
	const headerLen = 6 + 16
	var side = 48 // the PNG passed in must be square

	out := make([]byte, 0, headerLen+len(png))
	out = binary.LittleEndian.AppendUint16(out, 0) // reserved
	out = binary.LittleEndian.AppendUint16(out, 1) // type: icon
	out = binary.LittleEndian.AppendUint16(out, 1) // image count
	out = append(out, byte(side%256))              // width (0 means 256)
	out = append(out, byte(side%256))              // height
	out = append(out, 0)                           // palette size
	out = append(out, 0)                           // reserved
	out = binary.LittleEndian.AppendUint16(out, 1) // color planes
	out = binary.LittleEndian.AppendUint16(out, 32)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(png)))
	out = binary.LittleEndian.AppendUint32(out, headerLen)
	return append(out, png...)
}
