package worker

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFakeSender_RecordsMessage(t *testing.T) {
	fake := &FakeSender{}
	msg := Message{
		From:     "test@example.com",
		FromName: "Test",
		To:       "recipient@example.com",
		Subject:  "Test Subject",
		TextBody: "Test text",
		HTMLBody: "<p>Test HTML</p>",
	}

	err := fake.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(fake.Sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(fake.Sent))
	}

	sent := fake.Sent[0]
	if sent.From != msg.From || sent.To != msg.To || sent.Subject != msg.Subject {
		t.Fatalf("message mismatch: %+v", sent)
	}
}

func TestFakeSender_ReturnsConfiguredError(t *testing.T) {
	expectedErr := errors.New("test error")
	fake := &FakeSender{ErrToReturn: expectedErr}

	msg := Message{From: "test@example.com", To: "recipient@example.com"}
	err := fake.Send(context.Background(), msg)

	if err != expectedErr {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}

	if len(fake.Sent) != 0 {
		t.Fatalf("expected no messages recorded on error, got %d", len(fake.Sent))
	}
}

func TestNewNetSMTPSender_ValidatesHost(t *testing.T) {
	_, err := NewNetSMTPSender("", 587, "", "", "test@example.com", "starttls", 10)
	if err == nil || err.Error() != "SMTP host is required" {
		t.Fatalf("expected host validation error, got %v", err)
	}
}

func TestNewNetSMTPSender_ValidatesFrom(t *testing.T) {
	_, err := NewNetSMTPSender("smtp.example.com", 587, "", "", "", "starttls", 10)
	if err == nil || err.Error() != "SMTP from address is required" {
		t.Fatalf("expected from validation error, got %v", err)
	}
}

func TestNewNetSMTPSender_ValidatesTLSMode(t *testing.T) {
	_, err := NewNetSMTPSender("smtp.example.com", 587, "", "", "test@example.com", "invalid", 10)
	if err == nil {
		t.Fatalf("expected TLS mode validation error, got nil")
	}
	if err.Error() != "invalid TLS mode \"invalid\", must be tls, starttls, or none" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNewNetSMTPSender_Success(t *testing.T) {
	sender, err := NewNetSMTPSender("smtp.example.com", 587, "user", "pass", "test@example.com", "starttls", 10)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if sender.Host != "smtp.example.com" {
		t.Fatalf("expected host smtp.example.com, got %s", sender.Host)
	}
	if sender.Port != 587 {
		t.Fatalf("expected port 587, got %d", sender.Port)
	}
	if sender.TLSMode != "starttls" {
		t.Fatalf("expected TLS mode starttls, got %s", sender.TLSMode)
	}
}

func TestNewNetSMTPSender_DefaultsTimeout(t *testing.T) {
	sender, err := NewNetSMTPSender("smtp.example.com", 587, "", "", "test@example.com", "none", 0)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if sender.TimeoutSeconds != 10 {
		t.Fatalf("expected default timeout 10, got %d", sender.TimeoutSeconds)
	}
}

func TestNetSMTPSender_buildMIMEMessage(t *testing.T) {
	sender := &NetSMTPSender{}
	message := Message{
		From:     "sender@example.com",
		FromName: "Sender Name",
		To:       "recipient@example.com",
		Subject:  "Welcome",
		TextBody: "plain body",
		HTMLBody: "<p>html body</p>",
	}

	body := string(sender.buildMIMEMessage(message))

	checks := []string{
		"From: Sender Name <sender@example.com>",
		"To: recipient@example.com",
		"Subject: Welcome",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=",
		"Content-Type: text/plain; charset=utf-8",
		"plain body",
		"Content-Type: text/html; charset=utf-8",
		"<p>html body</p>",
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Fatalf("expected MIME body to contain %q, got %q", check, body)
		}
	}
}

func TestNetSMTPSender_buildMIMEMessage_RandomBoundaryChanges(t *testing.T) {
	sender := &NetSMTPSender{}
	msg := Message{From: "a@b.com", FromName: "A", To: "b@c.com", Subject: "S", TextBody: "T", HTMLBody: "H"}

	boundary1 := extractBoundary(string(sender.buildMIMEMessage(msg)))
	boundary2 := extractBoundary(string(sender.buildMIMEMessage(msg)))
	if boundary1 == "" || boundary2 == "" {
		t.Fatal("boundary should not be empty")
	}
	if boundary1 == boundary2 {
		t.Fatalf("expected random boundary to differ between calls, got %q twice", boundary1)
	}
}

