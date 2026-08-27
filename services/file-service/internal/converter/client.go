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
)

type Format string

const (
	FormatDOCX Format = "docx"
	FormatODT  Format = "odt"
	FormatPPT  Format = "ppt"
	FormatPPTX Format = "pptx"
)

var (
	ErrBlocked   = errors.New("converter blocked document")
	ErrPermanent = errors.New("permanent converter failure")
	ErrTransient = errors.New("transient converter failure")
)

type Client struct {
	endpoint string
	http     *http.Client
}

func NewClient(rawURL string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("DOCUMENT_CONVERTER_URL must be an absolute HTTP(S) origin")
	}
	if timeout <= 0 || timeout >= 40*time.Second {
		return nil, errors.New("document converter timeout must be positive and below 40 seconds")
	}
	return &Client{
		endpoint: strings.TrimSuffix(parsed.String(), "/") + "/v1/convert",
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
