package render

import "image"

// normStats carries the validation numbers computed during normalization, so
// the pipeline touches the image exactly once before the preset runs.
type normStats struct {
	meanLuma float64 // 0-255 mean luminance of the normalized image
	uniform  bool    // every pixel identical → dead sensor
}

// normalize bilinearly rescales img to exactly dstW wide (§7) and computes
// dark/dead-sensor stats in the same pass. EXIF is dropped by construction:
// only decoded pixels survive, never the source container.
func normalize(img image.Image, dstW int) (*image.RGBA, normStats, error) {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw < 1 || sh < 1 || dstW < 1 {
		return nil, normStats{}, Reject("dims")
	}
	dw := dstW
	dh := sh * dstW / sw
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	// Edge-clamped bilinear sampling.
	sxScale := float64(sw) / float64(dw)
	syScale := float64(sh) / float64(dh)

	var lumaSum uint64
	firstR, firstG, firstB := -1, -1, -1
	uniform := true
	n := 0

	for y := 0; y < dh; y++ {
		y0, wy := sampleCoord(float64(y)+0.5, syScale, sh)
		y1 := y0 + 1
		if y1 > sh-1 {
			y1 = sh - 1
		}
		for x := 0; x < dw; x++ {
			x0, wx := sampleCoord(float64(x)+0.5, sxScale, sw)
			x1 := x0 + 1
			if x1 > sw-1 {
				x1 = sw - 1
			}

			r00, g00, b00 := rgbAt(img, b.Min.X+x0, b.Min.Y+y0)
			r10, g10, b10 := rgbAt(img, b.Min.X+x1, b.Min.Y+y0)
			r01, g01, b01 := rgbAt(img, b.Min.X+x0, b.Min.Y+y1)
			r11, g11, b11 := rgbAt(img, b.Min.X+x1, b.Min.Y+y1)

			r := lerp(lerp(float64(r00), float64(r10), wx), lerp(float64(r01), float64(r11), wx), wy)
			g := lerp(lerp(float64(g00), float64(g10), wx), lerp(float64(g01), float64(g11), wx), wy)
			bl := lerp(lerp(float64(b00), float64(b10), wx), lerp(float64(b01), float64(b11), wx), wy)

			ri, gi, bi := int(r+0.5), int(g+0.5), int(bl+0.5)
			if ri > 255 {
				ri = 255
			}
			if gi > 255 {
				gi = 255
			}
			if bi > 255 {
				bi = 255
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o] = uint8(ri)
			dst.Pix[o+1] = uint8(gi)
			dst.Pix[o+2] = uint8(bi)
			dst.Pix[o+3] = 0xff

			if firstR < 0 {
				firstR, firstG, firstB = ri, gi, bi
			} else if uniform && (ri != firstR || gi != firstG || bi != firstB) {
				uniform = false
			}
			// BT.601 luma, integer, deterministic.
			lumaSum += uint64((ri*77 + gi*150 + bi*29) >> 8)
			n++
		}
	}

	mean := 0.0
	if n > 0 {
		mean = float64(lumaSum) / float64(n)
	}
	return dst, normStats{meanLuma: mean, uniform: uniform && n > 1}, nil
}

// sampleCoord maps a destination pixel center to a clamped source index and
// fractional weight for bilinear interpolation.
func sampleCoord(dstCenter, scale float64, srcLen int) (int, float64) {
	f := dstCenter*scale - 0.5
	if f <= 0 {
		return 0, 0
	}
	i := int(f)
	if i >= srcLen-1 {
		return srcLen - 1, 0
	}
	return i, f - float64(i)
}

// rgbAt reads an 8-bit RGB triple from img at (x, y).
func rgbAt(img image.Image, x, y int) (r, g, b int) {
	rr, gg, bb, _ := img.At(x, y).RGBA()
	return int(rr >> 8), int(gg >> 8), int(bb >> 8)
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }
