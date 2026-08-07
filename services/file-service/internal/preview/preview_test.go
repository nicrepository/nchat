package preview_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
)

// renderTimeout bounds every test render. It is generous: the PDF path builds a
// WebAssembly sandbox, which costs a few hundred milliseconds on first use.
const renderTimeout = 60 * time.Second

func renderBytes(t *testing.T, detectedMIME string, source []byte) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
	defer cancel()
	return preview.New().Render(ctx, detectedMIME, bytes.NewReader(source))
}

// decodePreview asserts the output is a JPEG and returns its geometry, which is
// the observable contract: callers get an image, not an internal buffer.
func decodePreview(t *testing.T, rendered []byte) image.Image {
	t.Helper()
	decoded, format, err := image.Decode(bytes.NewReader(rendered))
	if err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("preview format = %q, want jpeg", format)
	}
	return decoded
}

// gradientImage is a deterministic, compressible-but-not-uniform source, so a
// scaled result is meaningfully different from a blank one.
func gradientImage(width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x % 256), //nolint:gosec // G115: masked to a byte.
				G: uint8(y % 256), //nolint:gosec // G115: masked to a byte.
				B: 0x40,
				A: 0xff,
			})
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func encodeJPEGSource(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func encodeGIF(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

func TestRenderProducesABoundedJPEGForEverySupportedImageFormat(t *testing.T) {
	source := gradientImage(900, 600)
	cases := map[string][]byte{
		"image/png":  encodePNG(t, source),
		"image/jpeg": encodeJPEGSource(t, source),
		"image/gif":  encodeGIF(t, source),
	}
	for detectedMIME, encoded := range cases {
		t.Run(detectedMIME, func(t *testing.T) {
			rendered, err := renderBytes(t, detectedMIME, encoded)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			bounds := decodePreview(t, rendered).Bounds()
			if bounds.Dx() > domain.MaxPreviewDimension || bounds.Dy() > domain.MaxPreviewDimension {
				t.Fatalf("preview is %dx%d, want both edges <= %d",
					bounds.Dx(), bounds.Dy(), domain.MaxPreviewDimension)
			}
		})
	}
}

func TestRenderPreservesTheAspectRatioWhenScalingDown(t *testing.T) {
	rendered, err := renderBytes(t, "image/png", encodePNG(t, gradientImage(1600, 400)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	bounds := decodePreview(t, rendered).Bounds()
	if bounds.Dx() != domain.MaxPreviewDimension {
		t.Fatalf("width = %d, want %d", bounds.Dx(), domain.MaxPreviewDimension)
	}
	if want := domain.MaxPreviewDimension / 4; bounds.Dy() != want {
		t.Fatalf("height = %d, want %d for a 4:1 source", bounds.Dy(), want)
	}
}

func TestRenderDoesNotScaleUpAnImageAlreadyInsideTheBox(t *testing.T) {
	rendered, err := renderBytes(t, "image/png", encodePNG(t, gradientImage(48, 32)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	bounds := decodePreview(t, rendered).Bounds()
	if bounds.Dx() != 48 || bounds.Dy() != 32 {
		t.Fatalf("preview is %dx%d, want the source 48x32", bounds.Dx(), bounds.Dy())
	}
}

func TestRenderFlattensTransparencyOntoWhite(t *testing.T) {
	transparent := image.NewRGBA(image.Rect(0, 0, 64, 64))
	rendered, err := renderBytes(t, "image/png", encodePNG(t, transparent))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	decoded := decodePreview(t, rendered)
	r, g, b, _ := decoded.At(10, 10).RGBA()
	// JPEG is lossy, so the assertion is "essentially white", not an exact
	// value: a fully transparent source must never come out dark.
	if r>>8 < 0xf0 || g>>8 < 0xf0 || b>>8 < 0xf0 {
		t.Fatalf("transparent pixel rendered as %d,%d,%d, want near white", r>>8, g>>8, b>>8)
	}
}

func TestRenderRefusesAContentTypeWithNoRenderer(t *testing.T) {
	_, err := renderBytes(t, "application/zip", []byte("PK\x03\x04whatever"))
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

func TestRenderRefusesBytesThatAreNotTheImageTheyClaimToBe(t *testing.T) {
	_, err := renderBytes(t, "image/png", []byte("this is definitely not a png"))
	if !errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want ErrRender", err)
	}
}

func TestRenderRefusesATruncatedImage(t *testing.T) {
	encoded := encodePNG(t, gradientImage(200, 200))
	_, err := renderBytes(t, "image/png", encoded[:len(encoded)/2])
	if !errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want ErrRender", err)
	}
}

// pngHeaderClaiming builds a PNG that declares the given dimensions and carries
// no pixel data at all. It is the decompression-bomb shape in its purest form:
// a few dozen bytes on the wire that would allocate gigabytes if the decoder
// were allowed to start.
func pngHeaderClaiming(width, height uint32) []byte {
	const ihdrLength uint32 = 13

	var ihdr bytes.Buffer
	_ = binary.Write(&ihdr, binary.BigEndian, width)
	_ = binary.Write(&ihdr, binary.BigEndian, height)
	ihdr.Write([]byte{8, 2, 0, 0, 0}) // 8-bit truecolour, no interlace

	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	// The IHDR payload is 13 bytes by construction, so the width is a constant.
	_ = binary.Write(&out, binary.BigEndian, ihdrLength)
	chunk := append([]byte("IHDR"), ihdr.Bytes()...)
	out.Write(chunk)
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return out.Bytes()
}

func TestRenderRefusesADecompressionBombBeforeDecodingIt(t *testing.T) {
	bomb := pngHeaderClaiming(100_000, 100_000)
	if len(bomb) > 128 {
		t.Fatalf("bomb fixture is %d bytes, expected a tiny header", len(bomb))
	}
	_, err := renderBytes(t, "image/png", bomb)
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

func TestRenderRefusesASourceLargerThanTheReadLimit(t *testing.T) {
	oversized := io.LimitReader(zeroReader{}, domain.MaxPreviewSourceBytes+1)
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
	defer cancel()
	_, err := preview.New().Render(ctx, "image/png", oversized)
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

func TestRenderRefusesAnEmptySource(t *testing.T) {
	_, err := renderBytes(t, "image/png", nil)
	if !errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want ErrRender", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// singlePagePDF builds a valid one-page PDF with two filled rectangles, by
// hand. It is a few hundred bytes, so the suite carries no binary fixture.
func singlePagePDF() []byte {
	content := "1 0 0 rg\n10 10 80 60 re f\n0 0 1 rg\n120 20 60 60 re f\n"
	return assemblePDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R /Resources << >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
	})
}

// emptyPDF is structurally valid and has no page at all.
func emptyPDF() []byte {
	return assemblePDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [] /Count 0 >>",
	})
}

// assemblePDF writes a minimal cross-reference table around the given objects.
// extraTrailer lets a fixture add a trailer entry, which is how the encrypted
// case declares its security handler.
func assemblePDF(objects []string, extraTrailer ...string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = buf.Len()
		buf.WriteString(strconv.Itoa(i+1) + " 0 obj\n" + object + "\nendobj\n")
	}
	xref := buf.Len()
	buf.WriteString("xref\n0 " + strconv.Itoa(len(objects)+1) + "\n0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	trailer := ""
	if len(extraTrailer) > 0 {
		trailer = extraTrailer[0]
	}
	buf.WriteString("trailer\n<< /Size " + strconv.Itoa(len(objects)+1) +
		" /Root 1 0 R " + trailer + ">>\nstartxref\n" + strconv.Itoa(xref) + "\n%%EOF\n")
	return buf.Bytes()
}

func TestRenderRasterisesTheFirstPageOfAPDF(t *testing.T) {
	rendered, err := renderBytes(t, "application/pdf", singlePagePDF())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	bounds := decodePreview(t, rendered).Bounds()
	if bounds.Dx() > domain.MaxPreviewDimension || bounds.Dy() > domain.MaxPreviewDimension {
		t.Fatalf("preview is %dx%d, want both edges <= %d",
			bounds.Dx(), bounds.Dy(), domain.MaxPreviewDimension)
	}
	// The fixture page is twice as wide as it is tall, and the renderer must
	// fit it into the box rather than stretching it.
	if bounds.Dx() <= bounds.Dy() {
		t.Fatalf("preview is %dx%d, want a landscape page to stay landscape",
			bounds.Dx(), bounds.Dy())
	}
}

func TestRenderRefusesAPDFThatIsNotReadable(t *testing.T) {
	_, err := renderBytes(t, "application/pdf", []byte("%PDF-1.4\nbut nothing else at all"))
	if !errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want ErrRender", err)
	}
}

func TestRenderReportsAPDFWithoutPagesAsUnsupported(t *testing.T) {
	_, err := renderBytes(t, "application/pdf", emptyPDF())
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

func TestRenderStopsWhenTheContextIsAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := preview.New().Render(ctx, "application/pdf", bytes.NewReader(singlePagePDF()))
	if err == nil {
		t.Fatal("render returned no error for a cancelled context")
	}
	// Cancellation is transient: it must never be reported as content this
	// service cannot render, which would be a permanent state.
	if errors.Is(err, preview.ErrUnsupported) || errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want a transient failure", err)
	}
}

func TestRenderFitsAPortraitSourceIntoTheSameBox(t *testing.T) {
	rendered, err := renderBytes(t, "image/png", encodePNG(t, gradientImage(400, 1600)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	bounds := decodePreview(t, rendered).Bounds()
	if bounds.Dy() != domain.MaxPreviewDimension {
		t.Fatalf("height = %d, want %d", bounds.Dy(), domain.MaxPreviewDimension)
	}
	if want := domain.MaxPreviewDimension / 4; bounds.Dx() != want {
		t.Fatalf("width = %d, want %d for a 1:4 source", bounds.Dx(), want)
	}
}

// An extreme ratio must still produce an image rather than a zero-width one,
// which no encoder would accept.
func TestRenderKeepsAtLeastOnePixelOnTheShortEdge(t *testing.T) {
	rendered, err := renderBytes(t, "image/png", encodePNG(t, gradientImage(4000, 2)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	bounds := decodePreview(t, rendered).Bounds()
	if bounds.Dx() < 1 || bounds.Dy() < 1 {
		t.Fatalf("preview collapsed to %dx%d", bounds.Dx(), bounds.Dy())
	}
}

// A source that fails mid-read is storage's problem, not the content's: it must
// stay transient so the job retries instead of writing a terminal state.
func TestRenderReportsAFailingSourceAsTransient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), renderTimeout)
	defer cancel()
	_, err := preview.New().Render(ctx, "image/png", failingReader{})
	if err == nil {
		t.Fatal("expected the read failure to be reported")
	}
	if errors.Is(err, preview.ErrUnsupported) || errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want a transient failure", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("storage stream broke") }

// The PDF path must work without a deadline on the context: the job always sets
// one, but the renderer must not depend on it to decide anything.
func TestRenderAPDFWithoutADeadline(t *testing.T) {
	rendered, err := preview.New().Render(
		context.Background(), "application/pdf", bytes.NewReader(singlePagePDF()))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	decodePreview(t, rendered)
}

// An encrypted PDF is a legitimate file this service cannot look inside, so it
// is an expected absence rather than an operational failure: nobody is paged
// because a user shared a password-protected document.
func TestRenderReportsAProtectedPDFAsUnsupported(t *testing.T) {
	_, err := renderBytes(t, "application/pdf", encryptedPDF())
	if !errors.Is(err, preview.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

// encryptedPDF declares standard security handler encryption, which PDFium
// refuses to open without a password.
func encryptedPDF() []byte {
	return assemblePDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Resources << >> >>",
		"<< /Filter /Standard /V 1 /R 2 /O <0102030405060708090a0b0c0d0e0f10" +
			"1112131415161718191a1b1c1d1e1f20> /U <0102030405060708090a0b0c0d0e0f10" +
			"1112131415161718191a1b1c1d1e1f20> /P -1 >>",
	}, "/Encrypt 4 0 R ")
}

// --- hostile content ------------------------------------------------------
//
// Every fixture below is generated in-process: a few hundred bytes of header,
// never a captured file. Nothing here is a real exploit, nothing is executed,
// and the assertions are about *containment* — a bounded refusal, classified so
// the job knows whether to retry — rather than about detecting an attack.

// A file whose bytes disagree with what its name and its declared type claim is
// the ordinary case, not the exotic one: the detected type decides, and the
// decoder then has to agree with it too.
func TestRenderJudgesContentByItsBytesAndNotByWhatItClaims(t *testing.T) {
	for name, tt := range map[string]struct {
		detectedMIME string
		content      []byte
	}{
		"html served as jpeg": {
			detectedMIME: "image/jpeg",
			content:      []byte("<html><body><script>alert(1)</script></body></html>"),
		},
		"svg served as png": {
			detectedMIME: "image/png",
			content:      []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`),
		},
		"random bytes served as jpeg": {
			detectedMIME: "image/jpeg",
			content:      []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99},
		},
		"pdf bytes served as png": {
			detectedMIME: "image/png",
			content:      singlePagePDF(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderBytes(t, tt.detectedMIME, tt.content)
			if err == nil {
				t.Fatal("content that is not the image it claims must not render")
			}
			// Permanent, and classified: the job records a terminal state
			// instead of retrying a file that will never decode.
			if !errors.Is(err, preview.ErrRender) && !errors.Is(err, preview.ErrUnsupported) {
				t.Fatalf("error = %v, want a permanent classification", err)
			}
		})
	}
}

// A header whose dimensions multiply past what an int can hold must be refused
// before anything is allocated, not wrapped into a small "acceptable" number.
//
// Two layers can refuse it and both are acceptable: the decoder's own header
// validation, or this package's pixel limit. What is asserted is that one of
// them always does, permanently, and that nothing tries to allocate the
// billions of pixels the header asks for — the fixture is a bare header, so a
// decode that started would be visible as a hang or an out-of-memory, not as a
// tidy error value.
func TestRenderRefusesDimensionsThatWouldOverflowAPixelCount(t *testing.T) {
	for name, size := range map[string][2]uint32{
		"both edges enormous":                {0x7FFFFFFF, 0x7FFFFFFF},
		"one edge enormous":                  {0x7FFFFFFF, 2},
		"product wraps int32":                {65536, 65536},
		"inside int32, past the pixel limit": {40000, 40000},
	} {
		t.Run(name, func(t *testing.T) {
			// The product of these edges overflows a signed 32-bit count; the
			// check has to be made in a width that does not.
			if int64(size[0])*int64(size[1]) <= domain.MaxPreviewSourcePixels {
				t.Fatalf("fixture %s is inside the pixel limit", name)
			}
			_, err := renderBytes(t, "image/png", pngHeaderClaiming(size[0], size[1]))
			if !errors.Is(err, preview.ErrUnsupported) && !errors.Is(err, preview.ErrRender) {
				t.Fatalf("error = %v, want a permanent refusal", err)
			}
		})
	}
}

// A source truncated mid-stream is the shape a broken upload or a tampered
// object takes. It must fail, and it must fail without hanging.
func TestRenderRefusesTruncatedSourcesOfEveryAcceptedFormat(t *testing.T) {
	source := gradientImage(120, 90)
	for name, encoded := range map[string][]byte{
		"jpeg": encodeJPEGSource(t, source),
		"png":  encodePNG(t, source),
		"gif":  encodeGIF(t, source),
	} {
		t.Run(name, func(t *testing.T) {
			// A valid signature with a body cut in half: the decoder gets far
			// enough to believe the format and then runs out.
			truncated := encoded[:len(encoded)/2]
			mime := "image/" + name
			if _, err := renderBytes(t, mime, truncated); !errors.Is(err, preview.ErrRender) {
				t.Fatalf("error = %v, want ErrRender", err)
			}
		})
	}
	if _, err := renderBytes(t, "application/pdf", singlePagePDF()[:60]); err == nil {
		t.Fatal("a truncated pdf must not render")
	}
}

// A page far larger than any real document must still produce a bounded raster:
// the output box is the server's, never the document's.
func TestRenderBoundsAPDFWithAnExtremePageSize(t *testing.T) {
	content := "0 0 1 rg\n0 0 14000 14000 re f\n"
	huge := assemblePDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 14400 14400] /Contents 4 0 R /Resources << >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
	})

	rendered, err := renderBytes(t, "application/pdf", huge)
	if err != nil {
		// Refusing is an acceptable outcome for a page this size; rendering it
		// unbounded is not, and that is what the next branch checks.
		if !errors.Is(err, preview.ErrRender) && !errors.Is(err, preview.ErrUnsupported) {
			t.Fatalf("error = %v, want a permanent classification", err)
		}
		return
	}
	bounds := decodePreview(t, rendered).Bounds()
	if bounds.Dx() > domain.MaxPreviewDimension || bounds.Dy() > domain.MaxPreviewDimension {
		t.Fatalf("a %dx%d page produced a %dx%d preview", 14400, 14400, bounds.Dx(), bounds.Dy())
	}
}

// The deadline has to reach inside the sandbox, not merely bound the call that
// set it up: a render that will not finish must end when the job's time is up.
func TestRenderStopsAPDFAtTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	// Give the deadline time to pass before the sandbox is even built.
	<-ctx.Done()

	_, err := preview.New().Render(ctx, "application/pdf", bytes.NewReader(singlePagePDF()))
	if err == nil {
		t.Fatal("an expired deadline must stop the render")
	}
	if errors.Is(err, preview.ErrUnsupported) || errors.Is(err, preview.ErrRender) {
		t.Fatalf("error = %v, want a transient failure the job can retry", err)
	}
}

// The image path is bounded by the same limits regardless of how many frames a
// container carries: only the first is decoded, so an animation cannot multiply
// the work.
func TestRenderDecodesOnlyTheFirstFrameOfAnAnimatedGIF(t *testing.T) {
	var animated bytes.Buffer
	frames := &gif.GIF{}
	for range 24 {
		frame := image.NewPaletted(image.Rect(0, 0, 200, 200), palette.Plan9)
		frames.Image = append(frames.Image, frame)
		frames.Delay = append(frames.Delay, 0)
	}
	if err := gif.EncodeAll(&animated, frames); err != nil {
		t.Fatalf("encode animated gif: %v", err)
	}

	rendered, err := renderBytes(t, "image/gif", animated.Bytes())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	bounds := decodePreview(t, rendered).Bounds()
	if bounds.Dx() != 200 || bounds.Dy() != 200 {
		t.Fatalf("preview is %dx%d, want the single decoded frame", bounds.Dx(), bounds.Dy())
	}
}
