package processor

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"

	"file-compressor/internal/config"

	"github.com/gen2brain/webp"
)

// CompressImage decodes, optionally rotates (per JPEG EXIF orientation) and
// downsizes, and re-encodes data. The EXIF block is not carried over, so
// orientation is baked into the pixels; otherwise phone photos would show
// sideways after re-encoding.
func CompressImage(data []byte, ct string, set config.Settings) ([]byte, string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// Content sniffed as an image but won't decode: retrying can't help.
		return nil, "", permanentError{err}
	}

	var rgba *image.RGBA
	if ct == "image/jpeg" {
		if o := jpegOrientation(data); o > 1 {
			rgba = orient(toRGBA(img), o)
		}
	}
	if set.MaxDimension > 0 {
		if rgba == nil {
			rgba = toRGBA(img)
		}
		rgba = fitWithin(rgba, set.MaxDimension)
	}
	if rgba != nil {
		img = rgba
	}

	var buf bytes.Buffer

	if set.WebP {
		err = webp.Encode(&buf, img, webp.Options{Quality: set.ImageQuality})
		return buf.Bytes(), "image/webp", err
	}

	if ct == "image/jpeg" {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: set.ImageQuality})
		return buf.Bytes(), "image/jpeg", err
	}

	err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img)
	return buf.Bytes(), "image/png", err
}

// toRGBA returns img as an *image.RGBA with origin (0,0), copying only when
// needed.
func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) {
		return r
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// fitWithin shrinks src so its longer side is at most max pixels, keeping
// the aspect ratio, by averaging the source pixels under each destination
// pixel (good quality for downscaling). It never enlarges.
func fitWithin(src *image.RGBA, max int) *image.RGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	longest := w
	if h > longest {
		longest = h
	}
	if max <= 0 || longest <= max {
		return src
	}

	nw, nh := w*max/longest, h*max/longest
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for dy := 0; dy < nh; dy++ {
		y0, y1 := dy*h/nh, (dy+1)*h/nh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < nw; dx++ {
			x0, x1 := dx*w/nw, (dx+1)*w/nw
			if x1 <= x0 {
				x1 = x0 + 1
			}

			var r, g, b, a, n uint32
			for y := y0; y < y1; y++ {
				row := src.Pix[y*src.Stride:]
				for x := x0; x < x1; x++ {
					p := row[x*4 : x*4+4]
					r += uint32(p[0])
					g += uint32(p[1])
					b += uint32(p[2])
					a += uint32(p[3])
					n++
				}
			}
			o := dst.Pix[dy*dst.Stride+dx*4:]
			o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(b/n), uint8(a/n)
		}
	}
	return dst
}

// orient applies an EXIF orientation value (2-8) to src.
func orient(src *image.RGBA, o int) *image.RGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			default:
				nx, ny = x, y
			}
			copy(dst.Pix[ny*dst.Stride+nx*4:ny*dst.Stride+nx*4+4], src.Pix[y*src.Stride+x*4:y*src.Stride+x*4+4])
		}
	}
	return dst
}

// jpegOrientation returns the EXIF orientation (1-8) of a JPEG, or 1 if it
// has none or the metadata can't be parsed.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xFF { // fill byte
			i++
			continue
		}
		if marker == 0xD9 || marker == 0xDA { // end of image / start of scan
			return 1
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		if marker == 0xE1 {
			if o := exifOrientation(data[i+4 : i+2+segLen]); o != 0 {
				return o
			}
		}
		i += 2 + segLen
	}
	return 1
}

// exifOrientation reads tag 0x0112 from an APP1 segment payload, returning
// 0 if the segment isn't EXIF or has no valid orientation.
func exifOrientation(seg []byte) int {
	if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
		return 0
	}
	t := seg[6:]

	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 0
	}

	ifd := int(bo.Uint32(t[4:8]))
	if ifd < 8 || ifd+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[ifd:]))
	for k := 0; k < n; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}
