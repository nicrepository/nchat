package preview

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"

	// The decoders this package supports, and only those. An image format that
	// is not imported here cannot be decoded even if a file claims it, which is
	// the point: the set of parsers reachable from an upload is decided at
	// compile time.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// decodableFormats maps image.Decode's format name to itself. It is the second
// half of the allowlist: domain.PreviewSupported decides which *detected* types
// reach a decoder, this decides which *actual* formats are accepted once the
// bytes are read, so a file whose sniffed type and real header disagree is
// refused instead of being rendered as whichever one won.
var decodableFormats = map[string]struct{}{
	"jpeg": {},
	"png":  {},
	"gif":  {},
}

// renderImage decodes an image and returns a bounded JPEG thumbnail.
//
// The order is deliberate and is the decompression-bomb defence: the header is
// parsed first, on its own, and the pixel count it declares is checked before
// image.Decode is ever called. A 100000x100000 PNG is a few hundred kilobytes
// on the wire and 40 GB decoded; it is refused here having allocated nothing.
func renderImage(data []byte) ([]byte, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: image header is not readable", ErrRender)
	}
	if _, ok := decodableFormats[format]; !ok {
		return nil, fmt.Errorf("%w: image format %q has no renderer", ErrUnsupported, format)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("%w: image has no area", ErrRender)
	}
	if int64(config.Width)*int64(config.Height) > domain.MaxPreviewSourcePixels {
		return nil, fmt.Errorf("%w: image exceeds the preview pixel limit", ErrUnsupported)
	}

	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: image is not decodable", ErrRender)
	}
	return encodeJPEG(thumbnail(source, domain.MaxPreviewDimension))
}

// encodeJPEG produces the one output format previews have.
//
// Re-encoding is also what strips metadata: the output is built from pixels, so
// EXIF, GPS coordinates, colour profiles, comments and any appended payload in
// the original are simply not carried over. Nothing is copied from the source
// container.
func encodeJPEG(img image.Image) ([]byte, error) {
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: domain.PreviewJPEGQuality}); err != nil {
		return nil, fmt.Errorf("%w: encode preview: %v", ErrRender, err)
	}
	return out.Bytes(), nil
}

// thumbnail scales src down so its longest edge is at most max, preserving the
// aspect ratio, and flattens transparency onto white.
//
// It never scales up: an image already inside the box is only flattened, so a
// 32x32 icon stays 32x32 instead of becoming a blurry 512x512.
//
// The filter is a box average — every source pixel inside a destination pixel's
// footprint contributes exactly once. For downscaling that is both the simplest
// and the right choice: sampling a single pixel per destination (nearest
// neighbour) aliases badly at the ratios thumbnails use, and anything fancier
// would be a dependency for a difference nobody can see at 512px.
func thumbnail(src image.Image, max int) *image.RGBA {
	bounds := src.Bounds()
	width, height := fit(bounds.Dx(), bounds.Dy(), max)
	dst := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		// Source rows covered by this destination row. The multiplication is
		// done before the division so the footprints tile the source exactly,
		// with no gap and no overlap.
		top := bounds.Min.Y + y*bounds.Dy()/height
		bottom := bounds.Min.Y + (y+1)*bounds.Dy()/height
		if bottom <= top {
			bottom = top + 1
		}
		for x := 0; x < width; x++ {
			left := bounds.Min.X + x*bounds.Dx()/width
			right := bounds.Min.X + (x+1)*bounds.Dx()/width
			if right <= left {
				right = left + 1
			}
			dst.SetRGBA(x, y, averageOverWhite(src, left, top, right, bottom))
		}
	}
	return dst
}

// white is the background every preview is composited onto. JPEG has no alpha
// channel, so a transparent source has to land on something; white is what a
// document viewer would show.
var white = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

// averageOverWhite averages one destination pixel's source footprint and
// composites the result over an opaque white background.
//
// RGBA() yields alpha-premultiplied 16-bit components, which is exactly what
// makes both steps sound: premultiplied values may be averaged directly, and
// compositing over white is then a single addition of the uncovered fraction.
func averageOverWhite(src image.Image, left, top, right, bottom int) color.RGBA {
	var red, green, blue, alpha, count uint64
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			red += uint64(r)
			green += uint64(g)
			blue += uint64(b)
			alpha += uint64(a)
			count++
		}
	}
	if count == 0 {
		return white
	}
	uncovered := 0xffff - alpha/count
	return color.RGBA{
		R: to8(red/count + uncovered),
		G: to8(green/count + uncovered),
		B: to8(blue/count + uncovered),
		A: 0xff,
	}
}

// to8 narrows a 16-bit component to 8 bits, clamping first. Rounding error in
// the average can leave a value a hair above full scale; clamping keeps that
// from wrapping to black.
func to8(value uint64) uint8 {
	if value > 0xffff {
		value = 0xffff
	}
	return uint8(value >> 8) //nolint:gosec // G115: clamped to 16 bits above.
}

// fit returns the largest size within a max-by-max box that keeps the source
// aspect ratio. A source already inside the box keeps its size.
func fit(width, height, max int) (int, int) {
	if width <= 0 || height <= 0 {
		return 1, 1
	}
	if width <= max && height <= max {
		return width, height
	}
	if width >= height {
		scaled := height * max / width
		if scaled < 1 {
			scaled = 1
		}
		return max, scaled
	}
	scaled := width * max / height
	if scaled < 1 {
		scaled = 1
	}
	return scaled, max
}
