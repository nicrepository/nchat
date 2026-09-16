package service_test

// Nic-Gravador compatibility task: Download must serve a real MP3 for any
// audio attachment or voice message that is not already one, never the
// stored container merely renamed. These tests exercise that behaviour
// through the exported Download API, the same way every other download test
// in this package does, with a fake AudioTranscoder standing in for the
// document-converter sidecar.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	converterapi "github.com/nicrepository/nchat/services/file-service/internal/converter"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// fakeAudioTranscoder stands in for the document-converter sidecar client.
// Recording every call's format is what lets a test assert *which* container
// Download decided this attachment was, not just that some conversion ran.
type fakeAudioTranscoder struct {
	calls []converterapi.AudioFormat
	mp3   []byte
	err   error
}

func (f *fakeAudioTranscoder) ConvertAudio(
	_ context.Context, format converterapi.AudioFormat, source io.Reader,
) ([]byte, error) {
	f.calls = append(f.calls, format)
	// The real client always reads its source fully; draining it here means a
	// test that forgets to close the upstream decrypted reader still behaves
	// like production would, rather than leaving it half-read.
	_, _ = io.Copy(io.Discard, source)
	if f.err != nil {
		return nil, f.err
	}
	return f.mp3, nil
}

const fakeMP3Payload = "\xff\xfb\x90\x44FAKE-MP3-PAYLOAD"

// uploadWithPurpose mirrors storedAttachment (attachment_service_test.go) but
// lets the caller choose the upload purpose and declared MIME, and override
// the status the way every existing download test already does.
func uploadWithPurpose(
	t *testing.T, f *fixture, payload []byte, filename, declaredMIME, purpose string, status domain.Status,
) service.StoredAttachment {
	t.Helper()
	target, err := f.service.AuthorizeUpload(context.Background(), service.AuthorizeUploadInput{
		Destination: domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		UserID:      testUserID, SessionID: testSessionID,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	view, err := f.service.Upload(context.Background(), service.UploadInput{
		Target: target, Filename: filename, DeclaredMIME: declaredMIME,
		Purpose: purpose, Content: bytes.NewReader(payload),
	})
	if err != nil {
		t.Fatalf("seed upload: %v", err)
	}
	created, uploaded, _ := f.store.snapshot()
	return service.StoredAttachment{
		ID: view.ID, WorkspaceID: testWorkspaceID, Kind: domain.DestinationKindChannel,
		Status: status, Filename: view.Filename,
		DeclaredMIME: created[0].DeclaredMIME, DetectedMIME: uploaded[0].DetectedMIME,
		Size: uploaded[0].Size, StorageObjectKey: created[0].StorageObjectKey,
		EnvelopeVersion:    created[0].EnvelopeVersion,
		WrappedDEK:         uploaded[0].WrappedDEK,
		KEKKeyID:           uploaded[0].KEKKeyID,
		KeyWrapVersion:     created[0].KeyWrapVersion,
		CreatedAt:          time.Now().UTC(),
		SessionExpiresAt:   time.Now().Add(time.Hour),
		AudioKind:          created[0].AudioKind,
		DeclaredDurationMs: created[0].DeclaredDurationMs,
	}
}

func TestDownloadTranscodesOggAudioToRealMP3(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte("OggS"), make([]byte, 40)...)
	record := uploadWithPurpose(t, f, payload, "recording.ogg", "audio/ogg", "", domain.StatusClean)
	f.store.authorized = record

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if download.ContentType != "audio/mpeg" {
		t.Fatalf("content type = %q, want audio/mpeg", download.ContentType)
	}
	if !strings.HasSuffix(download.Filename, ".mp3") {
		t.Fatalf("filename = %q, want a .mp3 suffix", download.Filename)
	}
	got, err := io.ReadAll(download.Content)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The decisive assertion: the served bytes are the transcoder's own
	// output, not the original OGG payload wearing a new name.
	if string(got) != fakeMP3Payload {
		t.Fatalf("payload = %q, want the transcoder's MP3 bytes — the OGG source must never be served as the .mp3", got)
	}
	if len(transcoder.calls) != 1 || transcoder.calls[0] != converterapi.AudioFormatOgg {
		t.Fatalf("transcoder calls = %v, want exactly one call with AudioFormatOgg", transcoder.calls)
	}
}

func TestDownloadTranscodesWavAudioToRealMP3(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append(append([]byte("RIFF\x00\x00\x00\x00"), []byte("WAVE")...), bytes.Repeat([]byte("x"), 20)...)
	record := uploadWithPurpose(t, f, payload, "clip.wav", "audio/wav", "", domain.StatusClean)
	f.store.authorized = record

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if download.ContentType != "audio/mpeg" || !strings.HasSuffix(download.Filename, ".mp3") {
		t.Fatalf("content type/filename = %q/%q", download.ContentType, download.Filename)
	}
	got, err := io.ReadAll(download.Content)
	if err != nil || string(got) != fakeMP3Payload {
		t.Fatalf("payload = %q, err=%v, want the transcoder's MP3 bytes", got, err)
	}
	if len(transcoder.calls) != 1 || transcoder.calls[0] != converterapi.AudioFormatWav {
		t.Fatalf("transcoder calls = %v, want exactly one call with AudioFormatWav", transcoder.calls)
	}
}

// TestDownloadTranscodesVoiceWebMToRealMP3 covers RF-670's actual recording
// shape: MediaRecorder's audio-only output sniffs as video/webm, and only the
// AudioKindVoice tag says it should be treated as audio at all — see
// audioTranscodeFormat's own doc comment.
func TestDownloadTranscodesVoiceWebMToRealMP3(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, bytes.Repeat([]byte("x"), 40)...)
	record := uploadWithPurpose(
		t, f, payload, "voice-message.webm", "audio/webm",
		service.UploadPurposeVoiceMessage, domain.StatusClean,
	)
	f.store.authorized = record
	if record.AudioKind != domain.AudioKindVoice {
		t.Fatalf("test setup: expected the upload to be tagged voice, got %q", record.AudioKind)
	}

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if download.ContentType != "audio/mpeg" || !strings.HasSuffix(download.Filename, ".mp3") {
		t.Fatalf("content type/filename = %q/%q", download.ContentType, download.Filename)
	}
	if len(transcoder.calls) != 1 || transcoder.calls[0] != converterapi.AudioFormatWebM {
		t.Fatalf("transcoder calls = %v, want exactly one call with AudioFormatWebM", transcoder.calls)
	}
}

