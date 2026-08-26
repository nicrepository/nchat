package worker

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/emailcrypto"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

const (
	// One row per claim.
	//
	// It used to be ten, all leased for the same smtpWorkerDeadlineSeconds and
	// then processed sequentially, while a single send may take
	// SMTP_TIMEOUT_SECONDS (10s by default). Four slow messages therefore
	// outlived the lease that was protecting them, and a second worker — of
	// which Blue/Green guarantees there are more — could reclaim rows the first
	// was still working on. A lease has to cover the work it protects, and the
	// cheapest way to make that true is to protect one message at a time.
	smtpWorkerBatchSize = 1

	// The lease a claimed row is held under, from config so the worker, App and
	// readiness all reason about the same number.
	smtpWorkerDeadlineSeconds = config.SMTPLeaseSeconds

	// Reserved, after the send returns, for recording what happened.
	//
	// The send gets its own deadline; this is what is left of the pass budget
	// afterwards. Without it a send that used its entire timeout would leave a
	// finalisation with an already-cancelled context, the row would stay
	// unfinalised, its lease would expire, and the message would be delivered
	// again — the duplication this design exists to avoid.
	finaliseGrace = config.SMTPFinaliseGraceSeconds * time.Second
)

// Worker polls the e-mail outbox and delivers messages via SMTP.
type Worker struct {
	cfg       config.Config
	store     storage.OutboxStore
	decryptor *emailcrypto.Encryptor
	sender    Sender
	logger    *slog.Logger
}

// New creates a new SMTP worker.
func New(cfg config.Config, store storage.OutboxStore, decryptor *emailcrypto.Encryptor, sender Sender, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{cfg: cfg, store: store, decryptor: decryptor, sender: sender, logger: logger}
}

// sendTimeout is how long one SMTP exchange may take, from configuration.
func (w *Worker) sendTimeout() time.Duration {
	seconds := w.cfg.SMTPTimeoutSeconds
	if seconds <= 0 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}

// passTimeout bounds one polling pass: one send, plus the grace reserved for
// recording its outcome. With a batch size of one this is also the longest a
// shutdown can wait for work already in flight.
func (w *Worker) passTimeout() time.Duration {
	return w.sendTimeout() + finaliseGrace
}

// leaseCoversProcessing reports whether the lease outlives the work it protects.
//
// The invariant, stated once: a row must stay claimed for at least as long as
// the worker may spend on it. If configuration ever makes that false the worker
// says so rather than silently handing rows to a second worker mid-send.
func (w *Worker) leaseCoversProcessing() bool {
	return w.cfg.SMTPLeaseCoversProcessing()
}

// Start runs the SMTP worker until ctx is cancelled.
//
// Cancelling ctx stops the worker from claiming anything new; it does not abort
// a pass that is already running. That distinction is the point. An outbox row
// is claimed under a lease, sent, and only then marked finalised, so a context
// cancelled between the send and the write leaves the row unfinalised, its lease
// expiring, and the message delivered a second time on the next attempt.
//
// So a pass runs on its own bounded context rather than the caller's: shutdown
// waits for it, and pollTimeout is what stops that wait being unbounded.
// Delivery remains at-least-once — SMTP offers nothing stronger — but the
// duplication window during an ordinary shutdown is closed rather than left open.
func (w *Worker) Start(ctx context.Context) {
	pollInterval := time.Duration(w.cfg.SMTPWorkerPollSeconds) * time.Second
	if pollInterval <= 0 {
		pollInterval = 10 * time.Second
	}

	if !w.leaseCoversProcessing() {
		w.logger.Error("smtp worker lease is shorter than the work it protects",
			"lease_seconds", smtpWorkerDeadlineSeconds,
			"pass_seconds", int(w.passTimeout().Seconds()))
		return
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.drainQueue(ctx)
		}
	}
}

// drainQueue empties the backlog one claim at a time.
//
// One row per claim is what keeps a lease covering the work it protects, but
// claiming once per tick made that cost throughput: at the default ten-second
// poll a worker delivered six messages a minute regardless of how many were
// waiting. So a claim that found work is followed immediately by another, and
// only an empty queue or an error goes back to waiting for the ticker.
//
// The shutdown check is between items, never inside one: a worker asked to stop
// finishes the message it holds and then claims nothing further.
func (w *Worker) drainQueue(ctx context.Context) {
	for ctx.Err() == nil {
		if !w.pollOnce() {
			return
		}
	}
}

// pollOnce runs one pass under a context the caller's cancellation does not
// reach, so an in-flight message is finalised rather than abandoned. It reports
// whether it found work, which is what tells drainQueue to keep going.
func (w *Worker) pollOnce() bool {
	ctx, cancel := context.WithTimeout(context.Background(), w.passTimeout())
	defer cancel()
	return w.poll(ctx)
}

