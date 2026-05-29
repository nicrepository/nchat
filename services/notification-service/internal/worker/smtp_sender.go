package worker

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Message is the outbound email payload.
type Message struct {
	From     string
	FromName string
	To       string
	Subject  string
	TextBody string
	HTMLBody string
}

// Sender sends an email message.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// NetSMTPSender sends email via standard net/smtp.
type NetSMTPSender struct {
	Host           string
	Port           int
	Username       string
	Password       string
	from           string
	TLSMode        string
	TimeoutSeconds int
}

// NewNetSMTPSender creates a NetSMTPSender with validation.
func NewNetSMTPSender(host string, port int, username, password, from, tlsMode string, timeoutSeconds int) (*NetSMTPSender, error) {
	if host == "" {
		return nil, fmt.Errorf("SMTP host is required")
	}
	if from == "" {
		return nil, fmt.Errorf("SMTP from address is required")
	}
	if tlsMode != "tls" && tlsMode != "starttls" && tlsMode != "none" {
		return nil, fmt.Errorf("invalid TLS mode %q, must be tls, starttls, or none", tlsMode)
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 10
	}
	return &NetSMTPSender{
		Host:           host,
		Port:           port,
		Username:       username,
		Password:       password,
		from:           from,
		TLSMode:        tlsMode,
		TimeoutSeconds: timeoutSeconds,
	}, nil
}

// Send sends an email message via SMTP.
func (s *NetSMTPSender) Send(ctx context.Context, msg Message) error {
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	timeout := time.Duration(s.TimeoutSeconds) * time.Second

	// Use s.from as the envelope sender; fall back for the From header if msg.From is empty.
	if msg.From == "" {
		msg.From = s.from
	}

	// Build MIME multipart message
	body := s.buildMIMEMessage(msg)

	switch s.TLSMode {
	case "tls":
		return s.sendWithTLS(ctx, addr, timeout, s.from, msg.To, body)
	case "starttls":
		return s.sendWithStartTLS(ctx, addr, timeout, s.from, msg.To, body)
	case "none":
		return s.sendPlain(ctx, addr, timeout, s.from, msg.To, body)
	default:
		return fmt.Errorf("invalid TLS mode: %s", s.TLSMode)
	}
}

func (s *NetSMTPSender) buildMIMEMessage(msg Message) []byte {
	// Use a random boundary to prevent injection via predictable separators.
	boundary := randomBoundary()
	var body string
	body += fmt.Sprintf("From: %s <%s>\r\n", sanitizeHeader(msg.FromName), sanitizeHeader(msg.From))
	body += fmt.Sprintf("To: %s\r\n", sanitizeHeader(msg.To))
	body += fmt.Sprintf("Subject: %s\r\n", sanitizeHeader(msg.Subject))
	body += "MIME-Version: 1.0\r\n"
	body += fmt.Sprintf("Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	body += fmt.Sprintf("--%s\r\n", boundary)
	body += "Content-Type: text/plain; charset=utf-8\r\n\r\n"
	body += fmt.Sprintf("%s\r\n\r\n", msg.TextBody)
	body += fmt.Sprintf("--%s\r\n", boundary)
	body += "Content-Type: text/html; charset=utf-8\r\n\r\n"
	body += fmt.Sprintf("%s\r\n\r\n", msg.HTMLBody)
	body += fmt.Sprintf("--%s--\r\n", boundary)
	return []byte(body)
}

// sanitizeHeader strips CR, LF, and NUL characters from an email header value
// to prevent CRLF injection attacks.
func sanitizeHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\x00' {
			return -1
		}
		return r
	}, s)
}

// randomBoundary generates a 32-character hex MIME boundary using crypto/rand.
func randomBoundary() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: should never happen on any modern OS.
		return "nchat-fallback-boundary-v1"
	}
	return "nchat-" + hex.EncodeToString(b)
}

func (s *NetSMTPSender) sendWithTLS(ctx context.Context, addr string, timeout time.Duration, from, to string, body []byte) error {
	dialer := &net.Dialer{Timeout: timeout}
	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}

	tlsDialer := &tls.Dialer{NetDialer: dialer, Config: tlsConfig}
	conn, err := tlsDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer conn.Close()                        //nolint:errcheck
	conn.SetDeadline(time.Now().Add(timeout)) //nolint:errcheck

	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer client.Close() //nolint:errcheck

	if s.Username != "" {
		auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := wc.Write(body); err != nil {
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp close data writer: %w", err)
	}

	return client.Quit()
}

func (s *NetSMTPSender) sendWithStartTLS(ctx context.Context, addr string, timeout time.Duration, from, to string, body []byte) error {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()                        //nolint:errcheck
	conn.SetDeadline(time.Now().Add(timeout)) //nolint:errcheck

	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer client.Close() //nolint:errcheck

	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if err := client.StartTLS(tlsConfig); err != nil {
		return fmt.Errorf("smtp starttls: %w", err)
	}

	if s.Username != "" {
		auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := wc.Write(body); err != nil {
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp close data writer: %w", err)
	}

	return client.Quit()
}

func (s *NetSMTPSender) sendPlain(ctx context.Context, addr string, timeout time.Duration, from, to string, body []byte) error {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()                        //nolint:errcheck
	conn.SetDeadline(time.Now().Add(timeout)) //nolint:errcheck

	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer client.Close() //nolint:errcheck

	if s.Username != "" {
		auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := wc.Write(body); err != nil {
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp close data writer: %w", err)
	}

	return client.Quit()
}

// FakeSender is a test implementation of Sender.
type FakeSender struct {
	Sent        []Message
	ErrToReturn error
}

// Send records the message or returns a configured error.
func (f *FakeSender) Send(_ context.Context, msg Message) error {
	if f.ErrToReturn != nil {
		return f.ErrToReturn
	}
	f.Sent = append(f.Sent, msg)
	return nil
}
