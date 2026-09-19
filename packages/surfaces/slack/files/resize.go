package slackfiles

import (
	"bytes"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
)

const (
	// MaxImageEdge is the longest edge allowed before an image is downscaled.
	// Hosted assistants normalize uploads the same way: model token cost depends
	// on image dimensions, not on the uploaded byte size, so bounding the edge is
	// what keeps a large screenshot cheap.
	MaxImageEdge = 1568
	// imageJPEGQuality trades a little fidelity for a much smaller payload.
	imageJPEGQuality = 80
)

// shrinkImageForModel downscales and re-encodes a decodable image so a large
// screenshot cannot inflate the request payload. It returns the input unchanged
// when the image already fits, when it cannot be decoded (for example WebP, which
// the standard library does not decode), or when re-encoding would be larger.
func shrinkImageForModel(data []byte, mime string) ([]byte, string) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mime
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= MaxImageEdge && height <= MaxImageEdge {
		return data, mime
	}
	targetWidth, targetHeight := scaledDimensions(width, height, MaxImageEdge)
	resized := boxDownscale(src, targetWidth, targetHeight)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, resized, &jpeg.Options{Quality: imageJPEGQuality}); err != nil {
		return data, mime
	}
	if encoded.Len() >= len(data) {
		return data, mime
	}
	return encoded.Bytes(), "image/jpeg"
}

func scaledDimensions(width, height, maxEdge int) (int, int) {
	if width <= 0 || height <= 0 {
		return width, height
	}
	if width >= height {
		return maxEdge, max(1, height*maxEdge/width)
	}
	return max(1, width*maxEdge/height), maxEdge
}

// boxDownscale averages each destination pixel's source box. It is a
// dependency-free resampler that is good enough for screenshots; the model
// tolerates its softer output, and avoiding an image-scaling dependency keeps the
// module surface small.
func boxDownscale(src image.Image, width, height int) *image.RGBA {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	bounds := src.Bounds()
	scaleX := float64(bounds.Dx()) / float64(width)
	scaleY := float64(bounds.Dy()) / float64(height)
	for y := 0; y < height; y++ {
		y0 := bounds.Min.Y + int(float64(y)*scaleY)
		y1 := bounds.Min.Y + int(float64(y+1)*scaleY)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > bounds.Max.Y {
			y1 = bounds.Max.Y
		}
		for x := 0; x < width; x++ {
			x0 := bounds.Min.X + int(float64(x)*scaleX)
			x1 := bounds.Min.X + int(float64(x+1)*scaleX)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > bounds.Max.X {
				x1 = bounds.Max.X
			}
			var r, g, b, a, count uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					b += uint64(cb)
					a += uint64(ca)
					count++
				}
			}
			if count == 0 {
				count = 1
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(r / count >> 8),
				G: uint8(g / count >> 8),
				B: uint8(b / count >> 8),
				A: uint8(a / count >> 8),
			})
		}
	}
	return dst
}
