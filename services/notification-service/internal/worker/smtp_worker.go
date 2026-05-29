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
	smtpWorkerBatchSize       = 10
	smtpWorkerDeadlineSeconds = 30
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

// Start runs the SMTP worker until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	pollInterval := time.Duration(w.cfg.SMTPWorkerPollSeconds) * time.Second
	if pollInterval <= 0 {
		pollInterval = 10 * time.Second
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

func (w *Worker) poll(ctx context.Context) {
	rows, err := w.store.ClaimBatch(ctx, w.cfg.SMTPMaxAttempts, smtpWorkerDeadlineSeconds, smtpWorkerBatchSize)
	if err != nil {
		w.logger.Error("smtp worker claim failed", "error_type", "claim_batch_failed")
		return
	}

	for _, row := range rows {
		w.process(ctx, row)
	}
}

func (w *Worker) process(ctx context.Context, row storage.OutboxRow) {
	plaintext, err := w.decryptor.Decrypt(row.EncryptedPayload)
	if err != nil {
		w.logger.Error("smtp worker decrypt failed", "id", row.ID, "kind", row.Kind, "error_type", "decrypt_error")
		w.finaliseFailure(ctx, row, row.Attempts+1, "decrypt_error")
		return
	}

	kind := plaintext.Kind
	if kind == "" {
		kind = row.Kind
	}
	toEmail := plaintext.ToEmail
	if toEmail == "" {
		toEmail = row.ToEmail
	}

	link, err := buildDeliveryLink(w.cfg.AuthPublicWebBaseURL, plaintext)
	if err != nil {
		w.logger.Error("smtp worker render failed", "id", row.ID, "kind", kind, "error_type", "render_error")
		w.finaliseFailure(ctx, row, row.Attempts+1, "render_error")
		return
	}

	data := TemplateData{ExpiresAt: plaintext.ExpiresAt}
	switch kind {
	case "password_reset":
		data.ResetLink = link
	case "invite":
		data.AcceptLink = link
	default:
		w.logger.Error("smtp worker render failed", "id", row.ID, "kind", kind, "error_type", "render_error")
		w.finaliseFailure(ctx, row, row.Attempts+1, "render_error")
		return
	}

	subject, textBody, htmlBody, err := RenderTemplate(kind, data)
	if err != nil {
		w.logger.Error("smtp worker render failed", "id", row.ID, "kind", kind, "error_type", "render_error")
		w.finaliseFailure(ctx, row, row.Attempts+1, "render_error")
		return
	}

	if err := w.sender.Send(ctx, Message{
		From:     w.cfg.SMTPFrom,
		FromName: w.cfg.SMTPFromName,
		To:       toEmail,
		Subject:  subject,
		TextBody: textBody,
		HTMLBody: htmlBody,
	}); err != nil {
		w.logger.Error("smtp worker send failed", "id", row.ID, "kind", kind, "error_type", "send_error")
		w.finaliseFailure(ctx, row, row.Attempts+1, "send_error")
		return
	}

	if err := w.store.FinaliseSuccess(ctx, row.ID); err != nil {
		w.logger.Error("smtp worker success finalisation failed", "id", row.ID, "kind", kind, "error_type", "finalise_success_failed")
	}
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
