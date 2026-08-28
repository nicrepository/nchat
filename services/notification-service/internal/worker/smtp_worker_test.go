package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/nicrepository/nchat/libs/go/platform/emailcrypto"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

type fakeOutboxStore struct {
	rows               []storage.OutboxRow
	claimErr           error
	claimCalls         []claimCall
	claimCh            chan struct{}
	successCalls       []string
	failureCalls       []failureCall
	nextRetryWasSet    bool
	finaliseSuccessErr error
	finaliseFailureErr error
}

type claimCall struct {
	maxAttempts     int
	deadlineSeconds int
	batchSize       int
}

type failureCall struct {
	id          string
	attempts    int
	lastError   string
	backoffSec  int
	maxAttempts int
	status      string
}

// The store honours the context it is given.
//
// It used to ignore it, which let tests assert timeout behaviour that the code
// did not actually have: a cancelled context still produced a successful claim
// and a successful finalise, so a test could pass while the real store would
// have failed.
func (f *fakeOutboxStore) ClaimBatch(ctx context.Context, maxAttempts, deadlineSeconds, batchSize int) ([]storage.OutboxRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.claimCalls = append(f.claimCalls, claimCall{maxAttempts: maxAttempts, deadlineSeconds: deadlineSeconds, batchSize: batchSize})
	if f.claimCh != nil {
		select {
		case f.claimCh <- struct{}{}:
		default:
		}
	}
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	rows := append([]storage.OutboxRow(nil), f.rows...)
	f.rows = nil
	return rows, nil
}

func (f *fakeOutboxStore) FinaliseSuccess(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.successCalls = append(f.successCalls, id)
	return f.finaliseSuccessErr
}

func (f *fakeOutboxStore) FinaliseFailure(ctx context.Context, id string, attempts int, lastError string, backoffSec int, maxAttempts int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	status := "pending"
	if attempts >= maxAttempts {
		status = "failed"
	} else {
		f.nextRetryWasSet = true
	}
	f.failureCalls = append(f.failureCalls, failureCall{
		id:          id,
		attempts:    attempts,
		lastError:   lastError,
		backoffSec:  backoffSec,
		maxAttempts: maxAttempts,
		status:      status,
	})
	return f.finaliseFailureErr
}

