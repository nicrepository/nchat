package linkfetch

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"

	// The three decoders a thumbnail may be derived from. Registered by import;
	// anything else — SVG, WebP, AVIF, BMP, TIFF — is refused by the sniff below
	// before image.Decode is ever asked, so no decoder outside this list runs.
	_ "image/gif"
	_ "image/png"
)

// Derived images (issue #807, §20-21).
//
// The browser never fetches og:image itself: it would hand the reader's IP and
// user agent to the remote site for merely opening the conversation, and it
// would render remote bytes nobody inspected. What the browser gets is a
// thumbnail this function produced — sniffed, bounded before decoding,
// decoded by a standard-library decoder, downscaled, and re-encoded. Nothing of
// the original file survives except its pixels: no metadata, no embedded
// profile, no polyglot tail, no animation.
const (
	// MaxImageDimension and MaxImagePixels are read from the *header* before any
	// pixel is decoded. A PNG or GIF header can declare 100000×100000; refusing
	// on the declaration is what keeps a decompression bomb from ever
	// allocating.
	MaxImageDimension = 8192
	MaxImagePixels    = 20_000_000

	// ThumbnailMaxWidth and ThumbnailMaxHeight bound the derived image. A card
	// image is decorative; 480×320 is larger than any card draws it.
	ThumbnailMaxWidth  = 480
	ThumbnailMaxHeight = 320

	// MaxThumbnailBytes bounds the stored derivative. It is what makes storing
	// it inline in a row acceptable: a preview is at most this much image.
	MaxThumbnailBytes = 160 << 10

	// ThumbnailContentType is the only type a derived image ever has.
	ThumbnailContentType = "image/jpeg"

	// sniffBytes is what http.DetectContentType reads.
	sniffBytes = 512

	// samplesPerAxis bounds the work of downscaling: each output pixel averages
	// at most this many samples per axis from its source box, so the cost is a
	// function of the thumbnail's size and not of the original's.
	samplesPerAxis = 3
)

// Thumbnail is a derived image ready to be stored and served by NChat.
type Thumbnail struct {
	Data        []byte
	ContentType string
	Width       int
	Height      int
}

// allowedImageTypes are the sniffed types a thumbnail may be derived from.
var allowedImageTypes = map[string]struct{}{
	"image/jpeg": {}, "image/png": {}, "image/gif": {},
}

// DeriveThumbnail turns remote image bytes into a bounded JPEG thumbnail, or
// refuses them.
//
// The order is the defence: sniff the real type, bound the declared
// dimensions, and only then decode. A GIF yields its first frame; a PNG with
// transparency is composited onto white, because the output has no alpha.
func DeriveThumbnail(data []byte) (Thumbnail, error) {
	if err := checkImageHeader(data); err != nil {
		return Thumbnail{}, err
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Thumbnail{}, fmt.Errorf("%w: image could not be decoded", ErrImageRejected)
	}
	width, height := fitWithin(source.Bounds().Dx(), source.Bounds().Dy(), ThumbnailMaxWidth, ThumbnailMaxHeight)
	return encodeBounded(downscale(source, width, height))
}

// checkImageHeader sniffs the bytes and bounds the declared dimensions without
// decoding a pixel.
func checkImageHeader(data []byte) error {
	head := data
	if len(head) > sniffBytes {
		head = head[:sniffBytes]
	}
	if _, ok := allowedImageTypes[http.DetectContentType(head)]; !ok {
		return fmt.Errorf("%w: not a supported image", ErrImageRejected)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%w: image header could not be read", ErrImageRejected)
	}
	if config.Width <= 0 || config.Height <= 0 ||
		config.Width > MaxImageDimension || config.Height > MaxImageDimension ||
		config.Width*config.Height > MaxImagePixels {
		return fmt.Errorf("%w: image dimensions out of bounds", ErrImageRejected)
	}
	return nil
}

