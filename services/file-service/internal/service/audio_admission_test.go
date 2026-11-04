package service_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	converterapi "github.com/nicrepository/nchat/services/file-service/internal/converter"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

type barrierTranscoder struct {
	entered chan struct{}
	release chan struct{}
	err     error
}

func (b *barrierTranscoder) ConvertAudio(ctx context.Context, _ converterapi.AudioFormat, source io.Reader) ([]byte, error) {
	if _, err := io.Copy(io.Discard, source); err != nil {
		return nil, err
	}
	b.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
	}
	return []byte(fakeMP3Payload), b.err
}

type audioDownloadResult struct {
	download service.Download
	err      error
}

func TestAudioAdmissionBoundsConversionAndDelivery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	f := newFixture(t)
	record := uploadWithPurpose(t, f, append([]byte("OggS"), make([]byte, 40)...), "audio.ogg", "audio/ogg", "", domain.StatusClean)
	other := uploadWithPurpose(t, f, append([]byte("OggS"), make([]byte, 40)...), "other.ogg", "audio/ogg", "", domain.StatusClean)
	f.store.authorizedByID = map[string]service.StoredAttachment{record.ID: record, other.ID: other}
	b := &barrierTranscoder{entered: make(chan struct{}, 4), release: make(chan struct{})}
	f.service.SetAudioTranscoder(b)
	result := make(chan audioDownloadResult, 1)
	go func() {
		d, err := f.service.DownloadMP3(context.Background(), downloadInput(record.ID))
		result <- audioDownloadResult{d, err}
	}()
	<-b.entered
	for _, id := range []string{record.ID, other.ID} {
		if _, err := f.service.DownloadMP3(context.Background(), downloadInput(id)); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("saturation: %v", err)
		}
	}
	close(b.release)
	first := <-result
	if first.err != nil {
		t.Fatal(first.err)
	}
	if _, err := f.service.DownloadMP3(context.Background(), downloadInput(record.ID)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("delivery not bounded: %v", err)
	}
	if err := first.download.Content.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch leak: %v/%v", entries, err)
	}
	second, err := f.service.DownloadMP3(context.Background(), downloadInput(other.ID))
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Content.Close()
}

func TestAudioAdmissionReleasesOnFailureAndCancellation(t *testing.T) {
	for _, failure := range []error{errors.New("process failed"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			f := newFixture(t)
			record := uploadWithPurpose(t, f, append([]byte("OggS"), make([]byte, 40)...), "audio.ogg", "audio/ogg", "", domain.StatusClean)
			f.store.authorized = record
			b := &barrierTranscoder{entered: make(chan struct{}, 2), release: make(chan struct{}), err: failure}
			f.service.SetAudioTranscoder(b)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := f.service.DownloadMP3(ctx, downloadInput(record.ID)); done <- err }()
			<-b.entered
			if failure == context.Canceled {
				cancel()
			} else {
				close(b.release)
			}
			if err := <-done; !errors.Is(err, failure) {
				t.Fatalf("failure: %v", err)
			}
			f.service.SetAudioTranscoder(&fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)})
			d, err := f.service.DownloadMP3(context.Background(), downloadInput(record.ID))
			if err != nil {
				t.Fatal(err)
			}
			_ = d.Content.Close()
		})
	}
}

func TestAudioAuthorizationPrecedesAdmission(t *testing.T) {
	f := newFixture(t)
	record := uploadWithPurpose(t, f, append([]byte("OggS"), make([]byte, 40)...), "audio.ogg", "audio/ogg", "", domain.StatusClean)
	f.store.authorized = record
	transcoder := &fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)}
	f.service.SetAudioTranscoder(transcoder)
	f.store.authorizedErr = domain.ErrNotFound
	if _, err := f.service.DownloadMP3(context.Background(), downloadInput(record.ID)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if len(transcoder.calls) != 0 {
		t.Fatal("unauthorized request started conversion")
	}
}

func TestAudioCancellationBeforeAdmissionDoesNotOpenOriginal(t *testing.T) {
	f := newFixture(t)
	record := uploadWithPurpose(t, f, append([]byte("OggS"), make([]byte, 40)...), "audio.ogg", "audio/ogg", "", domain.StatusClean)
	f.store.authorized = record
	f.service.SetAudioTranscoder(&fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)})
	before := f.objects.openCount()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.service.DownloadMP3(ctx, downloadInput(record.ID)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.objects.openCount() != before {
		t.Fatal("canceled request opened original")
	}
}

func TestAudioConverterSaturationPropagatesAndReleasesAdmission(t *testing.T) {
	f := newFixture(t)
	record := uploadWithPurpose(t, f, append([]byte("OggS"), make([]byte, 40)...), "audio.ogg", "audio/ogg", "", domain.StatusClean)
	f.store.authorized = record
	f.service.SetAudioTranscoder(&fakeAudioTranscoder{err: converterapi.ErrBusy})
	if _, err := f.service.DownloadMP3(context.Background(), downloadInput(record.ID)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("converter busy: %v", err)
	}
	f.service.SetAudioTranscoder(&fakeAudioTranscoder{mp3: []byte(fakeMP3Payload)})
	d, err := f.service.DownloadMP3(context.Background(), downloadInput(record.ID))
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Content.Close()
}
