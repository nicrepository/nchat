package linkfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func pngBytes(t *testing.T, width, height int, fill color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("png: %v", err)
	}
	return buffer.Bytes()
}

func jpegBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, nil); err != nil {
		t.Fatalf("jpeg: %v", err)
	}
	return buffer.Bytes()
}

func gifBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.Black, color.White})
	var buffer bytes.Buffer
	if err := gif.Encode(&buffer, img, nil); err != nil {
		t.Fatalf("gif: %v", err)
	}
	return buffer.Bytes()
}

func decodeThumbnail(t *testing.T, thumb Thumbnail) image.Image {
	t.Helper()
	decoded, err := jpeg.Decode(bytes.NewReader(thumb.Data))
	if err != nil {
		t.Fatalf("thumbnail is not a JPEG: %v", err)
	}
	return decoded
}

func TestDeriveThumbnailDownscalesAndReencodes(t *testing.T) {
	thumb, err := DeriveThumbnail(pngBytes(t, 1600, 800, color.NRGBA{R: 200, G: 30, B: 30, A: 255}))
	if err != nil {
		t.Fatalf("DeriveThumbnail: %v", err)
	}
	if thumb.ContentType != ThumbnailContentType {
		t.Fatalf("content type: %q", thumb.ContentType)
	}
	if thumb.Width != 480 || thumb.Height != 240 {
		t.Fatalf("expected 480×240 keeping aspect, got %d×%d", thumb.Width, thumb.Height)
	}
	if len(thumb.Data) > MaxThumbnailBytes {
		t.Fatalf("thumbnail is %d bytes, over the ceiling", len(thumb.Data))
	}
	decoded := decodeThumbnail(t, thumb)
	if decoded.Bounds().Dx() != 480 {
		t.Fatalf("decoded width %d", decoded.Bounds().Dx())
	}
	r, _, _, _ := decoded.At(10, 10).RGBA()
	if r>>8 < 150 {
		t.Fatalf("expected the red fill to survive, got r=%d", r>>8)
	}
}

func TestDeriveThumbnailNeverEnlarges(t *testing.T) {
	thumb, err := DeriveThumbnail(jpegBytes(t, 120, 90))
	if err != nil {
		t.Fatalf("DeriveThumbnail: %v", err)
	}
	if thumb.Width != 120 || thumb.Height != 90 {
		t.Fatalf("a small image must keep its size, got %d×%d", thumb.Width, thumb.Height)
	}
}

func TestDeriveThumbnailCompositesTransparencyOntoWhite(t *testing.T) {
	thumb, err := DeriveThumbnail(pngBytes(t, 40, 40, color.NRGBA{A: 0}))
	if err != nil {
		t.Fatalf("DeriveThumbnail: %v", err)
	}
	r, g, b, _ := decodeThumbnail(t, thumb).At(20, 20).RGBA()
	if r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Fatalf("transparent pixels must become white, got %d %d %d", r>>8, g>>8, b>>8)
	}
}

func TestDeriveThumbnailAcceptsGIFFirstFrame(t *testing.T) {
	if _, err := DeriveThumbnail(gifBytes(t)); err != nil {
		t.Fatalf("gif: %v", err)
	}
}

func TestDeriveThumbnailRefusesWhatIsNotARasterImage(t *testing.T) {
	cases := map[string][]byte{
		"html":               []byte("<html><body>not an image</body></html>"),
		"svg":                []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"empty":              {},
		"pdf":                []byte("%PDF-1.4 fake"),
		"webp":               append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 64)...),
		"truncated png":      pngBytes(t, 10, 10, color.White)[:20],
		"jpeg magic, junk":   append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0}, 100)...),
		"png header only":    pngBytes(t, 10, 10, color.White)[:33],
		"gif magic no image": []byte("GIF89a"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DeriveThumbnail(data); !errors.Is(err, ErrImageRejected) {
				t.Fatalf("want ErrImageRejected, got %v", err)
			}
		})
	}
}