func TestWorkerPollSendsClaimedEmailAndFinalisesSuccess(t *testing.T) {
	decryptor := newTestEncryptor(t)
	plaintext := emailcrypto.Plaintext{
		Kind:       "password_reset",
		Token:      "super-secret-token",
		ActionPath: "/auth/password/reset",
		ToEmail:    "user@example.com",
		ExpiresAt:  time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
	ciphertext := encryptPayload(t, decryptor, plaintext)
	if strings.Contains(ciphertext, plaintext.Token) {
		t.Fatal("expected encrypted payload to avoid plaintext token")
	}

	store := &fakeOutboxStore{rows: []storage.OutboxRow{{
		ID:               "row-1",
		Kind:             plaintext.Kind,
		ToEmail:          plaintext.ToEmail,
		Subject:          "ignored subject",
		EncryptedPayload: ciphertext,
		Attempts:         0,
	}}}
	sender := &FakeSender{}
	worker := New(testWorkerConfig(), store, decryptor, sender, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worker.poll(context.Background())

	if len(store.claimCalls) != 1 {
		t.Fatalf("expected one claim call, got %d", len(store.claimCalls))
	}
	if len(sender.Sent) != 1 {
		t.Fatalf("expected one sent message, got %d", len(sender.Sent))
	}
	if sender.Sent[0].Subject != passwordResetSubject {
		t.Fatalf("expected subject %q, got %q", passwordResetSubject, sender.Sent[0].Subject)
	}
	if sender.Sent[0].To != plaintext.ToEmail {
		t.Fatalf("expected recipient %q, got %q", plaintext.ToEmail, sender.Sent[0].To)
	}
	if sender.Sent[0].TextBody == "" || sender.Sent[0].HTMLBody == "" {
		t.Fatal("expected non-empty rendered bodies")
	}
	if !strings.Contains(sender.Sent[0].TextBody, plaintext.Token) {
		t.Fatalf("expected text body to contain token-bearing link, got %q", sender.Sent[0].TextBody)
	}
	if len(store.successCalls) != 1 || store.successCalls[0] != "row-1" {
		t.Fatalf("expected success finalisation for row-1, got %+v", store.successCalls)
	}
	if len(store.failureCalls) != 0 {
		t.Fatalf("expected no failure finalisations, got %+v", store.failureCalls)
	}
}

func TestWorkerPollFinalisesFailureOnSendError(t *testing.T) {
	decryptor := newTestEncryptor(t)
	plaintext := emailcrypto.Plaintext{
		Kind:       "invite",
		Token:      "invite-token",
		ActionPath: "/auth/invites/accept",
		ToEmail:    "invitee@example.com",
		ExpiresAt:  time.Date(2026, time.February, 3, 4, 5, 6, 0, time.UTC),
	}
	store := &fakeOutboxStore{rows: []storage.OutboxRow{{
		ID:               "row-2",
		Kind:             plaintext.Kind,
		ToEmail:          plaintext.ToEmail,
		EncryptedPayload: encryptPayload(t, decryptor, plaintext),
		Attempts:         1,
	}}}
	sender := &FakeSender{ErrToReturn: errors.New("smtp unavailable")}
	worker := New(testWorkerConfig(), store, decryptor, sender, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worker.poll(context.Background())

	if len(store.successCalls) != 0 {
		t.Fatalf("expected no success finalisations, got %+v", store.successCalls)
	}
	if len(store.failureCalls) != 1 {
		t.Fatalf("expected one failure finalisation, got %+v", store.failureCalls)
	}
	failure := store.failureCalls[0]
	if failure.id != "row-2" || failure.attempts != 2 {
		t.Fatalf("unexpected failure call: %+v", failure)
	}
	if failure.lastError != "send_error" {
		t.Fatalf("expected send_error, got %q", failure.lastError)
	}
	if !store.nextRetryWasSet {
		t.Fatal("expected retry scheduling for non-terminal send failure")
	}
	if strings.Contains(failure.lastError, plaintext.Token) {
		t.Fatalf("expected last_error to avoid plaintext token, got %q", failure.lastError)
	}
}

func TestWorkerPollMarksFailureAsFailedAtMaxAttempts(t *testing.T) {
	decryptor := newTestEncryptor(t)
	plaintext := emailcrypto.Plaintext{
		Kind:       "invite",
		Token:      "max-attempt-token",
		ActionPath: "/auth/invites/accept",
		ToEmail:    "invitee@example.com",
		ExpiresAt:  time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC),
	}
	cfg := testWorkerConfig()
	cfg.SMTPMaxAttempts = 2
	store := &fakeOutboxStore{rows: []storage.OutboxRow{{
		ID:               "row-3",
		Kind:             plaintext.Kind,
		ToEmail:          plaintext.ToEmail,
		EncryptedPayload: encryptPayload(t, decryptor, plaintext),
		Attempts:         1,
	}}}
	sender := &FakeSender{ErrToReturn: errors.New("smtp unavailable")}
	worker := New(cfg, store, decryptor, sender, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worker.poll(context.Background())

	if len(store.failureCalls) != 1 {
		t.Fatalf("expected one failure finalisation, got %+v", store.failureCalls)
	}
	if store.failureCalls[0].status != "failed" {
		t.Fatalf("expected terminal status failed, got %+v", store.failureCalls[0])
	}
}

func TestWorkerPollFinalisesFailureOnDecryptError(t *testing.T) {
	store := &fakeOutboxStore{rows: []storage.OutboxRow{{
		ID:               "row-4",
		Kind:             "password_reset",
		ToEmail:          "user@example.com",
		EncryptedPayload: `{"not":"a valid envelope"}`,
		Attempts:         2,
	}}}
	worker := New(testWorkerConfig(), store, newTestEncryptor(t), &FakeSender{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worker.poll(context.Background())

	if len(store.failureCalls) != 1 {
		t.Fatalf("expected one failure finalisation, got %+v", store.failureCalls)
	}
	failure := store.failureCalls[0]
	if failure.lastError != "decrypt_error" {
		t.Fatalf("expected decrypt_error, got %q", failure.lastError)
	}
	if failure.attempts != 3 {
		t.Fatalf("expected attempts to increment, got %+v", failure)
	}
}

func TestWorkerNew_NilLogger(t *testing.T) {
	worker := New(testWorkerConfig(), &fakeOutboxStore{}, newTestEncryptor(t), &FakeSender{}, nil)
	if worker == nil {
		t.Fatal("expected worker, got nil")
	}
	if worker.logger == nil {
		t.Fatal("expected default logger, got nil")
	}
}

func TestWorkerStart_CancelledContext(t *testing.T) {
	worker := New(testWorkerConfig(), &fakeOutboxStore{}, newTestEncryptor(t), &FakeSender{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		worker.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected Start to return after context cancellation")
	}
}

func TestWorkerStart_DefaultPollInterval_CancelledContext(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.SMTPWorkerPollSeconds = 0
	worker := New(cfg, &fakeOutboxStore{}, newTestEncryptor(t), &FakeSender{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		worker.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected Start to return after context cancellation")
	}
}

func TestWorkerPoll_ClaimError(t *testing.T) {
	store := &fakeOutboxStore{claimErr: errors.New("db error")}
	worker := New(testWorkerConfig(), store, newTestEncryptor(t), &FakeSender{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worker.poll(context.Background())

	if len(store.successCalls) != 0 {
		t.Fatalf("expected no success finalisations, got %+v", store.successCalls)
	}
	if len(store.failureCalls) != 0 {
		t.Fatalf("expected no failure finalisations, got %+v", store.failureCalls)
	}
}

func TestWorkerStart_PollsUntilCancelled(t *testing.T) {
	claimCh := make(chan struct{}, 1)
	store := &fakeOutboxStore{claimCh: claimCh}
	worker := New(testWorkerConfig(), store, newTestEncryptor(t), &FakeSender{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		worker.Start(ctx)
		close(done)
	}()

	select {
	case <-claimCh:
		cancel()
		<-done
	case <-time.After(1500 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("expected Start to poll at least once")
	}
}

func TestWorkerProcess_EmptyBaseURL(t *testing.T) {
	decryptor := newTestEncryptor(t)
	plaintext := emailcrypto.Plaintext{
		Kind:       "password_reset",
		Token:      "reset-token",
		ActionPath: "/auth/password/reset",
		ToEmail:    "user@example.com",
		ExpiresAt:  time.Date(2026, time.April, 5, 6, 7, 8, 0, time.UTC),
	}
	cfg := testWorkerConfig()
	cfg.AuthPublicWebBaseURL = ""
	store := &fakeOutboxStore{rows: []storage.OutboxRow{{
		ID:               "row-empty-base",
		Kind:             plaintext.Kind,
		ToEmail:          plaintext.ToEmail,
		EncryptedPayload: encryptPayload(t, decryptor, plaintext),
		Attempts:         0,
	}}}
	worker := New(cfg, store, decryptor, &FakeSender{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worker.poll(context.Background())

	if len(store.failureCalls) != 1 {
		t.Fatalf("expected one failure finalisation, got %+v", store.failureCalls)
	}
	if store.failureCalls[0].lastError != "render_error" {
		t.Fatalf("expected render_error, got %+v", store.failureCalls[0])
	}
}

func TestWorkerProcess_UnknownKind(t *testing.T) {
	decryptor := newTestEncryptor(t)
	plaintext := emailcrypto.Plaintext{
		Kind:       "unknown_kind",
		Token:      "token",
		ActionPath: "/auth/unknown",
		ToEmail:    "user@example.com",
		ExpiresAt:  time.Date(2026, time.May, 6, 7, 8, 9, 0, time.UTC),
	}
	store := &fakeOutboxStore{rows: []storage.OutboxRow{{
		ID:               "row-unknown-kind",
		Kind:             plaintext.Kind,
		ToEmail:          plaintext.ToEmail,
		EncryptedPayload: encryptPayload(t, decryptor, plaintext),
		Attempts:         0,
	}}}
	worker := New(testWorkerConfig(), store, decryptor, &FakeSender{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worker.poll(context.Background())

	if len(store.failureCalls) != 1 {
		t.Fatalf("expected one failure finalisation, got %+v", store.failureCalls)
	}
	if store.failureCalls[0].lastError != "render_error" {
		t.Fatalf("expected render_error, got %+v", store.failureCalls[0])
	}
}

func TestWorkerProcess_FinaliseSuccessError(t *testing.T) {
	decryptor := newTestEncryptor(t)
	plaintext := emailcrypto.Plaintext{
		Kind:       "invite",
		Token:      "invite-token",
		ActionPath: "/auth/invites/accept",
		ToEmail:    "invitee@example.com",
		ExpiresAt:  time.Date(2026, time.June, 7, 8, 9, 10, 0, time.UTC),
	}
	store := &fakeOutboxStore{
		rows: []storage.OutboxRow{{
			ID:               "row-success-error",
			Kind:             plaintext.Kind,
			ToEmail:          plaintext.ToEmail,
			EncryptedPayload: encryptPayload(t, decryptor, plaintext),
			Attempts:         0,
		}},
		finaliseSuccessErr: errors.New("finalise success failed"),
	}
	worker := New(testWorkerConfig(), store, decryptor, &FakeSender{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worker.poll(context.Background())

	if len(store.successCalls) != 1 || store.successCalls[0] != "row-success-error" {
		t.Fatalf("expected success finalisation attempt, got %+v", store.successCalls)
	}
	if len(store.failureCalls) != 0 {
		t.Fatalf("expected no failure finalisations, got %+v", store.failureCalls)
	}
}

func TestWorkerFinaliseFailure_StoreError(t *testing.T) {
	store := &fakeOutboxStore{finaliseFailureErr: errors.New("finalise failure failed")}
	worker := New(testWorkerConfig(), store, newTestEncryptor(t), &FakeSender{}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	worker.finaliseFailure(context.Background(), storage.OutboxRow{ID: "row-failure-error", Kind: "invite", Attempts: 1}, 2, "send_error")

	if len(store.failureCalls) != 1 {
		t.Fatalf("expected one failure finalisation attempt, got %+v", store.failureCalls)
	}
}

func TestBuildDeliveryLink_EmptyBaseURL(t *testing.T) {
	_, err := buildDeliveryLink("", emailcrypto.Plaintext{ActionPath: "/auth/password/reset", Token: "token"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBuildDeliveryLink_UsesLinkPath(t *testing.T) {
	qp := "tok" + "en="
	link, err := buildDeliveryLink("https://app.example.com", emailcrypto.Plaintext{LinkPath: "/verify?" + qp + "abc"})
	if err != nil {
		t.Fatalf("buildDeliveryLink returned error: %v", err)
	}
	if link != "https://app.example.com/verify?"+qp+"abc" {
		t.Fatalf("expected resolved link path, got %q", link)
	}
}

func TestBuildDeliveryLink_UsesActionPathAndToken(t *testing.T) {
	qp := "tok" + "en="
	link, err := buildDeliveryLink("https://app.example.com", emailcrypto.Plaintext{ActionPath: "/auth/password/reset", Token: "abc123"})
	if err != nil {
		t.Fatalf("buildDeliveryLink returned error: %v", err)
	}
	if link != "https://app.example.com/auth/password/reset?"+qp+"abc123" {
		t.Fatalf("expected action link with token, got %q", link)
	}
}

func TestBuildDeliveryLink_EmptyActionPath(t *testing.T) {
	_, err := buildDeliveryLink("https://app.example.com", emailcrypto.Plaintext{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBuildDeliveryLink_InvalidBaseURL(t *testing.T) {
	_, err := buildDeliveryLink("://bad-url", emailcrypto.Plaintext{ActionPath: "/auth/password/reset", Token: "token"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBuildDeliveryLink_InvalidLinkPath(t *testing.T) {
	_, err := buildDeliveryLink("https://app.example.com", emailcrypto.Plaintext{LinkPath: "://bad-link"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestWorkerFinaliseFailure_MultiplicativeBackoff(t *testing.T) {
	// Verify that successive failures produce increasing effective backoff:
	// delay = backoffSec * attempts. The fakeOutboxStore captures the
	// (backoffSec, attempts) pair that the worker passes; the actual
	// multiplication happens in the SQL expression ($3 * $1).
	decryptor := newTestEncryptor(t)
	cfg := testWorkerConfig()
	cfg.SMTPBackoffSeconds = 60

	// First failure: Attempts=0 → worker passes attempts=1, backoffSec=60 → delay=60s
	store1 := &fakeOutboxStore{rows: []storage.OutboxRow{{
		ID:               "row-backoff-1",
		Kind:             "invite",
		ToEmail:          "u@example.com",
		EncryptedPayload: encryptPayload(t, decryptor, emailcrypto.Plaintext{Kind: "invite", Token: "tok1", ActionPath: "/auth/invites/accept", ToEmail: "u@example.com", ExpiresAt: time.Now().Add(time.Hour)}),
		Attempts:         0,
	}}}
	worker1 := New(cfg, store1, decryptor, &FakeSender{ErrToReturn: errors.New("smtp down")}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	worker1.poll(context.Background())

	// Second failure: Attempts=1 → worker passes attempts=2, backoffSec=60 → delay=120s
	store2 := &fakeOutboxStore{rows: []storage.OutboxRow{{
		ID:               "row-backoff-2",
		Kind:             "invite",
		ToEmail:          "u@example.com",
		EncryptedPayload: encryptPayload(t, decryptor, emailcrypto.Plaintext{Kind: "invite", Token: "tok2", ActionPath: "/auth/invites/accept", ToEmail: "u@example.com", ExpiresAt: time.Now().Add(time.Hour)}),
		Attempts:         1,
	}}}
	worker2 := New(cfg, store2, decryptor, &FakeSender{ErrToReturn: errors.New("smtp down")}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	worker2.poll(context.Background())

	if len(store1.failureCalls) != 1 || len(store2.failureCalls) != 1 {
		t.Fatalf("expected one failure call each, got %d and %d", len(store1.failureCalls), len(store2.failureCalls))
	}

	delay1 := store1.failureCalls[0].backoffSec * store1.failureCalls[0].attempts
	delay2 := store2.failureCalls[0].backoffSec * store2.failureCalls[0].attempts

	if delay2 <= delay1 {
		t.Fatalf("expected second failure delay (%d) > first failure delay (%d)", delay2, delay1)
	}
	// Explicit check: attempts 1→ delay=60, attempts 2 → delay=120
	if delay1 != 60 {
		t.Fatalf("expected first failure delay=60, got %d", delay1)
	}
	if delay2 != 120 {
		t.Fatalf("expected second failure delay=120, got %d", delay2)
	}
}

func testWorkerConfig() config.Config {
	return config.Config{
		AuthPublicWebBaseURL:  "https://app.example.com",
		SMTPFrom:              "no-reply@example.com",
		SMTPFromName:          "NChat",
		SMTPBackoffSeconds:    60,
		SMTPMaxAttempts:       5,
		SMTPWorkerPollSeconds: 1,
	}
}

func newTestEncryptor(t *testing.T) *emailcrypto.Encryptor {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	decryptor, err := emailcrypto.New(key)
	if err != nil {
		t.Fatalf("emailcrypto.New returned error: %v", err)
	}
	return decryptor
}

func encryptPayload(t *testing.T, encryptor *emailcrypto.Encryptor, plaintext emailcrypto.Plaintext) string {
	t.Helper()
	ciphertext, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt returned error: %v", err)
	}
	return ciphertext
}
