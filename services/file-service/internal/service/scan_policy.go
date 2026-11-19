package service

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

var errScanPolicy = errors.New("scan policy could not approve content")
var errFutureScanPolicy = errors.New("scan policy version is unsupported")

// scanInspection keeps the #1128 policy evidence separate from AV and complete
// authenticated reading. Strategy identifies the inspection required by v1;
// Recognized means that strategy completed, not that every format has a parser.
type scanInspection struct {
	policy       filepolicy.Result
	verdict      scanner.Verdict
	evidence     *signatures.Evidence
	scanned      bool
	readComplete bool
	err          error
}

func (r scanInspection) decision(ctx context.Context, policyVersion int) (scanner.Verdict, error) {
	if r.scanned && r.verdict == scanner.VerdictInfected {
		return scanner.VerdictInfected, nil
	}
	if err := ctx.Err(); err != nil {
		return scanner.VerdictInfected, err
	}
	if r.err != nil {
		return scanner.VerdictInfected, r.err
	}
	if err := scanPolicyDecision(r.policy, policyVersion); err != nil {
		return scanner.VerdictInfected, err
	}
	if !r.scanned || r.verdict != scanner.VerdictClean || !r.readComplete {
		return scanner.VerdictInfected, scanner.ErrProtocol
	}
	return scanner.VerdictClean, nil
}

func scanPolicyDecision(result filepolicy.Result, policyVersion int) error {
	if policyVersion > filepolicy.Version || result.Version != filepolicy.Version {
		return errFutureScanPolicy
	}
	if result.Decision != filepolicy.Allow {
		return errScanPolicy
	}
	if result.Code != filepolicy.Recognized || result.MIMEMismatch {
		return errScanPolicy
	}
	if result.Format.ID == "" || result.Format.ID == filepolicy.IDUnknown || result.Format.Strategy == "" || result.Format.Strategy == filepolicy.Unavailable {
		return errScanPolicy
	}
	return nil
}

func (s *MalwareScanService) inspectScanContent(ctx context.Context, job ScanJob) (filepolicy.Result, error) {
	if job.PolicyVersion > filepolicy.Version {
		return filepolicy.Result{}, errFutureScanPolicy
	}
	inspectionCtx, cancel := context.WithTimeout(ctx, inspectionTimeout)
	defer cancel()
	r, err := s.openScanContent(inspectionCtx, job)
	if err != nil {
		return filepolicy.Result{}, scanStructuralError(ctx, inspectionCtx, err)
	}
	defer func() { _ = r.Close() }()
	started := time.Now()
	defer s.observeScanPhase("format_validation", started)
	result, err := filepolicy.Inspect(inspectionCtx, r, job.Size, job.Filename, job.DeclaredMIME, s.profiles)
	if observer, ok := s.observer.(interface {
		ObserveFileInspection(filepolicy.Result, time.Duration)
	}); ok {
		observer.ObserveFileInspection(result, time.Since(started))
	}
	if err := scanStructuralError(ctx, inspectionCtx, err); err != nil {
		return result, err
	}
	return result, scanPolicyDecision(result, job.PolicyVersion)
}

func scanStructuralError(jobCtx, inspectionCtx context.Context, cause error) error {
	if err := jobCtx.Err(); err != nil {
		return err
	}
	inspectionErr := inspectionCtx.Err()
	if inspectionErr == nil && cause == nil {
		return nil
	}
	// Reuse the pre-AV category while preserving the deadline and underlying read error.
	return errors.Join(errScanContentUnavailable, inspectionErr, cause)
}

// Approval requires EOF, not just the advertised byte count: EOF authenticates
// the envelope's final frame. A daemon replying OK early cannot approve a prefix.
type scanCompletionReader struct {
	source          io.Reader
	expected, count int64
	eof             bool
	err             error
}

func (r *scanCompletionReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	r.count += int64(n)
	if errors.Is(err, io.EOF) {
		r.eof = true
	} else if err != nil {
		r.err = err
	}
	return n, err
}
func (r *scanCompletionReader) complete() bool {
	return r.err == nil && r.eof && r.count == r.expected
}