// TestDeriveThumbnailRefusesDeclaredBombsBeforeDecoding is the pixel-bomb case:
// a tiny file whose header declares an enormous image. The refusal must come
// from the header, so nothing is allocated for the declared size.
func TestDeriveThumbnailRefusesDeclaredBombsBeforeDecoding(t *testing.T) {
	// A PNG signature and IHDR declaring 30000×30000, then nothing. DecodeConfig
	// reads only the header.
	var buffer bytes.Buffer
	buffer.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	buffer.Write([]byte{0, 0, 0, 13, 'I', 'H', 'D', 'R'})
	buffer.Write([]byte{0, 0, 0x75, 0x30, 0, 0, 0x75, 0x30, 8, 6, 0, 0, 0}) // 30000×30000, RGBA
	buffer.Write([]byte{0, 0, 0, 0})                                        // CRC (unchecked by DecodeConfig)
	if _, err := DeriveThumbnail(buffer.Bytes()); !errors.Is(err, ErrImageRejected) {
		t.Fatalf("declared bomb must be refused, got %v", err)
	}

	// Also over MaxImagePixels while under MaxImageDimension per axis.
	buffer.Reset()
	buffer.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	buffer.Write([]byte{0, 0, 0, 13, 'I', 'H', 'D', 'R'})
	buffer.Write([]byte{0, 0, 0x1f, 0x40, 0, 0, 0x1f, 0x40, 8, 6, 0, 0, 0}) // 8000×8000 = 64M px
	buffer.Write([]byte{0, 0, 0, 0})
	if _, err := DeriveThumbnail(buffer.Bytes()); !errors.Is(err, ErrImageRejected) {
		t.Fatalf("pixel ceiling must be enforced, got %v", err)
	}
}

func TestFitWithin(t *testing.T) {
	cases := []struct{ w, h, mw, mh, ew, eh int }{
		{100, 50, 480, 320, 100, 50},
		{960, 640, 480, 320, 480, 320},
		{1000, 100, 480, 320, 480, 48},
		{100, 1000, 480, 320, 32, 320},
		{1, 10000, 480, 320, 1, 320},
	}
	for _, tc := range cases {
		if w, h := fitWithin(tc.w, tc.h, tc.mw, tc.mh); w != tc.ew || h != tc.eh {
			t.Errorf("fitWithin(%d,%d): got %d×%d want %d×%d", tc.w, tc.h, w, h, tc.ew, tc.eh)
		}
	}
}

func TestEncodeBoundedHalvesWhenQualityIsNotEnough(t *testing.T) {
	// Noise does not compress. At 480×320 the noisiest image still fits within
	// 160 KiB at low quality, so the halving branch needs a larger canvas.
	img := image.NewRGBA(image.Rect(0, 0, 1400, 1000))
	seed := uint32(7)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = uint8(seed >> 24)
	}
	thumb, err := encodeBounded(img)
	if err != nil {
		t.Fatalf("encodeBounded: %v", err)
	}
	if len(thumb.Data) > MaxThumbnailBytes {
		t.Fatalf("still %d bytes", len(thumb.Data))
	}
	if thumb.Width >= 1400 {
		t.Fatalf("expected the canvas to have been halved, got %d", thumb.Width)
	}
}

// --- FetchImage and the hop policy ------------------------------------------

