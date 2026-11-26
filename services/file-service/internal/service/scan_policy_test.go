package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
)

func completeScanInspection(t *testing.T) scanInspection {
	t.Helper()
	policy, err := filepolicy.Inspect(context.Background(), strings.NewReader("test"), 4, "notes.txt", "text/plain", filepolicy.Profiles{})
	if err != nil {
		t.Fatal(err)
	}
	return scanInspection{policy: policy, scanned: true, verdict: scanner.VerdictClean, readComplete: true}
}

func TestScanInspectionRequiresEveryEvidence(t *testing.T) {
	complete := completeScanInspection(t)
	for _, tc := range []struct {
		name   string
		change func(*scanInspection)
		want   error
	}{
		{"complete", func(*scanInspection) {}, nil},
		{"missing policy", func(r *scanInspection) { r.policy = filepolicy.Result{} }, errFutureScanPolicy},
		{"future policy", func(r *scanInspection) { r.policy.Version++ }, errFutureScanPolicy},
		{"policy denied", func(r *scanInspection) { r.policy.Decision = filepolicy.Reject }, errScanPolicy},
		{"policy unknown", func(r *scanInspection) { r.policy.Decision = "unknown" }, errScanPolicy},
		{"policy inconclusive", func(r *scanInspection) { r.policy.Decision = filepolicy.Indeterminate }, errScanPolicy},
		{"structural limit", func(r *scanInspection) { r.policy.Code = filepolicy.BudgetExceeded }, errScanPolicy},
		{"structural invalid", func(r *scanInspection) { r.policy.Code = filepolicy.Invalid }, errScanPolicy},
		{"structural unsupported", func(r *scanInspection) { r.policy.Code = filepolicy.Unsupported }, errScanPolicy},
		{"missing inspector", func(r *scanInspection) { r.policy.Format.Strategy = "" }, errScanPolicy},
		{"unsupported inspector", func(r *scanInspection) { r.policy.Format.Strategy = filepolicy.Unavailable }, errScanPolicy},
		{"unknown format", func(r *scanInspection) { r.policy.Format.ID = filepolicy.IDUnknown }, errScanPolicy},
		{"missing format", func(r *scanInspection) { r.policy.Format.ID = "" }, errScanPolicy},
		{"mime mismatch", func(r *scanInspection) { r.policy.MIMEMismatch = true }, errScanPolicy},
		{"missing AV", func(r *scanInspection) { r.scanned = false }, scanner.ErrProtocol},
		{"unknown AV", func(r *scanInspection) { r.verdict = scanner.Verdict(99) }, scanner.ErrProtocol},
		{"incomplete read", func(r *scanInspection) { r.readComplete = false }, scanner.ErrProtocol},
		{"decrypt failed", func(r *scanInspection) { r.err = crypto.ErrCiphertext }, crypto.ErrCiphertext},
		{"scanner unavailable", func(r *scanInspection) { r.err = scanner.ErrProtocol }, scanner.ErrProtocol},
		{"scan limit", func(r *scanInspection) { r.err = scanner.ErrStreamTooLarge }, scanner.ErrStreamTooLarge},
		{"timeout", func(r *scanInspection) { r.err = context.DeadlineExceeded }, context.DeadlineExceeded},
		{"cancel", func(r *scanInspection) { r.err = context.Canceled }, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := complete
			tc.change(&r)
			verdict, err := r.decision(context.Background(), filepolicy.Version)
			if !errors.Is(err, tc.want) || tc.want != nil && verdict == scanner.VerdictClean {
				t.Fatalf("decision=%v/%v, want error %v", verdict, err, tc.want)
			}
		})
	}
}

func TestScanInspectionVersionCompatibility(t *testing.T) {
	complete := completeScanInspection(t)
	for _, version := range []int{0, filepolicy.Version, filepolicy.Version + 1} {
		verdict, err := complete.decision(context.Background(), version)
		if (err == nil && verdict == scanner.VerdictClean) != (version <= filepolicy.Version) {
			t.Fatalf("version %d: %v/%v", version, verdict, err)
		}
	}
}

func TestScanInspectionPrecedence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (scanInspection{}).decision(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if _, err := (scanInspection{}).decision(ctx, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// A valid malware verdict blocks even with an incomplete read or policy.
	r := scanInspection{scanned: true, verdict: scanner.VerdictInfected, err: crypto.ErrCiphertext}
	if verdict, err := r.decision(context.Background(), 0); err != nil || verdict != scanner.VerdictInfected {
		t.Fatalf("confirmed malware: %v/%v", verdict, err)
	}
}

func TestStructuralErrorPreservesDeadlineAndReadCause(t *testing.T) {
	jobCtx := context.Background()
	inspectionCtx, cancel := context.WithDeadline(jobCtx, time.Unix(1, 0))
	defer cancel()
	err := scanStructuralError(jobCtx, inspectionCtx, crypto.ErrCiphertext)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, crypto.ErrCiphertext) || scanErrorKind(err) != scanErrorDecrypt {
		t.Fatalf("deadline masked integrity failure: %v", err)
	}

	jobCtx, cancelJob := context.WithCancel(jobCtx)
	cancelJob()
	err = scanStructuralError(jobCtx, inspectionCtx, nil)
	if !errors.Is(err, context.Canceled) || scanErrorKind(err) != scanErrorCanceled {
		t.Fatalf("job cancellation lost precedence: %v", err)
	}
}

func TestScanCompletionCannotForgetReadFailure(t *testing.T) {
	r := &scanCompletionReader{source: iotest.ErrReader(crypto.ErrCiphertext), expected: 4}
	_, _ = r.Read(make([]byte, 4))
	r.source = strings.NewReader("test")
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	if r.complete() {
		t.Fatal("later EOF erased an integrity failure")
	}
}

type ciphertextErrorReader struct{ read bool }

func (r *ciphertextErrorReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	return copy(p, "test"), crypto.ErrCiphertext
}

func TestScanCompletionCountsBytesReturnedWithReadError(t *testing.T) {
	r := &scanCompletionReader{source: &ciphertextErrorReader{}, expected: 4}
	buf := make([]byte, 4)
	if n, err := r.Read(buf); n != 4 || !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("first read=%d/%v, want 4/ErrCiphertext", n, err)
	}
	if n, err := r.Read(buf); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("second read=%d/%v, want 0/EOF", n, err)
	}
	if r.count != 4 || !r.eof || !errors.Is(r.err, crypto.ErrCiphertext) || r.complete() {
		t.Fatalf("count=%d eof=%t err=%v complete=%t", r.count, r.eof, r.err, r.complete())
	}
}

func TestScanCompletionRequiresExactSizeAndEOF(t *testing.T) {
	for _, expected := range []int64{3, 4, 5} {
		r := &scanCompletionReader{source: strings.NewReader("test"), expected: expected}
		if _, err := io.ReadFull(r, make([]byte, 4)); err != nil {
			t.Fatal(err)
		}
		if r.complete() {
			t.Fatal("byte count substituted for authenticated EOF")
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Fatal(err)
		}
		if r.complete() != (expected == 4) {
			t.Fatalf("expected size %d", expected)
		}
	}
}
