package filepolicy

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// Only single-part UTF-8/ASCII plaintext EML is supported. Multipart, embedded
// attachments, HTML and opaque transfer encodings require a separate inspector.
func inspectEmail(ctx context.Context, source io.Reader, size int64) error {
	if size > MaxTextBytes {
		return errBudget
	}
	b, err := io.ReadAll(io.LimitReader(contextReader{ctx, source}, MaxTextBytes+1))
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return errStructure
	}
	m, err := mail.ReadMessage(bytes.NewReader(b))
	if err != nil {
		return errStructure
	}
	if err = mailHeaders(m.Header); err != nil {
		return err
	}
	body, err := mailBody(m)
	if err != nil {
		return err
	}
	decoded, err := io.ReadAll(io.LimitReader(contextReader{ctx, body}, MaxTextBytes+1))
	if err != nil {
		return errStructure
	}
	if len(decoded) == 0 || !passiveText(decoded) {
		return errActive
	}
	return nil
}

func mailHeaders(h mail.Header) error {
	if err := mailAddresses(h); err != nil {
		return err
	}
	if len(h["Date"]) != 1 || len(h["Content-Type"]) != 1 {
		return errStructure
	}
	if _, err := mail.ParseDate(h.Get("Date")); err != nil {
		return errStructure
	}
	t, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		return errStructure
	}
	if t != "text/plain" {
		return errOpaque
	}
	charset := strings.ToLower(params["charset"])
	if charset != "" && charset != "utf-8" && charset != "us-ascii" {
		return errOpaque
	}
	return nil
}

func mailBody(m *mail.Message) (io.Reader, error) {
	if len(m.Header["Content-Transfer-Encoding"]) > 1 {
		return nil, errStructure
	}
	switch strings.ToLower(m.Header.Get("Content-Transfer-Encoding")) {
	case "", "7bit", "8bit":
		return m.Body, nil
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, m.Body), nil
	case "quoted-printable":
		return quotedprintable.NewReader(m.Body), nil
	default:
		return nil, errOpaque
	}
}

func mailAddresses(h mail.Header) error {
	for _, name := range []string{"From", "To"} {
		if len(h[name]) != 1 {
			return errStructure
		}
		if _, err := mail.ParseAddressList(h.Get(name)); err != nil {
			return errStructure
		}
	}
	return nil
}