// poll claims and processes at most one row. It reports whether it did any
// work; a claim error reports false so the caller backs off to the ticker
// instead of retrying in a tight loop against a database that is struggling.
func (w *Worker) poll(ctx context.Context) bool {
	rows, err := w.store.ClaimBatch(ctx, w.cfg.SMTPMaxAttempts, smtpWorkerDeadlineSeconds, smtpWorkerBatchSize)
	if err != nil {
		w.logger.Error("smtp worker claim failed", "error_type", "claim_batch_failed")
		return false
	}

	for _, row := range rows {
		w.process(ctx, row)
	}
	return len(rows) > 0
}

// composed is one outbox row turned into a message ready to send.
type composed struct {
	message Message
	kind    string
}

// compose turns a claimed row into the message to deliver, or names why it
// cannot be. Separating this from delivery is what keeps each half readable:
// everything here fails for a reason intrinsic to the row and is never retried
// into success, while delivery fails for reasons that are.
func (w *Worker) compose(row storage.OutboxRow) (composed, string) {
	plaintext, err := w.decryptor.Decrypt(row.EncryptedPayload)
	if err != nil {
		return composed{kind: row.Kind}, "decrypt_error"
	}
	kind := firstNonEmpty(plaintext.Kind, row.Kind)
	toEmail := firstNonEmpty(plaintext.ToEmail, row.ToEmail)

	link, err := buildDeliveryLink(w.cfg.AuthPublicWebBaseURL, plaintext)
	if err != nil {
		return composed{kind: kind}, "render_error"
	}
	data, ok := templateDataFor(kind, link, plaintext.ExpiresAt)
	if !ok {
		return composed{kind: kind}, "render_error"
	}
	subject, textBody, htmlBody, err := RenderTemplate(kind, data)
	if err != nil {
		return composed{kind: kind}, "render_error"
	}
	return composed{kind: kind, message: Message{
		From:     w.cfg.SMTPFrom,
		FromName: w.cfg.SMTPFromName,
		To:       toEmail,
		Subject:  subject,
		TextBody: textBody,
		HTMLBody: htmlBody,
	}}, ""
}

func templateDataFor(kind, link string, expiresAt time.Time) (TemplateData, bool) {
	data := TemplateData{ExpiresAt: expiresAt}
	switch kind {
	case "password_reset":
		data.ResetLink = link
		return data, true
	case "invite":
		data.AcceptLink = link
		return data, true
	}
	return data, false
}

func firstNonEmpty(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}

func (w *Worker) process(ctx context.Context, row storage.OutboxRow) {
	built, reason := w.compose(row)
	if reason != "" {
		w.logger.Error("smtp worker "+reason, "id", row.ID, "kind", built.kind, "error_type", reason)
		w.finaliseFailure(ctx, row, row.Attempts+1, reason)
		return
	}
	if err := w.send(ctx, built.message); err != nil {
		w.logger.Error("smtp worker send failed", "id", row.ID, "kind", built.kind, "error_type", "send_error")
		w.finaliseFailure(ctx, row, row.Attempts+1, "send_error")
		return
	}
	if err := w.store.FinaliseSuccess(ctx, row.ID); err != nil {
		w.logger.Error("smtp worker finalise failed", "id", row.ID, "kind", built.kind, "error_type", "finalise_error")
		return
	}
	w.logger.Info("smtp worker delivered", "id", row.ID, "kind", built.kind)
}

// send runs the SMTP exchange under its own deadline, derived from the pass
// context but shorter by finaliseGrace.
//
// Deliberately not the pass context itself: if the send consumed the whole
// budget, the caller would then be trying to record the outcome through a
// context that had already expired. Bounding the send separately is what leaves
// the caller something to finalise with.
func (w *Worker) send(ctx context.Context, msg Message) error {
	sendCtx, cancel := context.WithTimeout(ctx, w.sendTimeout())
	defer cancel()
	return w.sender.Send(sendCtx, msg)
}

func (w *Worker) finaliseFailure(ctx context.Context, row storage.OutboxRow, attempts int, lastError string) {
	if err := w.store.FinaliseFailure(ctx, row.ID, attempts, lastError, w.cfg.SMTPBackoffSeconds, w.cfg.SMTPMaxAttempts); err != nil {
		w.logger.Error("smtp worker failure finalisation failed", "id", row.ID, "kind", row.Kind, "error_type", "finalise_failure_failed")
	}
}

func buildDeliveryLink(baseURL string, plaintext emailcrypto.Plaintext) (string, error) {
	if baseURL == "" {
		return "", fmt.Errorf("base url is required")
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}

	if plaintext.LinkPath != "" {
		rel, err := url.Parse(plaintext.LinkPath)
		if err != nil {
			return "", fmt.Errorf("parse link path: %w", err)
		}
		return base.ResolveReference(rel).String(), nil
	}
	if plaintext.ActionPath == "" {
		return "", fmt.Errorf("action path is required")
	}

	rel, err := url.Parse(plaintext.ActionPath)
	if err != nil {
		return "", fmt.Errorf("parse action path: %w", err)
	}
	query := rel.Query()
	query.Set("token", plaintext.Token)
	rel.RawQuery = query.Encode()
	return base.ResolveReference(rel).String(), nil
}