func extractBoundary(mimeMsg string) string {
	for _, line := range strings.Split(mimeMsg, "\r\n") {
		if strings.HasPrefix(line, "Content-Type: multipart/alternative; boundary=") {
			// boundary is quoted: boundary="value"
			start := strings.Index(line, `"`)
			end := strings.LastIndex(line, `"`)
			if start >= 0 && end > start {
				return line[start+1 : end]
			}
		}
	}
	return ""
}

func TestNetSMTPSender_buildMIMEMessage_CRLFInjectionInSubject(t *testing.T) {
	sender := &NetSMTPSender{}
	msg := Message{
		From:     "sender@example.com",
		FromName: "Sender",
		To:       "victim@example.com",
		Subject:  "Hello\r\nX-Evil: injected",
		TextBody: "text",
		HTMLBody: "html",
	}
	body := string(sender.buildMIMEMessage(msg))
	// Injected header must not appear as a separate line (CRLF + header name).
	if strings.Contains(body, "\r\nX-Evil:") || strings.HasPrefix(body, "X-Evil:") {
		t.Fatalf("CRLF injection in Subject created a header line, got:\n%s", body)
	}
	if !strings.Contains(body, "Subject:") {
		t.Fatalf("expected Subject header in output, got:\n%s", body)
	}
}

func TestNetSMTPSender_buildMIMEMessage_CRLFInjectionInTo(t *testing.T) {
	sender := &NetSMTPSender{}
	msg := Message{
		From:     "sender@example.com",
		FromName: "Sender",
		To:       "victim@example.com\r\nBcc: attacker@evil.com",
		Subject:  "Test",
		TextBody: "text",
		HTMLBody: "html",
	}
	body := string(sender.buildMIMEMessage(msg))
	// Injected Bcc header must not appear as a separate line.
	if strings.Contains(body, "\r\nBcc:") || strings.HasPrefix(body, "Bcc:") {
		t.Fatalf("CRLF injection in To created a Bcc header line, got:\n%s", body)
	}
}

func TestNetSMTPSender_buildMIMEMessage_CRLFInjectionInFromName(t *testing.T) {
	sender := &NetSMTPSender{}
	msg := Message{
		From:     "sender@example.com",
		FromName: "Evil\r\nX-Injected: yes",
		To:       "victim@example.com",
		Subject:  "Test",
		TextBody: "text",
		HTMLBody: "html",
	}
	body := string(sender.buildMIMEMessage(msg))
	// Injected header must not appear as a separate line.
	if strings.Contains(body, "\r\nX-Injected:") || strings.HasPrefix(body, "X-Injected:") {
		t.Fatalf("CRLF injection in FromName created a header line, got:\n%s", body)
	}
}