// TestDownloadTranscodesVoiceMP4ToRealMP3 is TestDownloadTranscodesVoiceWebMToRealMP3's
// counterpart for Safari, whose MediaRecorder wraps its audio-only output in
// an MP4 container instead of WebM (see useVoiceRecorder.ts's CANDIDATE_MIME_TYPES).
func TestDownloadTranscodesVoiceMP4ToRealMP3(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	// A minimal ftyp box: net/http.DetectContentType's MP4 signature only
	// checks for "ftyp" at offset 4, so this is enough to sniff as video/mp4
	// without needing a real, fully-formed container.
	payload := append(append([]byte{0, 0, 0, 0x18}, []byte("ftypisom")...), []byte{0, 0, 2, 0}...)
	payload = append(payload, []byte("isommp41")...)
	record := uploadWithPurpose(
		t, f, payload, "voice-message.mp4", "audio/mp4",
		service.UploadPurposeVoiceMessage, domain.StatusClean,
	)
	f.store.authorized = record
	if record.AudioKind != domain.AudioKindVoice {
		t.Fatalf("test setup: expected the upload to be tagged voice, got %q", record.AudioKind)
	}

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if download.ContentType != "audio/mpeg" || !strings.HasSuffix(download.Filename, ".mp3") {
		t.Fatalf("content type/filename = %q/%q", download.ContentType, download.Filename)
	}
	if len(transcoder.calls) != 1 || transcoder.calls[0] != converterapi.AudioFormatMP4 {
		t.Fatalf("transcoder calls = %v, want exactly one call with AudioFormatMP4", transcoder.calls)
	}
}

// TestDownloadNeverTranscodesAnOrdinaryWebMVideo is the regression guard on
// the other side of the same detection gap: a real video upload sniffs
// identically to a voice message (video/webm), and the only thing telling
// them apart is AudioKind. Routing an actual video through an audio-only
// re-encode would silently destroy its picture track.
func TestDownloadNeverTranscodesAnOrdinaryWebMVideo(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, bytes.Repeat([]byte("x"), 40)...)
	record := uploadWithPurpose(t, f, payload, "screen-recording.webm", "video/webm", "", domain.StatusClean)
	f.store.authorized = record
	if record.AudioKind == domain.AudioKindVoice {
		t.Fatal("test setup: an ordinary upload must not be tagged voice")
	}

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if download.ContentType != "video/webm" {
		t.Fatalf("content type = %q, want video/webm unchanged", download.ContentType)
	}
	if download.Filename != "screen-recording.webm" {
		t.Fatalf("filename = %q, want the stored name unchanged", download.Filename)
	}
	if len(transcoder.calls) != 0 {
		t.Fatalf("transcoder calls = %v, want none: a real video must never be re-encoded as audio", transcoder.calls)
	}
	got, err := io.ReadAll(download.Content)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("payload changed for a non-audio attachment: err=%v", err)
	}
}

