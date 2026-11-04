package service

import (
	"context"
	"fmt"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
)

const inspectionTimeout = 10 * time.Second

func (s *AttachmentService) SetFormatProfiles(p filepolicy.Profiles)  { s.profiles = p }
func (s *MalwareScanService) SetFormatProfiles(p filepolicy.Profiles) { s.profiles = p }

func (s *AttachmentService) inspectPending(ctx context.Context, target uploadTarget, p pendingAttachment, size int64) (filepolicy.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, inspectionTimeout)
	defer cancel()
	r := &rangeReader{ctx: ctx, objects: s.objects, logger: s.logger, dataKey: p.dataKey, objectID: p.id,
		object: encryptedObject{objectID: p.id.String(), objectKey: p.objectKey, size: size, workspaceID: target.workspaceID}}
	defer func() { _ = r.Close() }()
	started := time.Now()
	result, err := filepolicy.Inspect(ctx, r, size, target.filename, target.declaredMIME, s.profiles)
	if observer, ok := s.orphans.(interface {
		ObserveFileInspection(filepolicy.Result, time.Duration)
	}); ok {
		observer.ObserveFileInspection(result, time.Since(started))
	}
	if err != nil {
		return result, fmt.Errorf("%w: inspection unavailable", domain.ErrUnavailable)
	}
	if result.Decision != filepolicy.Allow {
		return result, policyError(result)
	}
	return result, nil
}

func policyError(r filepolicy.Result) error {
	switch r.Code {
	case filepolicy.Unsupported:
		return domain.ErrUnsupportedMedia
	case filepolicy.BudgetExceeded:
		return domain.ErrInspectionLimit
	case filepolicy.Invalid, filepolicy.Mismatch:
		return domain.ErrUnrecognizedContent
	default:
		return domain.ErrFormatBlocked
	}
}

func (s *AttachmentService) reauthorizeUpload(ctx context.Context, target uploadTarget) error {
	authorized, err := s.authorizer.AuthorizeDestination(ctx, DestinationAuthInput{
		Destination: target.destination, UserID: target.uploaderID, SessionID: target.sessionID,
	})
	if err != nil {
		return err
	}
	if authorized.WorkspaceID != target.workspaceID || authorized.ID != target.destination.ID {
		return domain.ErrNotFound
	}
	return nil
}

// DownloadMP3 is an explicit derivative. Download always returns the original.
func (s *AttachmentService) DownloadMP3(ctx context.Context, input AttachmentAuthInput) (Download, error) {
	if !s.Ready() {
		return Download{}, domain.ErrDependenciesUnavailable
	}
	record, err := s.downloadableAttachment(ctx, input)
	if err != nil {
		return Download{}, err
	}
	if isMP3Already(record) {
		original, err := s.originalDownload(ctx, record)
		if err != nil {
			return Download{}, err
		}
		original.Filename = withMP3Extension(record.Filename)
		return original, nil
	}
	format, ok := audioTranscodeFormat(record)
	if !ok || s.audioTranscoder == nil {
		return Download{}, domain.ErrAudioTranscodeFailed
	}
	derived, err := s.downloadTranscodedAudio(ctx, record, format)
	if err != nil {
		return Download{}, err
	}
	if _, err = s.downloadableAttachment(ctx, input); err != nil {
		_ = derived.Content.Close()
		return Download{}, err
	}
	return derived, nil
}
