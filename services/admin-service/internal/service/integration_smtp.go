package service

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/smtp"
	"strings"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The SMTP diagnostic and the test message (issue #582).
//
// One session serves both. A diagnostic dials, secures, authenticates and stops
// at NOOP; the test message continues into MAIL/RCPT/DATA on the same
// connection. Sharing the session is not a saving — it is what makes the two
// features describe the same relay: an operator whose diagnostic passes and
// whose test message fails learns something real, rather than comparing two
// code paths that might differ.
//
// The destination of a test message is the authenticated administrator's own
// address, taken from the session principal. It is not a request field, there
// is no allowlist to maintain and there is no "send to" input anywhere in the
// console. That single decision is the whole anti-relay control: the worst an
// attacker with a stolen administrative session can do is mail the victim.

// smtpSettings is the relay as this pod's environment describes it.
//
// Password is written to the socket and never anywhere else: it is not stored
// on a step, not wrapped in an error, not logged and not marshalled.
type smtpSettings struct {
	host     string
	port     string
	tlsMode  string
	username string
	password string
	from     string
	fromName string
}

// smtpTLSMode values, matching notification-service's own configuration so the
// diagnostic exercises the transport the platform actually uses.
const (
	smtpTLSImplicit = "tls"
	smtpTLSStartTLS = "starttls"
	smtpTLSNone     = "none"
)

// runSMTPDiagnostic exercises the relay, optionally delivering one message.
//
// deliver is nil for a plain diagnostic. When it is set, the delivery stage is
// appended to the same run, so the console renders one staged result whichever
// button was pressed.
func runSMTPDiagnostic(
	ctx context.Context,
	recorder *stageRecorder,
	policy domain.IntegrationNetworkPolicy,
	settings smtpSettings,
	deliver *smtpTestMessage,
) {
	target, err := parseIntegrationAddress(net.JoinHostPort(settings.host, settings.port), settings.tlsMode == smtpTLSImplicit)
	if err != nil {
		recorder.skip(domain.StageResolve, "O endereço configurado do relay não está no formato host:porta.")
		return
	}
	conn, ok := dialStages(ctx, recorder, policy, target)
	if !ok {
		return
	}
	client, ok := smtpClientStage(recorder, conn, settings)
	if !ok {
		return
	}
	defer func() { _ = client.Close() }()

	if !smtpTLSStage(recorder, client, settings) {
		return
	}
	if !smtpCredentialStage(recorder, client, settings) {
		return
	}
	if !smtpReadyStage(recorder, client) {
		return
	}
	if !deliverIfStillAuthorized(ctx, recorder, client, settings, deliver) {
		// The session is dropped without an envelope: the deferred Close ends
		// the connection, and the relay never learns there was a message.
		return
	}
	_ = client.Quit()
}

// deliverIfStillAuthorized performs the delivery, but only after the platform
// has re-proved the administrator's authority.
//
// This is the last safe point. Everything before it — DNS, TCP, TLS, AUTH,
// NOOP — is reversible and leaves nothing behind; MAIL/RCPT/DATA hands a
// message to a relay and cannot be taken back. So the question is asked here
// rather than only at the start of the request, where a revocation arriving
// during a slow handshake would go unnoticed.
//
// It reports whether the session may be finished normally. Nil deliver is an
// ordinary diagnostic, which delivers nothing and needs no second check.
func deliverIfStillAuthorized(
	ctx context.Context,
	recorder *stageRecorder,
	client *smtp.Client,
	settings smtpSettings,
	deliver *smtpTestMessage,
) bool {
	if deliver == nil {
		return true
	}
	if err := deliver.authorize(ctx); err != nil {
		return false
	}
	smtpDeliveryStage(recorder, client, settings, *deliver)
	return true
}

// smtpClientStage reads the greeting. A relay that accepts the connection and
// then does not introduce itself is not a relay this platform can use.
func smtpClientStage(recorder *stageRecorder, conn net.Conn, settings smtpSettings) (*smtp.Client, bool) {
	client, err := smtp.NewClient(conn, settings.host)
	if err != nil {
		_ = conn.Close()
		recorder.skip(domain.StageTLS, "Não executada: o servidor não se apresentou como um relay SMTP.")
		recorder.skip(domain.StageCredential, notExecuted)
		done := recorder.begin(domain.StageReady)
		done(domain.DiagnosticFailed, domain.HealthErrorProtocolError,
			"O servidor aceitou a conexão mas não se apresentou como um relay SMTP pronto.")
		return nil, false
	}
	return client, true
}

// smtpTLSStage secures the session according to the configured mode.
//
// The implicit mode was already handled by the staged dial, so this only
// records that it happened.
//
// The none mode is a **warning** and not a skip, and the difference decides the
// verdict of the whole run: DeriveDiagnosticStatus ignores a skipped stage, so
// recording one here would let a relay carrying this platform's invitation and
// password-reset links in clear text finish as DiagnosticPassed. A green tick
// would tell an operator that is fine. It is not a failure either — `none` is a
// mode the deployment is allowed to choose in development — so the run
// continues and reports "funciona, mas sem TLS".
func smtpTLSStage(recorder *stageRecorder, client *smtp.Client, settings smtpSettings) bool {
	switch strings.ToLower(strings.TrimSpace(settings.tlsMode)) {
	case smtpTLSImplicit:
		return true
	case smtpTLSNone:
		recorder.warn(domain.StageTLS,
			"O relay está configurado sem TLS. Convites e links de redefinição trafegam em texto claro.")
		return true
	}
	done := recorder.begin(domain.StageTLS)
	err := client.StartTLS(&tls.Config{ServerName: settings.host, MinVersion: tls.VersionTLS12})
	if err != nil {
		category, detail := classifyDialError(err)
		if category == domain.HealthErrorDependencyUnavailable {
			category, detail = domain.HealthErrorTLSError, "O relay recusou ou não completou o STARTTLS."
		}
		done(domain.DiagnosticFailed, category, detail)
		recorder.skip(domain.StageCredential, notExecuted)
		recorder.skip(domain.StageReady, notExecuted)
		return false
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone, "STARTTLS negociado e certificado validado.")
	return true
}

// smtpCredentialStage authenticates, when the deployment configured a user.
//
// net/smtp refuses PLAIN over an unencrypted connection, which is a default
// this code deliberately does not work around: a relay that only accepts a
// password in clear text is a finding, not a configuration to accommodate.
func smtpCredentialStage(recorder *stageRecorder, client *smtp.Client, settings smtpSettings) bool {
	if settings.username == "" {
		recorder.skip(domain.StageCredential, "O relay está configurado sem autenticação.")
		return true
	}
	done := recorder.begin(domain.StageCredential)
	auth := smtp.PlainAuth("", settings.username, settings.password, settings.host)
	if err := client.Auth(auth); err != nil {
		done(domain.DiagnosticFailed, domain.HealthErrorAuthenticationFailed,
			"O relay recusou a credencial que este serviço observa.")
		recorder.skip(domain.StageReady, notExecuted)
		return false
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone, "O relay aceitou a credencial.")
	return true
}

func smtpReadyStage(recorder *stageRecorder, client *smtp.Client) bool {
	done := recorder.begin(domain.StageReady)
	if err := client.Noop(); err != nil {
		done(domain.DiagnosticFailed, domain.HealthErrorDependencyUnavailable,
			"O relay parou de responder antes de se declarar pronto para envio.")
		return false
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone, "O relay está pronto para aceitar mensagens.")
	return true
}

// smtpTestMessage is the one message this service can send.
//
// There is no subject field, no body field and no destination field: all three
// are constants or come from the authenticated principal. A struct with one
// member exists so the delivery stage cannot be called without a recipient that
// was validated first.
type smtpTestMessage struct {
	recipient string
	// authorize re-proves the administrator's authority immediately before the
	// envelope is written. A non-nil answer stops the delivery, and the caller
	// turns it into an administrative refusal rather than a diagnostic result.
	authorize func(context.Context) error
}

const (
	testEmailSubject = "NChat — teste de envio do Admin Console"
	testEmailText    = "Esta mensagem foi enviada pelo Admin Console do NChat para confirmar que o relay SMTP está entregando.\r\n" +
		"Ela não contém dados da plataforma e não requer nenhuma ação.\r\n"
)

func smtpDeliveryStage(recorder *stageRecorder, client *smtp.Client, settings smtpSettings, message smtpTestMessage) {
	done := recorder.begin(domain.StageDelivery)
	if err := deliverTestMessage(client, settings, message); err != nil {
		done(domain.DiagnosticFailed, domain.HealthErrorDependencyUnavailable,
			"O relay aceitou a sessão mas recusou a mensagem de teste.")
		return
	}
	done(domain.DiagnosticPassed, domain.HealthErrorNone,
		"O relay aceitou a mensagem de teste para entrega no endereço da sua conta administrativa.")
}

// deliverTestMessage writes the envelope and the body.
//
// Every header value passes through sanitizeEmailHeader, and the recipient was
// already validated by validateTestRecipient, so there are two independent
// reasons a newline cannot reach the wire and become a second header or a
// second recipient.
func deliverTestMessage(client *smtp.Client, settings smtpSettings, message smtpTestMessage) error {
	if err := client.Mail(settings.from); err != nil {
		return err
	}
	if err := client.Rcpt(message.recipient); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(testMessageBody(settings, message.recipient)); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func testMessageBody(settings smtpSettings, recipient string) []byte {
	var body strings.Builder
	fmt.Fprintf(&body, "From: %s <%s>\r\n", sanitizeEmailHeader(settings.fromName), sanitizeEmailHeader(settings.from))
	fmt.Fprintf(&body, "To: %s\r\n", sanitizeEmailHeader(recipient))
	fmt.Fprintf(&body, "Subject: %s\r\n", testEmailSubject)
	fmt.Fprintf(&body, "Message-ID: <%s@nchat.invalid>\r\n", messageID())
	body.WriteString("MIME-Version: 1.0\r\n")
	body.WriteString("Auto-Submitted: auto-generated\r\n")
	body.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	body.WriteString(testEmailText)
	return []byte(body.String())
}

func messageID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "nchat-admin-test"
	}
	return "nchat-admin-" + hex.EncodeToString(buffer)
}