// TestDownloadPassesThroughAlreadyMP3AudioWithoutTranscoding covers the case
// the upload sniffer already verified as real MP3 (audio/mpeg from the
// actual bytes, via an ID3 tag): nothing is re-encoded, and the only change
// is the display name catching up if the original upload's name did not
// already end in .mp3.
func TestDownloadPassesThroughAlreadyMP3AudioWithoutTranscoding(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte("ID3"), bytes.Repeat([]byte("x"), 40)...)
	record := uploadWithPurpose(t, f, payload, "song.MID3", "audio/mpeg", "", domain.StatusClean)
	f.store.authorized = record
	if record.DetectedMIME != "audio/mpeg" {
		t.Fatalf("test setup: expected the upload to sniff as audio/mpeg, got %q", record.DetectedMIME)
	}

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if !strings.HasSuffix(download.Filename, ".mp3") {
		t.Fatalf("filename = %q, want a .mp3 suffix", download.Filename)
	}
	if len(transcoder.calls) != 0 {
		t.Fatalf("transcoder calls = %v, want none: already-MP3 content must never round-trip through the converter", transcoder.calls)
	}
	got, err := io.ReadAll(download.Content)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("payload changed for already-MP3 content: err=%v", err)
	}
}

// TestDownloadPassThroughAlreadyMP3PropagatesStorageFailure covers Download's
// other storage-open call site — the isMP3Already branch, which never reaches
// downloadTranscodedAudio at all. A decrypt/storage failure there must
// surface exactly like it does for every other attachment type, not be
// swallowed or misreported as a transcode failure.
func TestDownloadPassThroughAlreadyMP3PropagatesStorageFailure(t *testing.T) {
	f := newFixture(t)
	payload := append([]byte("ID3"), bytes.Repeat([]byte("x"), 40)...)
	record := uploadWithPurpose(t, f, payload, "song.mp3", "audio/mpeg", "", domain.StatusClean)
	f.store.authorized = record
	f.objects.openErr = domain.ErrUnavailable

	_, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if errors.Is(err, domain.ErrAudioTranscodeFailed) {
		t.Fatal("already-MP3 content never transcodes, so its storage failure is not a transcode failure")
	}
}

// TestDownloadFailsWhenTranscodingFailsRatherThanServingTheOriginal is the
// core safety property: a converter failure must never fall back to serving
// the stored container labeled as MP3.
func TestDownloadFailsWhenTranscodingFailsRatherThanServingTheOriginal(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{err: converterapi.ErrTransient}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte("OggS"), make([]byte, 40)...)
	record := uploadWithPurpose(t, f, payload, "recording.ogg", "audio/ogg", "", domain.StatusClean)
	f.store.authorized = record

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if !errors.Is(err, domain.ErrAudioTranscodeFailed) {
		t.Fatalf("error = %v, want ErrAudioTranscodeFailed", err)
	}
	if download.Content != nil {
		t.Fatal("a failed transcode must never return content to serve")
	}
}

// TestDownloadSkipsTranscodingWhenNoTranscoderIsConfigured is what keeps
// every pre-existing download test's behaviour byte-for-byte unchanged:
// SetAudioTranscoder is never called by those fixtures, so this is the
// default every one of them already runs under.
func TestDownloadSkipsTranscodingWhenNoTranscoderIsConfigured(t *testing.T) {
	f := newFixture(t)
	payload := append([]byte("OggS"), make([]byte, 40)...)
	record := uploadWithPurpose(t, f, payload, "recording.ogg", "audio/ogg", "", domain.StatusClean)
	f.store.authorized = record

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if download.ContentType == "audio/mpeg" || download.Filename != "recording.ogg" {
		t.Fatalf("content type/filename = %q/%q, want the stored OGG unchanged", download.ContentType, download.Filename)
	}
}

