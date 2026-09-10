package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	converterapi "github.com/nicrepository/nchat/services/file-service/internal/converter"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// AudioTranscoder produces a real MP3 encoding of arbitrary audio bytes (or an
// audio-only WebM/MP4 recording), through the document-converter sidecar's
// ffmpeg-backed route. Implemented by *converter.Client.
type AudioTranscoder interface {
	ConvertAudio(ctx context.Context, format converterapi.AudioFormat, source io.Reader) ([]byte, error)
}

// audioTranscodeFormat reports the sidecar's input format for a record this
// service should re-encode to real MP3 before serving it, and whether it
// should at all — the Nic-Gravador compatibility task.
//
// video/webm and video/mp4 are only treated as audio-only containers when the
// row is itself tagged AudioKindVoice (RF-670): MediaRecorder's audio-only
// output is wrapped in one of those two containers, and file-service's own
// sniffer cannot tell that from a real video upload — see
// domain.VoiceCompatibleContent and attachmentAudioRules.ts (frontend) for the
// matching fact on the other side of this same detection gap. An ordinary
// video attachment must never be routed through an audio-only re-encode,
// which is exactly what the AudioKind check below prevents.
func audioTranscodeFormat(record StoredAttachment) (converterapi.AudioFormat, bool) {
	switch domain.NormalizeDetectedMIME(record.DetectedMIME) {
	case "audio/ogg", "application/ogg":
		return converterapi.AudioFormatOgg, true
	case "audio/wav", "audio/wave", "audio/x-wav":
		return converterapi.AudioFormatWav, true
	case "video/webm":
		return converterapi.AudioFormatWebM, record.AudioKind == domain.AudioKindVoice
	case "video/mp4":
		return converterapi.AudioFormatMP4, record.AudioKind == domain.AudioKindVoice
	default:
		return "", false
	}
}

// isMP3Already reports whether the stored bytes are already what the upload
// sniffer verified as audio/mpeg — real MP3, since sniffing runs against the
// actual bytes, never a client-declared type. Nothing is re-encoded in this
// case; only the display name is normalised to .mp3 below, in case the
// original upload's filename carried a different extension.
func isMP3Already(record StoredAttachment) bool {
	return domain.NormalizeDetectedMIME(record.DetectedMIME) == "audio/mpeg"
}

// withMP3Extension echoes what the served bytes actually are: by the time
// this is called the payload is genuinely MP3 (either transcoded, or already
// sniffed as audio/mpeg at upload), so replacing the extension is not a
// rename of the wrong content — it is the display name catching up with the
// content type, exactly like every other stored extension already does.
//
// The stored metadata (record.Filename in the database) is never touched:
// this only affects the one Download response being built.
func withMP3Extension(filename string) string {
	base := filename
	if ext := filepath.Ext(filename); ext != "" {
		base = strings.TrimSuffix(filename, ext)
	}
	if base == "" {
		base = "audio"
	}
	normalized, err := domain.NormalizeFilename(base + ".mp3")
	if err != nil {
		return "audio.mp3"
	}
	return normalized
}

// downloadTranscodedAudio serves a real MP3 re-encode of a non-MP3 audio
// attachment (or a voice message recorded in a WebM/MP4 container) instead of
// the bytes as stored. The stored container never changes — only what this
// one download response carries.
//
// The whole plaintext is read into memory and handed to the converter sidecar
// in one request, bounded by converterapi.MaxAudioInputBytes: unlike an
// ordinary download, http.ServeContent's Range support needs a seekable
// stream of known length up front, which a live re-encode cannot offer until
// it has finished — so the result is spooled to a temporary file the same way
// the browser-facing response is served from, and deleted the moment the
// handler closes it (see deleteOnCloseFile).
func (s *AttachmentService) downloadTranscodedAudio(
	ctx context.Context, record StoredAttachment, format converterapi.AudioFormat,
) (Download, error) {
	if record.Size <= 0 || record.Size > converterapi.MaxAudioInputBytes {
		return Download{}, domain.ErrAudioTranscodeFailed
	}
	content, err := s.openDecryptedContent(ctx, record)
	if err != nil {
		return Download{}, err
	}
	defer func() { _ = content.Close() }()

	mp3, err := s.audioTranscoder.ConvertAudio(ctx, format, content)
	if err != nil {
		return Download{}, fmt.Errorf("%w: %w", domain.ErrAudioTranscodeFailed, err)
	}

	file, err := os.CreateTemp("", "nchat-audio-*.mp3")
	if err != nil {
		return Download{}, fmt.Errorf("%w: create temp file", domain.ErrAudioTranscodeFailed)
	}
	if _, err := file.Write(mp3); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return Download{}, fmt.Errorf("%w: write temp file", domain.ErrAudioTranscodeFailed)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return Download{}, fmt.Errorf("%w: rewind temp file", domain.ErrAudioTranscodeFailed)
	}

	return Download{
		Filename:    withMP3Extension(record.Filename),
		ContentType: "audio/mpeg",
		Size:        int64(len(mp3)),
		Content:     deleteOnCloseFile{file},
	}, nil
}

// deleteOnCloseFile is an io.ReadSeekCloser backed by a temporary file that
// deletes itself on Close, so a transcoded download's scratch file never
// outlives the response that served it — success, client disconnect, or
// handler error all reach the same Close.
type deleteOnCloseFile struct{ *os.File }

func (f deleteOnCloseFile) Close() error {
	closeErr := f.File.Close()
	_ = os.Remove(f.Name())
	return closeErr
}