// sanitizeEmailHeader strips the characters that would end a header line.
func sanitizeEmailHeader(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, value)
}

// maxRecipientLength bounds the address a test message may be sent to. It is
// well above any real address and exists so a corrupt principal row cannot
// produce an unbounded SMTP command.
const maxRecipientLength = 254

// validateTestRecipient checks the address before it becomes an SMTP command.
//
// The address is the administrator's own, read from the database, so this is
// not input validation against a hostile caller — it is the guard that keeps a
// malformed or injected row in auth.users from turning into a second recipient
// or a forged header. It is deliberately strict rather than RFC-complete: an
// address this refuses is one an operator should fix in their profile.
func validateTestRecipient(address string) (string, error) {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" || len(trimmed) > maxRecipientLength {
		return "", fmt.Errorf("%w: administrative account has no usable email address", domain.ErrInvalidInput)
	}
	if trimmed != sanitizeEmailHeader(trimmed) || strings.ContainsAny(trimmed, " <>,;\"") {
		return "", fmt.Errorf("%w: administrative account email is malformed", domain.ErrInvalidInput)
	}
	local, host, found := strings.Cut(trimmed, "@")
	if !found || local == "" || host == "" || strings.Contains(host, "@") || !strings.Contains(host, ".") {
		return "", fmt.Errorf("%w: administrative account email is malformed", domain.ErrInvalidInput)
	}
	return trimmed, nil
}
