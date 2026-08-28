package service

import (
	"bytes"
	"image"
	"image/draw"
	_ "image/jpeg" // register the JPEG decoder; no GIF/BMP/WebP on purpose
	"image/png"
	"io"
	"net/http"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// allowedSniffedTypes is the content-type allowlist applied to the raw bytes
// before any decode. SVG (scriptable), GIF, BMP, TIFF, ICO, PDF and HTML are
// absent by design, so a file claiming to be one is rejected at the door.
var allowedSniffedTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
}

// ProcessAvatar validates raw upload bytes and returns a canonical, metadata-free
// PNG square. It is the single trust boundary for avatar content: nothing the
// user sends is ever stored verbatim.
//
// The pipeline is defence-in-depth:
//  1. size cap via LimitReader — a body one byte over the limit is rejected
//     without buffering the whole thing;
//  2. content sniffing on the real bytes — extension and multipart Content-Type
//     are never trusted;
//  3. dimension/megapixel check from the header *before* decoding, so a
//     decompression bomb never gets to allocate its canvas;
//  4. a real decode with only JPEG/PNG decoders registered;
//  5. re-encode from decoded pixels, which strips EXIF/trailing polyglot data
//     and yields a fixed-size PNG.
func ProcessAvatar(r io.Reader) ([]byte, error) {
	// +1 so exactly-at-limit passes and over-limit is detectable.
	raw, err := io.ReadAll(io.LimitReader(r, domain.AvatarMaxUploadBytes+1))
	if err != nil {
		return nil, domain.ErrAvatarUnsupported
	}
	if len(raw) == 0 {
		return nil, domain.ErrAvatarUnsupported
	}
	if len(raw) > domain.AvatarMaxUploadBytes {
		return nil, domain.ErrAvatarTooLarge
	}

	if !allowedSniffedTypes[http.DetectContentType(raw)] {
		return nil, domain.ErrAvatarUnsupported
	}

	// Header-only inspection first: format string and advertised dimensions.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, domain.ErrAvatarUnsupported
	}
	if cfg.Width < domain.AvatarMinEdge || cfg.Height < domain.AvatarMinEdge {
		return nil, domain.ErrAvatarUnsupported
	}
	if cfg.Width*cfg.Height > domain.AvatarMaxPixels {
		return nil, domain.ErrAvatarUnsupported
	}

	src, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, domain.ErrAvatarUnsupported
	}

	square := cropCenterSquare(src)
	out := resizeNearest(square, domain.AvatarOutputSize)

	var buf bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := encoder.Encode(&buf, out); err != nil {
		return nil, domain.ErrAvatarUnsupported
	}
	return buf.Bytes(), nil
}

// cropCenterSquare returns the largest centered square region of img as an RGBA
// image, so the subsequent resize never distorts the aspect ratio.
func cropCenterSquare(img image.Image) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	edge := w
	if h < edge {
		edge = h
	}
	offsetX := b.Min.X + (w-edge)/2
	offsetY := b.Min.Y + (h-edge)/2
	dst := image.NewRGBA(image.Rect(0, 0, edge, edge))
	draw.Draw(dst, dst.Bounds(), img, image.Pt(offsetX, offsetY), draw.Src)
	return dst
}

// resizeNearest scales a square image to size x size with nearest-neighbour
// sampling. It is dependency-free and good enough for a 256px avatar.
// ponytail: nearest-neighbour; swap for x/image/draw CatmullRom if avatar
// quality ever becomes a complaint.
func resizeNearest(src *image.RGBA, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	srcEdge := src.Bounds().Dx()
	if srcEdge == 0 {
		return dst
	}
	for y := 0; y < size; y++ {
		sy := y * srcEdge / size
		for x := 0; x < size; x++ {
			sx := x * srcEdge / size
			dst.Set(x, y, src.At(src.Bounds().Min.X+sx, src.Bounds().Min.Y+sy))
		}
	}
	return dst
}