func TestSanitizeHeader_StripsControlChars(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"hello\r\nworld", "helloworld"},
		{"test\x00value", "testvalue"},
		{"clean subject", "clean subject"},
		{"\r\n\x00", ""},
	}
	for _, tc := range cases {
		got := sanitizeHeader(tc.input)
		if got != tc.want {
			t.Fatalf("sanitizeHeader(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNetSMTPSender_Send_InvalidTLSMode(t *testing.T) {
	sender := &NetSMTPSender{TLSMode: "invalid", from: "sender@example.com"}

	err := sender.Send(context.Background(), Message{To: "recipient@example.com", Subject: "Subject"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid TLS mode") {
		t.Fatalf("expected invalid TLS mode error, got %v", err)
	}
}

func TestNetSMTPSender_Send_TLSDialError(t *testing.T) {
	sender := &NetSMTPSender{Host: "127.0.0.1", Port: 1, TLSMode: "tls", TimeoutSeconds: 1, from: "sender@example.com"}

	err := sender.Send(context.Background(), Message{To: "recipient@example.com", Subject: "Subject"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tls dial") {
		t.Fatalf("expected tls dial error, got %v", err)
	}
}

func TestNetSMTPSender_Send_StartTLSDialError(t *testing.T) {
	sender := &NetSMTPSender{Host: "127.0.0.1", Port: 1, TLSMode: "starttls", TimeoutSeconds: 1, from: "sender@example.com"}

	err := sender.Send(context.Background(), Message{To: "recipient@example.com", Subject: "Subject"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "dial") {
		t.Fatalf("expected dial error, got %v", err)
	}
}

func TestNetSMTPSender_Send_PlainDialError(t *testing.T) {
	sender := &NetSMTPSender{Host: "127.0.0.1", Port: 1, TLSMode: "none", TimeoutSeconds: 1, from: "sender@example.com"}

	err := sender.Send(context.Background(), Message{To: "recipient@example.com", Subject: "Subject"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "dial") {
		t.Fatalf("expected dial error, got %v", err)
	}
}

func TestNetSMTPSender_Send_PlainSuccess(t *testing.T) {
	server := startSMTPTestServer(t, smtpTestServerOptions{auth: true})
	sender := &NetSMTPSender{
		Host:           server.host,
		Port:           server.port,
		Username:       "user",
		Password:       "pass",
		TLSMode:        "none",
		TimeoutSeconds: 1,
		from:           "sender@example.com",
	}

	err := sender.Send(context.Background(), Message{
		To:       "recipient@example.com",
		Subject:  "Subject",
		TextBody: "plain body",
		HTMLBody: "<p>html body</p>",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !strings.Contains(server.message(), "Subject: Subject") {
		t.Fatalf("expected captured message to contain subject, got %q", server.message())
	}
}

func TestNetSMTPSender_Send_StartTLSSuccess(t *testing.T) {
	server := startSMTPTestServer(t, smtpTestServerOptions{startTLS: true, auth: true})
	sender := &NetSMTPSender{
		Host:           server.host,
		Port:           server.port,
		Username:       "user",
		Password:       "pass",
		TLSMode:        "starttls",
		TimeoutSeconds: 1,
		from:           "sender@example.com",
	}

	err := sender.Send(context.Background(), Message{
		To:       "recipient@example.com",
		Subject:  "Subject",
		TextBody: "plain body",
		HTMLBody: "<p>html body</p>",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !strings.Contains(server.message(), "Subject: Subject") {
		t.Fatalf("expected captured message to contain subject, got %q", server.message())
	}
}

func TestNetSMTPSender_Send_TLSSuccess(t *testing.T) {
	server := startSMTPTestServer(t, smtpTestServerOptions{implicitTLS: true, auth: true})
	sender := &NetSMTPSender{
		Host:           server.host,
		Port:           server.port,
		Username:       "user",
		Password:       "pass",
		TLSMode:        "tls",
		TimeoutSeconds: 1,
		from:           "sender@example.com",
	}

	err := sender.Send(context.Background(), Message{
		To:       "recipient@example.com",
		Subject:  "Subject",
		TextBody: "plain body",
		HTMLBody: "<p>html body</p>",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !strings.Contains(server.message(), "Subject: Subject") {
		t.Fatalf("expected captured message to contain subject, got %q", server.message())
	}
}

func TestNetSMTPSender_Send_PlainAuthError(t *testing.T) {
	server := startSMTPTestServer(t, smtpTestServerOptions{auth: true, authError: true})
	sender := &NetSMTPSender{
		Host:           server.host,
		Port:           server.port,
		Username:       "user",
		Password:       "pass",
		TLSMode:        "none",
		TimeoutSeconds: 1,
		from:           "sender@example.com",
	}

	err := sender.Send(context.Background(), Message{To: "recipient@example.com", Subject: "Subject"})
	if err == nil || !strings.Contains(err.Error(), "smtp auth") {
		t.Fatalf("expected smtp auth error, got %v", err)
	}
}

func TestNetSMTPSender_Send_StartTLSRejected(t *testing.T) {
	server := startSMTPTestServer(t, smtpTestServerOptions{startTLS: true, rejectStartTLS: true})
	sender := &NetSMTPSender{
		Host:           server.host,
		Port:           server.port,
		TLSMode:        "starttls",
		TimeoutSeconds: 1,
		from:           "sender@example.com",
	}

	err := sender.Send(context.Background(), Message{To: "recipient@example.com", Subject: "Subject"})
	if err == nil || !strings.Contains(err.Error(), "smtp starttls") {
		t.Fatalf("expected smtp starttls error, got %v", err)
	}
}

func TestNetSMTPSender_Send_TLSAuthError(t *testing.T) {
	server := startSMTPTestServer(t, smtpTestServerOptions{implicitTLS: true, auth: true, authError: true})
	sender := &NetSMTPSender{
		Host:           server.host,
		Port:           server.port,
		Username:       "user",
		Password:       "pass",
		TLSMode:        "tls",
		TimeoutSeconds: 1,
		from:           "sender@example.com",
	}

	err := sender.Send(context.Background(), Message{To: "recipient@example.com", Subject: "Subject"})
	if err == nil || !strings.Contains(err.Error(), "smtp auth") {
		t.Fatalf("expected smtp auth error, got %v", err)
	}
}

func TestNetSMTPSender_Send_PlainMailError(t *testing.T) {
	server := startSMTPTestServer(t, smtpTestServerOptions{mailError: true})
	sender := &NetSMTPSender{
		Host:           server.host,
		Port:           server.port,
		TLSMode:        "none",
		TimeoutSeconds: 1,
		from:           "sender@example.com",
	}

	err := sender.Send(context.Background(), Message{To: "recipient@example.com", Subject: "Subject"})
	if err == nil || !strings.Contains(err.Error(), "smtp mail") {
		t.Fatalf("expected smtp mail error, got %v", err)
	}
}

type smtpTestServerOptions struct {
	implicitTLS    bool
	startTLS       bool
	auth           bool
	closeOnAccept  bool
	rejectStartTLS bool
	authError      bool
	mailError      bool
}

// generateTestTLSFiles creates a self-signed certificate and key in dir and
// returns the cert and key file paths. The cert is valid for "localhost".
func generateTestTLSFiles(dir string) (certFile, keyFile string, err error) {
	key, rsaErr := rsa.GenerateKey(rand.Reader, 2048)
	if rsaErr != nil {
		return "", "", fmt.Errorf("generate RSA key: %w", rsaErr)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	certDER, certErr := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if certErr != nil {
		return "", "", fmt.Errorf("create certificate: %w", certErr)
	}

	certFile = dir + "/cert.pem"
	cf, cfErr := os.Create(certFile) //nolint:gosec // test helper with caller-controlled path
	if cfErr != nil {
		return "", "", fmt.Errorf("create cert file: %w", cfErr)
	}
	encCertErr := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	_ = cf.Close()
	if encCertErr != nil {
		return "", "", fmt.Errorf("encode cert PEM: %w", encCertErr)
	}

	keyFile = dir + "/key.pem"
	kf, kfErr := os.Create(keyFile) //nolint:gosec // test helper with caller-controlled path
	if kfErr != nil {
		return "", "", fmt.Errorf("create key file: %w", kfErr)
	}
	encKeyErr := pem.Encode(kf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	_ = kf.Close()
	if encKeyErr != nil {
		return "", "", fmt.Errorf("encode key PEM: %w", encKeyErr)
	}

	return certFile, keyFile, nil
}

// testCertFile and testKeyFile hold the paths to the shared TLS test cert/key
// generated once by TestMain, used by all TLS tests in this package.
var testCertFile string
var testKeyFile string

func TestMain(m *testing.M) {
	dir, mkdirErr := os.MkdirTemp("", "smtp-tls-test-*")
	if mkdirErr != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", mkdirErr)
		os.Exit(1)
	}

	var genErr error
	testCertFile, testKeyFile, genErr = generateTestTLSFiles(dir)
	if genErr != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "generate TLS files: %v\n", genErr)
		os.Exit(1)
	}
	// Set once so the Go system cert pool (loaded lazily on first TLS dial)
	// trusts the self-signed test cert across all tests.
	if setErr := os.Setenv("SSL_CERT_FILE", testCertFile); setErr != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "set SSL_CERT_FILE: %v\n", setErr)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type smtpTestServer struct {
	host string
	port int

	mu      sync.Mutex
	body    string
	err     error
	done    chan struct{}
	closeFn func() error
}

func startSMTPTestServer(t *testing.T, opts smtpTestServerOptions) *smtpTestServer {
	t.Helper()

	cert, err := tls.LoadX509KeyPair(testCertFile, testKeyFile)
	if err != nil {
		t.Fatalf("load test certificate: %v", err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}

	var ln net.Listener
	if opts.implicitTLS {
		ln, err = tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	} else {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &smtpTestServer{
		host:    "localhost",
		port:    ln.Addr().(*net.TCPAddr).Port,
		done:    make(chan struct{}),
		closeFn: ln.Close,
	}

	go func() {
		defer close(server.done)
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			server.err = acceptErr
			return
		}
		defer conn.Close() //nolint:errcheck
		server.err = server.handleConn(conn, tlsConfig, opts)
	}()

	t.Cleanup(func() {
		_ = server.closeFn()
		<-server.done
		if server.err != nil && !errors.Is(server.err, net.ErrClosed) && !strings.Contains(server.err.Error(), "use of closed network connection") {
			t.Fatalf("smtp test server failed: %v", server.err)
		}
	})

	return server
}

func (s *smtpTestServer) message() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body
}

func (s *smtpTestServer) setMessage(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = body
}

func (s *smtpTestServer) handleConn(conn net.Conn, tlsConfig *tls.Config, opts smtpTestServerOptions) error {
	if opts.closeOnAccept {
		return nil
	}
	if _, err := io.WriteString(conn, "220 localhost ESMTP\r\n"); err != nil {
		return err
	}

	reader := textproto.NewReader(bufio.NewReader(conn))
	for {
		line, err := reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO "), strings.HasPrefix(upper, "HELO "):
			if opts.startTLS && !isTLSConn(conn) {
				if err := writeSMTPResponse(conn, "250-localhost\r\n250-STARTTLS\r\n250 OK\r\n"); err != nil {
					return err
				}
				continue
			}
			if opts.auth {
				if err := writeSMTPResponse(conn, "250-localhost\r\n250-AUTH PLAIN\r\n250 OK\r\n"); err != nil {
					return err
				}
				continue
			}
			if err := writeSMTPResponse(conn, "250-localhost\r\n250 OK\r\n"); err != nil {
				return err
			}
		case upper == "STARTTLS":
			if opts.rejectStartTLS {
				return writeSMTPResponse(conn, "454 TLS not available\r\n")
			}
			if err := writeSMTPResponse(conn, "220 Ready to start TLS\r\n"); err != nil {
				return err
			}
			tlsConn := tls.Server(conn, tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return err
			}
			conn = tlsConn
			reader = textproto.NewReader(bufio.NewReader(conn))
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			if opts.authError {
				return writeSMTPResponse(conn, "535 Authentication failed\r\n")
			}
			if err := writeSMTPResponse(conn, "235 Authentication successful\r\n"); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			if opts.mailError {
				return writeSMTPResponse(conn, "550 Mailbox unavailable\r\n")
			}
			if err := writeSMTPResponse(conn, "250 OK\r\n"); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "RCPT TO:"):
			if err := writeSMTPResponse(conn, "250 OK\r\n"); err != nil {
				return err
			}
		case upper == "DATA":
			if err := writeSMTPResponse(conn, "354 End data with <CR><LF>.<CR><LF>\r\n"); err != nil {
				return err
			}
			body, err := io.ReadAll(reader.DotReader())
			if err != nil {
				return err
			}
			s.setMessage(string(body))
			if err := writeSMTPResponse(conn, "250 OK\r\n"); err != nil {
				return err
			}
		case upper == "QUIT":
			return writeSMTPResponse(conn, "221 Bye\r\n")
		default:
			return fmt.Errorf("unexpected SMTP command: %q", line)
		}
	}
}

func writeSMTPResponse(conn net.Conn, response string) error {
	_, err := io.WriteString(conn, response)
	return err
}

func isTLSConn(conn net.Conn) bool {
	_, ok := conn.(*tls.Conn)
	return ok
}