func imageServer(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func fetchImageAgainst(t *testing.T, server *httptest.Server, rawURL string) ([]byte, error) {
	t.Helper()
	connector := &recordingConnector{target: strings.TrimPrefix(server.URL, "http://")}
	target, err := ParseURL(rawURL)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	fetcher := NewFetcherWith(5*time.Second, fixedResolver(publicAddr), connector.connect)
	return fetcher.FetchImage(context.Background(), target, nil)
}

func TestFetchImageAcceptsImageTypesOnly(t *testing.T) {
	data := pngBytes(t, 4, 4, color.White)
	if body, err := fetchImageAgainst(t, imageServer(t, "image/png", data), "http://cdn.example.com/a.png"); err != nil || !bytes.Equal(body, data) {
		t.Fatalf("png: %v", err)
	}
	if _, err := fetchImageAgainst(t, imageServer(t, "text/html", []byte("<html>")), "http://cdn.example.com/a.png"); !errors.Is(err, ErrUnsupportedContentType) {
		t.Fatalf("html as image must be refused, got %v", err)
	}
	if _, err := fetchImageAgainst(t, imageServer(t, "image/png", make([]byte, MaxImageBytes+1)), "http://cdn.example.com/big.png"); !errors.Is(err, ErrUpstream) {
		t.Fatalf("oversized image must be refused, got %v", err)
	}
}

func TestFetchDocumentRefusesAnImageResponse(t *testing.T) {
	server := imageServer(t, "image/png", pngBytes(t, 4, 4, color.White))
	_, _, _, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/page")
	if !errors.Is(err, ErrUnsupportedContentType) {
		t.Fatalf("document fetch must not accept an image, got %v", err)
	}
}

var errHopPending = errors.New("hop target has no clearance yet")

func TestHopPolicyVetoesARedirectAfterSSRFPolicyAccepted(t *testing.T) {
	server := redirectServer(t, "http://elsewhere.example/ok", http.StatusFound)
	resolve := hostResolver(map[string]string{"example.com": publicAddr, "elsewhere.example": "8.8.8.8"})
	connector := &recordingConnector{target: strings.TrimPrefix(server.URL, "http://")}
	target, _ := ParseURL("http://example.com/start")
	fetcher := NewFetcherWith(5*time.Second, resolve, connector.connect)

	var asked []string
	_, _, err := fetcher.FetchDocument(context.Background(), target, func(next *url.URL) error {
		asked = append(asked, next.Host)
		return errHopPending
	})

	if !errors.Is(err, ErrRedirectRefused) || !errors.Is(err, errHopPending) {
		t.Fatalf("a vetoed hop must report ErrRedirectRefused carrying the caller's reason, got %v", err)
	}
	if len(asked) != 1 || asked[0] != "elsewhere.example" {
		t.Fatalf("hop policy must be asked about the redirect target, got %v", asked)
	}
	if strings.Contains(err.Error(), "elsewhere") {
		t.Fatalf("error must not name the destination: %v", err)
	}
	if dialed := connector.addresses(); len(dialed) != 1 {
		t.Fatalf("the vetoed hop must not be dialled, got %v", dialed)
	}
}

func TestHopPolicyIsNotAskedAboutAHopSSRFAlreadyRefused(t *testing.T) {
	server := redirectServer(t, "http://internal.example/admin", http.StatusFound)
	resolve := hostResolver(map[string]string{"example.com": publicAddr, "internal.example": "127.0.0.1"})
	connector := &recordingConnector{target: strings.TrimPrefix(server.URL, "http://")}
	target, _ := ParseURL("http://example.com/start")
	fetcher := NewFetcherWith(5*time.Second, resolve, connector.connect)

	// Not the URL rules — a public-looking hostname passes those — but the
	// dialer's address check, which runs when the hop is followed. The policy
	// allowed it; the address refused it; the error is the address refusal.
	_, _, err := fetcher.FetchDocument(context.Background(), target, func(*url.URL) error { return nil })
	if !errors.Is(err, ErrURLNotAllowed) {
		t.Fatalf("redirect into loopback must be refused by address, got %v", err)
	}
}

func TestHopPolicyAllowsAClearedRedirect(t *testing.T) {
	server := redirectServer(t, "http://elsewhere.example/ok", http.StatusFound)
	resolve := hostResolver(map[string]string{"example.com": publicAddr, "elsewhere.example": "8.8.8.8"})
	connector := &recordingConnector{target: strings.TrimPrefix(server.URL, "http://")}
	target, _ := ParseURL("http://example.com/start")
	fetcher := NewFetcherWith(5*time.Second, resolve, connector.connect)

	final, body, err := fetcher.FetchDocument(context.Background(), target, func(*url.URL) error { return nil })
	if err != nil || !strings.Contains(string(body), "arrived") || final.Host != "elsewhere.example" {
		t.Fatalf("cleared hop must be followed: %v %q", err, final)
	}
}

func TestEnvironmentProxyIsIgnored(t *testing.T) {
	// A proxy in the environment would receive the request instead of the
	// validated address. The recording connector sees the dial; a proxied
	// request would dial the proxy's address instead of publicAddr.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><head><title>proxied</title></head></html>")
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)

	server := htmlServer(t, "<html><head><title>direct</title></head></html>")
	connector, _, body, err := fetchAgainst(t, server, fixedResolver(publicAddr), "http://example.com/page")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Contains(string(body), "proxied") {
		t.Fatal("the request went through the environment proxy")
	}
	if dialed := connector.addresses(); len(dialed) != 1 || !strings.HasPrefix(dialed[0], publicAddr) {
		t.Fatalf("expected a direct dial to the validated address, got %v", dialed)
	}
}
