package converter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	MaxInputBytes = 20 << 20
	MaxPDFBytes   = 50 << 20
	// MaxAudioInputBytes/MaxAudioOutputBytes match the sidecar's own caps
	// (converter.MaxAudio{Input,Output}Bytes in document-converter) — this
	// client refuses locally what the sidecar would refuse anyway, so an
	// oversized attachment never even makes the round trip.
	MaxAudioInputBytes  = 50 << 20
	MaxAudioOutputBytes = 50 << 20
)

type Format string

const (
	FormatDOCX Format = "docx"
	FormatODT  Format = "odt"
	FormatPPT  Format = "ppt"
	FormatPPTX Format = "pptx"
)

// AudioFormat is the input container the sidecar's /v1/convert-audio route
// demuxes before re-encoding to MP3 — a container, not a codec.
type AudioFormat string

const (
	AudioFormatOgg  AudioFormat = "ogg"
	AudioFormatWav  AudioFormat = "wav"
	AudioFormatWebM AudioFormat = "webm"
	AudioFormatMP4  AudioFormat = "mp4"
)

var (
	ErrBlocked   = errors.New("converter blocked document")
	ErrPermanent = errors.New("permanent converter failure")
	ErrTransient = errors.New("transient converter failure")
)

type Client struct {
	endpoint      string
	audioEndpoint string
	http          *http.Client
}

func NewClient(rawURL string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("DOCUMENT_CONVERTER_URL must be an absolute HTTP(S) origin")
	}
	if timeout <= 0 || timeout >= 40*time.Second {
		return nil, errors.New("document converter timeout must be positive and below 40 seconds")
	}
	origin := strings.TrimSuffix(parsed.String(), "/")
	return &Client{
		endpoint:      origin + "/v1/convert",
		audioEndpoint: origin + "/v1/convert-audio",
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Convert(ctx context.Context, format Format, source io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(source, MaxInputBytes+1))
	if err != nil || len(body) > MaxInputBytes {
		return nil, fmt.Errorf("%w: read document", ErrPermanent)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request", ErrPermanent)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Document-Format", string(format))
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: request failed", ErrTransient)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusOK {
		if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0])); mediaType != "application/pdf" {
			return nil, fmt.Errorf("%w: invalid response content type", ErrPermanent)
		}
		pdf, readErr := io.ReadAll(io.LimitReader(res.Body, MaxPDFBytes+1))
		if readErr != nil {
			return nil, fmt.Errorf("%w: read response", ErrTransient)
		}
		if len(pdf) > MaxPDFBytes || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
			return nil, fmt.Errorf("%w: invalid PDF response", ErrPermanent)
		}
		return pdf, nil
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return nil, fmt.Errorf("%w: redirect refused", ErrTransient)
	}
	var payload struct {
		Code string `json:"code"`
	}
	limited, _ := io.ReadAll(io.LimitReader(res.Body, 4097))
	_ = json.Unmarshal(limited, &payload)
	switch payload.Code {
	case "blocked":
		return nil, ErrBlocked
	case "invalid_document", "unsupported", "output_too_large":
		return nil, ErrPermanent
	case "timeout", "conversion_failed":
		return nil, ErrTransient
	default:
		if res.StatusCode >= 500 {
			return nil, ErrTransient
		}
		return nil, ErrPermanent
	}
}

// ConvertAudio re-encodes source (a non-MP3 audio attachment, or an
// audio-only WebM/MP4 voice-message recording) into real MP3 through the
// sidecar's /v1/convert-audio route. It mirrors Convert's shape exactly,
// including error classification, so the caller's retry/permanence handling
// stays the same for both.
func (c *Client) ConvertAudio(ctx context.Context, format AudioFormat, source io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(source, MaxAudioInputBytes+1))
	if err != nil || len(body) > MaxAudioInputBytes {
		return nil, fmt.Errorf("%w: read audio", ErrPermanent)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.audioEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request", ErrPermanent)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Audio-Format", string(format))
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: request failed", ErrTransient)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusOK {
		if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0])); mediaType != "audio/mpeg" {
			return nil, fmt.Errorf("%w: invalid response content type", ErrPermanent)
		}
		mp3, readErr := io.ReadAll(io.LimitReader(res.Body, MaxAudioOutputBytes+1))
		if readErr != nil {
			return nil, fmt.Errorf("%w: read response", ErrTransient)
		}
		if len(mp3) > MaxAudioOutputBytes || !validMP3Response(mp3) {
			return nil, fmt.Errorf("%w: invalid MP3 response", ErrPermanent)
		}
		return mp3, nil
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return nil, fmt.Errorf("%w: redirect refused", ErrTransient)
	}
	var payload struct {
		Code string `json:"code"`
	}
	limited, _ := io.ReadAll(io.LimitReader(res.Body, 4097))
	_ = json.Unmarshal(limited, &payload)
	switch payload.Code {
	case "blocked":
		return nil, ErrBlocked
	case "invalid_audio", "unsupported", "output_too_large":
		return nil, ErrPermanent
	case "timeout", "conversion_failed":
		return nil, ErrTransient
	default:
		if res.StatusCode >= 500 {
			return nil, ErrTransient
		}
		return nil, ErrPermanent
	}
}

// validMP3Response reports whether data begins with an ID3v2 tag or an MPEG
// audio frame sync word — the same check the sidecar itself runs on its own
// output before answering 200, repeated here so this client never treats an
// arbitrary 200 response as a real MP3.
func validMP3Response(data []byte) bool {
	if bytes.HasPrefix(data, []byte("ID3")) {
		return true
	}
	return len(data) >= 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0
}
