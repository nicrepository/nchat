package service

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// makePNG builds a solid-colour PNG of the given size for use as valid input.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: uint8(x % 256), B: uint8(y % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestProcessAvatar_ReencodesToFixedSquarePNG(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{name: "png landscape", in: makePNG(t, 400, 200)},
		{name: "jpeg portrait", in: makeJPEG(t, 200, 500)},
		{name: "png square", in: makePNG(t, 300, 300)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ProcessAvatar(bytes.NewReader(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Output must be PNG regardless of input format.
			cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
			if err != nil || format != "png" {
				t.Fatalf("output not png: format=%q err=%v", format, err)
			}
			if cfg.Width != domain.AvatarOutputSize || cfg.Height != domain.AvatarOutputSize {
				t.Fatalf("expected %dx%d, got %dx%d", domain.AvatarOutputSize, domain.AvatarOutputSize, cfg.Width, cfg.Height)
			}
		})
	}
}

func TestProcessAvatar_RejectsNonImageAndDangerousTypes(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte("<!doctype html><html><body>hi</body></html>")
	var animatedGIF bytes.Buffer
	_ = gif.EncodeAll(&animatedGIF, &gif.GIF{
		Image: []*image.Paletted{
			image.NewPaletted(image.Rect(0, 0, 32, 32), color.Palette{color.Black, color.White}),
			image.NewPaletted(image.Rect(0, 0, 32, 32), color.Palette{color.Black, color.White}),
		},
		Delay: []int{0, 0},
	})

	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{name: "svg", in: svg},
		{name: "html", in: html},
		{name: "gif", in: animatedGIF.Bytes()},
		{name: "plain text", in: []byte("just some text, not an image at all")},
		{name: "truncated png", in: makePNG(t, 100, 100)[:40]},
		{name: "empty", in: nil},
		{name: "jpeg-named html polyglot header", in: append([]byte("\xff\xd8\xff"), html...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProcessAvatar(bytes.NewReader(tc.in))
			if !IsAvatarUnsupported(err) {
				t.Fatalf("expected unsupported error, got %v", err)
			}
		})
	}
}

func TestProcessAvatar_RejectsTooSmallAndTooLarge(t *testing.T) {
	tiny := makePNG(t, 8, 8) // below AvatarMinEdge
	if _, err := ProcessAvatar(bytes.NewReader(tiny)); !IsAvatarUnsupported(err) {
		t.Fatalf("expected unsupported for tiny image, got %v", err)
	}

	// Over the byte cap: pad a valid PNG's trailing bytes past the limit.
	oversized := append(makePNG(t, 64, 64), bytes.Repeat([]byte{0}, domain.AvatarMaxUploadBytes)...)
	if _, err := ProcessAvatar(bytes.NewReader(oversized)); !IsAvatarTooLarge(err) {
		t.Fatalf("expected too-large error, got %v", err)
	}
}

func TestProcessAvatar_RejectsExcessiveDimensions(t *testing.T) {
	// A header advertising a huge canvas must be rejected before decoding, even
	// if the encoded bytes are small. Build a valid but oversized PNG cheaply by
	// encoding a 1-px-tall, very wide image whose W*H still exceeds the cap only
	// when both dims are large; instead assert the megapixel guard via config.
	big := makePNG(t, 6000, 5000) // 30 MP > AvatarMaxPixels
	if _, err := ProcessAvatar(bytes.NewReader(big)); !IsAvatarUnsupported(err) {
		t.Fatalf("expected unsupported for oversized canvas, got %v", err)
	}
}

func TestProcessAvatar_StripsTrailingDataViaReencode(t *testing.T) {
	// A valid PNG with appended junk decodes fine; the re-encoded output must be
	// a clean PNG that no longer contains the trailing marker.
	marker := "TRAILING_SECRET_MARKER"
	poly := append(makePNG(t, 64, 64), []byte(marker)...)
	out, err := ProcessAvatar(bytes.NewReader(poly))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(out), marker) {
		t.Fatal("re-encoded avatar still contains trailing input bytes")
	}
}
