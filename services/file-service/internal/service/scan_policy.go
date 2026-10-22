package service

import (
	"context"
	"errors"
	"io"

	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
)

var errScanPolicy = errors.New("scan policy could not approve content")
var errFutureScanPolicy = errors.New("scan policy version is unsupported")

func (s *MalwareScanService) inspectScanContent(ctx context.Context, r io.ReadSeeker, job ScanJob) error {
	if job.PolicyVersion > filepolicy.Version {
		return errFutureScanPolicy
	}
	result, err := filepolicy.Inspect(ctx, r, job.Size, job.Filename, job.DeclaredMIME, s.profiles)
	if err != nil {
		return err
	}
	if result.Decision != filepolicy.Allow {
		return errScanPolicy
	}
	return nil
}

// Approval requires EOF, not just the advertised byte count: EOF authenticates
// the envelope's final frame. A daemon replying OK early cannot approve a prefix.
type scanCompletionReader struct {
	source          io.Reader
	expected, count int64
	eof             bool
}

func (r *scanCompletionReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	r.count += int64(n)
	if errors.Is(err, io.EOF) {
		r.eof = true
	}
	return n, err
}
func (r *scanCompletionReader) complete() bool { return r.eof && r.count == r.expected }