// TestDownloadRejectsAudioOverTheTranscodeSizeCapWithoutReadingIt proves the
// size check runs before storage is ever opened: the fake transcoder is
// never called, matching the guard in downloadTranscodedAudio.
func TestDownloadRejectsAudioOverTheTranscodeSizeCapWithoutReadingIt(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte("OggS"), make([]byte, 40)...)
	record := uploadWithPurpose(t, f, payload, "recording.ogg", "audio/ogg", "", domain.StatusClean)
	// The real object stored is tiny; only the size this record claims is
	// inflated, which is exactly what the pre-storage guard must catch.
	record.Size = converterapi.MaxAudioInputBytes + 1
	f.store.authorized = record

	_, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if !errors.Is(err, domain.ErrAudioTranscodeFailed) {
		t.Fatalf("error = %v, want ErrAudioTranscodeFailed", err)
	}
	if len(transcoder.calls) != 0 {
		t.Fatalf("transcoder calls = %v, want none for an oversized attachment", transcoder.calls)
	}
}

// TestDownloadTranscodePropagatesAStorageFailureWithoutCallingTheTranscoder
// covers the case downloadTranscodedAudio's own storage open sits in front
// of: a decrypt/storage failure must surface as-is, and the converter must
// never be asked to transcode content that was never actually read.
func TestDownloadTranscodePropagatesAStorageFailureWithoutCallingTheTranscoder(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte("OggS"), make([]byte, 40)...)
	record := uploadWithPurpose(t, f, payload, "recording.ogg", "audio/ogg", "", domain.StatusClean)
	f.store.authorized = record
	f.objects.openErr = domain.ErrUnavailable

	_, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if errors.Is(err, domain.ErrAudioTranscodeFailed) {
		t.Fatal("a storage failure is not a transcode failure and must not be reported as one")
	}
	if len(transcoder.calls) != 0 {
		t.Fatalf("transcoder calls = %v, want none: content that could not be opened was never handed to it", transcoder.calls)
	}
}

// TestDownloadTranscodedFilenameFallsBackWhenTheStoredNameIsEmpty exercises
// withMP3Extension's defensive branch for a record whose filename strips to
// nothing — no real upload can produce this (NormalizeFilename never accepts
// an empty name), but the extension-swap logic must still not panic or
// produce a bare ".mp3" leading dot on a metadata row from before that
// invariant existed.
func TestDownloadTranscodedFilenameFallsBackWhenTheStoredNameIsEmpty(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte("OggS"), make([]byte, 40)...)
	record := uploadWithPurpose(t, f, payload, "recording.ogg", "audio/ogg", "", domain.StatusClean)
	record.Filename = ""
	f.store.authorized = record

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if download.Filename != "audio.mp3" {
		t.Fatalf("filename = %q, want the audio.mp3 fallback", download.Filename)
	}
}

// TestDownloadTranscodedFilenameFallsBackWhenTheStoredNameIsInvalidUTF8 covers
// withMP3Extension's other defensive branch: domain.NormalizeFilename refuses
// a name that is not valid UTF-8 (a row a normal upload could never produce,
// since NormalizeFilename already runs at upload time — this guards a
// corrupted or pre-migration row instead of trusting the extension swap to
// always succeed).
func TestDownloadTranscodedFilenameFallsBackWhenTheStoredNameIsInvalidUTF8(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte("OggS"), make([]byte, 40)...)
	record := uploadWithPurpose(t, f, payload, "recording.ogg", "audio/ogg", "", domain.StatusClean)
	record.Filename = string([]byte{0xff, 0xfe, 0x00})
	f.store.authorized = record

	download, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if download.Filename != "audio.mp3" {
		t.Fatalf("filename = %q, want the audio.mp3 fallback", download.Filename)
	}
}

// TestDownloadTranscodeFailsCleanlyWhenTheTempFileCannotBeCreated covers the
// os.CreateTemp failure path: a transcode that otherwise succeeded must
// still never be reported as a successful download if the spooled file
// itself could not be created.
func TestDownloadTranscodeFailsCleanlyWhenTheTempFileCannotBeCreated(t *testing.T) {
	f := newFixture(t)
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	payload := append([]byte("OggS"), make([]byte, 40)...)
	record := uploadWithPurpose(t, f, payload, "recording.ogg", "audio/ogg", "", domain.StatusClean)
	f.store.authorized = record

	// A TMPDIR that does not exist makes os.CreateTemp fail deterministically,
	// without touching the real temp directory any other test relies on.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	_, err := f.service.Download(context.Background(), downloadInput(record.ID))
	if !errors.Is(err, domain.ErrAudioTranscodeFailed) {
		t.Fatalf("error = %v, want ErrAudioTranscodeFailed", err)
	}
}
