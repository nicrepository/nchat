package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// rangeFixture stores one clean, multi-chunk attachment and returns its
// plaintext alongside the fixture, so a test can compare a seek's output against
// the bytes that were actually uploaded.
func rangeFixture(t *testing.T, size int) (*fixture, []byte) {
	t.Helper()
	f := newFixture(t)
	payload := make([]byte, size)
	for i := range payload {
		// A repeating but non-uniform pattern: a wrong offset produces visibly
		// wrong bytes rather than accidentally matching.
		payload[i] = byte(i*7 + i/251)
	}
	f.store.authorized = storedAttachment(t, f, payload, domain.StatusClean)
	return f, payload
}

// A seek followed by a read must produce the plaintext at that offset — the
// property the whole HTTP range contract rests on.
func TestDownloadSeeksToThePlaintextAtAnyOffset(t *testing.T) {
	const size = 3*crypto.ChunkSize + 777
	f, payload := rangeFixture(t, size)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	offsets := []int64{
		0, 1, crypto.ChunkSize - 1, crypto.ChunkSize, crypto.ChunkSize + 1,
		2 * crypto.ChunkSize, 3 * crypto.ChunkSize, size - 1,
	}
	for _, offset := range offsets {
		if got, err := download.Content.Seek(offset, io.SeekStart); err != nil || got != offset {
			t.Fatalf("seek to %d: got %d, %v", offset, got, err)
		}
		window := make([]byte, min(int64(256), size-offset))
		if _, err := io.ReadFull(download.Content, window); err != nil {
			t.Fatalf("read at %d: %v", offset, err)
		}
		if !bytes.Equal(window, payload[offset:offset+int64(len(window))]) {
			t.Fatalf("offset %d returned the wrong plaintext", offset)
		}
	}
}

// The point of the feature: a small range deep in a large file must not pull —
// or decrypt — everything before it. The fake store records the byte offset it
// was asked to read from, so this is checked rather than assumed.
func TestDownloadRangeReadsFromTheOffsetNotTheStart(t *testing.T) {
	const size = 10 * crypto.ChunkSize
	const offset = 9 * crypto.ChunkSize
	f, payload := rangeFixture(t, size)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if _, err := download.Content.Seek(offset, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	window := make([]byte, 64)
	if _, err := io.ReadFull(download.Content, window); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(window, payload[offset:offset+64]) {
		t.Fatal("wrong plaintext at the offset")
	}

	// One read for the header and one for the chunk. The chunk read has to start
	// at the chunk's own place in the object — reading from zero would mean the
	// ninety chunks before it were fetched and decrypted for nothing.
	_, wantCiphertextOffset, _ := crypto.ChunkLocation(offset)
	offsets := f.objects.rangeReads()
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != wantCiphertextOffset {
		t.Fatalf("expected a header read and a read at %d, got %v", wantCiphertextOffset, offsets)
	}
}

// Seeking repeatedly — what a viewer dragging a scrub bar produces — costs one
// storage read per seek. The header is fetched once and reused.
func TestDownloadReusesTheHeaderAcrossSeeks(t *testing.T) {
	const size = 8 * crypto.ChunkSize
	f, payload := rangeFixture(t, size)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	for _, offset := range []int64{7 * crypto.ChunkSize, 2 * crypto.ChunkSize, 5 * crypto.ChunkSize} {
		if _, err := download.Content.Seek(offset, io.SeekStart); err != nil {
			t.Fatalf("seek: %v", err)
		}
		window := make([]byte, 32)
		if _, err := io.ReadFull(download.Content, window); err != nil {
			t.Fatalf("read at %d: %v", offset, err)
		}
		if !bytes.Equal(window, payload[offset:offset+32]) {
			t.Fatalf("offset %d returned the wrong plaintext", offset)
		}
	}

	// One header read plus one chunk read per seek.
	if got := len(f.objects.rangeReads()); got != 4 {
		t.Fatalf("expected 4 ranged reads, got %d: %v", got, f.objects.rangeReads())
	}
}

// Measuring the object is what net/http does before serving anything. It must
// cost nothing: no seek performs I/O, and returning to a position the open
// stream already holds must not reopen it.
func TestSeekingDoesNotTouchStorage(t *testing.T) {
	f, payload := rangeFixture(t, 4096)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	end, err := download.Content.Seek(0, io.SeekEnd)
	if err != nil || end != int64(len(payload)) {
		t.Fatalf("seek to end: got %d, %v", end, err)
	}
	if _, err := download.Content.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek to start: %v", err)
	}
	if got := f.objects.rangeReads(); len(got) != 0 {
		t.Fatalf("measuring the object must not read it, got %v", got)
	}

	// And the stream that was opened when the download was created is still the
	// one used, so the ordinary whole-file transfer costs no extra round trip.
	got, err := io.ReadAll(download.Content)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("the whole file did not come back intact")
	}
	if reads := f.objects.rangeReads(); len(reads) != 0 {
		t.Fatalf("a read from the start must not use a ranged read, got %v", reads)
	}
}