// fitWithin scales (width, height) down to fit the box, preserving aspect ratio
// and never enlarging.
func fitWithin(width, height, maxWidth, maxHeight int) (int, int) {
	if width <= maxWidth && height <= maxHeight {
		return width, height
	}
	scale := min(float64(maxWidth)/float64(width), float64(maxHeight)/float64(height))
	return max(1, int(float64(width)*scale)), max(1, int(float64(height)*scale))
}

// downscale produces a width×height RGBA image by averaging a bounded grid of
// samples from each output pixel's source box, composited onto white.
//
// ponytail: box-sampled average rather than a proper filter. It is what a
// thumbnail needs and it needs no dependency; golang.org/x/image/draw is the
// upgrade if card images ever look wrong.
func downscale(source image.Image, width, height int) *image.RGBA {
	bounds := source.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	scaleX := float64(bounds.Dx()) / float64(width)
	scaleY := float64(bounds.Dy()) / float64(height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			out.SetRGBA(x, y, averageBox(source, bounds, float64(x)*scaleX, float64(y)*scaleY, scaleX, scaleY))
		}
	}
	return out
}

// averageBox averages up to samplesPerAxis² pixels of the source box that
// starts at (x0, y0) and spans (w, h), blending each onto white.
func averageBox(source image.Image, bounds image.Rectangle, x0, y0, w, h float64) color.RGBA {
	samples := max(1, min(samplesPerAxis, int(max(w, h))))
	var r, g, b uint64
	for sy := 0; sy < samples; sy++ {
		for sx := 0; sx < samples; sx++ {
			px := bounds.Min.X + int(x0+(float64(sx)+0.5)*w/float64(samples))
			py := bounds.Min.Y + int(y0+(float64(sy)+0.5)*h/float64(samples))
			cr, cg, cb := onWhite(source.At(min(px, bounds.Max.X-1), min(py, bounds.Max.Y-1)))
			r, g, b = r+uint64(cr), g+uint64(cg), b+uint64(cb)
		}
	}
	count := uint64(samples * samples)
	return color.RGBA{R: channel(r / count), G: channel(g / count), B: channel(b / count), A: 0xff}
}

// onWhite composites one premultiplied colour onto an opaque white background
// and returns 8-bit channels.
func onWhite(c color.Color) (uint8, uint8, uint8) {
	r, g, b, a := c.RGBA()
	gap := uint64(0xffff - a)
	return channel((uint64(r) + gap) >> 8), channel((uint64(g) + gap) >> 8), channel((uint64(b) + gap) >> 8)
}

// channel narrows an averaged or composited value to one 8-bit channel. The
// inputs are already in range; the clamp is what makes that visible to the
// reader and to the integer-overflow linter alike.
func channel(value uint64) uint8 {
	if value > 0xff {
		return 0xff
	}
	return uint8(value)
}

// encodeBounded encodes the thumbnail as JPEG under MaxThumbnailBytes, lowering
// quality and then dimensions until it fits. Refusal is the last resort and
// practically unreachable at these dimensions.
func encodeBounded(img *image.RGBA) (Thumbnail, error) {
	for _, quality := range []int{82, 65, 50} {
		var buffer bytes.Buffer
		if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: quality}); err != nil {
			return Thumbnail{}, fmt.Errorf("%w: image could not be encoded", ErrImageRejected)
		}
		if buffer.Len() <= MaxThumbnailBytes {
			return Thumbnail{Data: buffer.Bytes(), ContentType: ThumbnailContentType,
				Width: img.Bounds().Dx(), Height: img.Bounds().Dy()}, nil
		}
	}
	if img.Bounds().Dx() <= 32 || img.Bounds().Dy() <= 32 {
		return Thumbnail{}, fmt.Errorf("%w: image could not be bounded", ErrImageRejected)
	}
	return encodeBounded(downscale(img, img.Bounds().Dx()/2, img.Bounds().Dy()/2))
}