// The cursor is bounded by the authenticated length, so nothing can move it
// outside the object.
func TestSeekRefusesPositionsOutsideTheObject(t *testing.T) {
	f, payload := rangeFixture(t, 1024)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	tests := []struct {
		name   string
		offset int64
		whence int
	}{
		{"before the start", -1, io.SeekStart},
		{"past the end", int64(len(payload)) + 1, io.SeekStart},
		{"past the end from the end", 1, io.SeekEnd},
		{"before the start from the end", -int64(len(payload)) - 1, io.SeekEnd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := download.Content.Seek(tt.offset, tt.whence); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}

	t.Run("unknown whence", func(t *testing.T) {
		if _, err := download.Content.Seek(0, 99); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput, got %v", err)
		}
	})

	// Exactly at the end is where a size measurement leaves the cursor, so it is
	// allowed — it simply has nothing to yield.
	if _, err := download.Content.Seek(int64(len(payload)), io.SeekStart); err != nil {
		t.Fatalf("seeking to the end must be allowed: %v", err)
	}
}

// A storage failure on a ranged read is reported as a failure, never as a short
// or empty range that a client would accept as content.
func TestRangedReadReportsStorageFailures(t *testing.T) {
	f, _ := rangeFixture(t, 4*crypto.ChunkSize)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	f.objects.openErr = domain.ErrUnavailable
	if _, err := download.Content.Seek(3*crypto.ChunkSize, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := download.Content.Read(make([]byte, 16)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

// An object that metadata says exists but storage cannot produce is an
// operational failure, not a client-visible "not found": reporting otherwise
// would describe storage to the caller.
func TestRangedReadHidesAMissingObject(t *testing.T) {
	f, _ := rangeFixture(t, 4*crypto.ChunkSize)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if err := f.objects.Delete(context.Background(), f.store.authorized.StorageObjectKey); err != nil {
		t.Fatalf("remove object: %v", err)
	}
	if _, err := download.Content.Seek(3*crypto.ChunkSize, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	_, err = download.Content.Read(make([]byte, 16))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatal("storage state must not reach the caller")
	}
}

// A tampered chunk fails when it is read at an offset, exactly as it does when
// the file is read whole. Nothing about seeking relaxes integrity.
func TestRangedReadStillFailsOnTamperedContent(t *testing.T) {
	const size = 4 * crypto.ChunkSize
	f, _ := rangeFixture(t, size)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	key, stored := f.objects.only(t)
	_, ciphertextOffset, _ := crypto.ChunkLocation(3 * crypto.ChunkSize)
	tampered := append([]byte(nil), stored...)
	tampered[ciphertextOffset+64] ^= 0xff
	f.objects.replace(key, tampered)

	if _, err := download.Content.Seek(3*crypto.ChunkSize, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := io.ReadAll(download.Content); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected a ciphertext failure, got %v", err)
	}
}

// Seeking into the middle of a chunk decrypts the bytes before the offset and
// discards them. A corrupt chunk therefore has to fail during that discard,
// before anything is handed out — not silently yield whatever the offset landed
// on.
func TestRangedReadFailsWhenSeekingIntoACorruptChunk(t *testing.T) {
	const size = 4 * crypto.ChunkSize
	const offset = 3*crypto.ChunkSize + 4096 // inside chunk 3, not on its boundary
	f, _ := rangeFixture(t, size)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	key, stored := f.objects.only(t)
	_, ciphertextOffset, _ := crypto.ChunkLocation(offset)
	tampered := append([]byte(nil), stored...)
	tampered[ciphertextOffset+32] ^= 0xff
	f.objects.replace(key, tampered)

	if _, err := download.Content.Seek(offset, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := download.Content.Read(make([]byte, 16)); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected a ciphertext failure, got %v", err)
	}
}
