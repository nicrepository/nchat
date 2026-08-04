package crypto_test

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
)

// largeFileEnv opts into the large-file validation and says how big it is, in
// MiB. It is off by default: the ordinary suite already covers the multi-chunk
// paths deterministically, and a 512 MiB round trip belongs to the release
// runbook, not to every CI run.
//
//	FILE_LARGE_ENVELOPE_MIB=512 go test ./internal/crypto/ -run LargeFile -v
//
// The procedure and the results of the last recorded run are in
// docs/runbooks/file-service-envelope-encryption.md.
const largeFileEnv = "FILE_LARGE_ENVELOPE_MIB"

// countingWriter is the destination the ciphertext is streamed into. It keeps a
// size and a digest, never the bytes: a validation that buffered the object
// would be measuring its own allocation instead of the envelope's.
type countingWriter struct {
	written int64
	digest  hash.Hash
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.written += int64(len(p))
	w.digest.Write(p)
	return len(p), nil
}

// patternReader generates n bytes on demand and hashes them, holding one
// caller-sized buffer and nothing else.
type patternReader struct {
	remaining int64
	next      byte
	digest    hash.Hash
}

func (r *patternReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		r.next = r.next*31 + 17
		p[i] = r.next
	}
	r.digest.Write(p)
	r.remaining -= int64(len(p))
	return len(p), nil
}

// TestLargeFileEnvelopeRoundTrip streams a large plaintext through the envelope
// and back, and reports the numbers the runbook records: sizes, both SHA-256
// digests, the wall time and the peak heap.
//
// The assertion that matters is the heap: encrypting and decrypting a file of
// any size must cost a bounded number of chunks, so the measured peak is
// compared against a ceiling that does not scale with the payload.
func TestLargeFileEnvelopeRoundTrip(t *testing.T) {
	raw := os.Getenv(largeFileEnv)
	if raw == "" {
		t.Skipf("set %s to the payload size in MiB to run the large-file validation", largeFileEnv)
	}
	sizeMiB, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || sizeMiB <= 0 {
		t.Fatalf("%s must be a positive number of MiB, got %q", largeFileEnv, raw)
	}
	size := sizeMiB << 20

	dataKey := newTestDataKey(t)
	attachmentID := uuid.New()

	source := &patternReader{remaining: size, digest: sha256.New()}
	encrypted, err := crypto.NewEncryptingReader(source, dataKey, attachmentID)
	if err != nil {
		t.Fatalf("build encrypting reader: %v", err)
	}
	// The decrypting reader consumes the encrypting one directly, so the
	// ciphertext is never stored anywhere in this process either.
	// The size is passed as the authenticated invariant it is in production: the
	// round trip must reach the final frame with exactly this many bytes.
	plaintext, err := crypto.NewDecryptingReader(encrypted, dataKey, attachmentID, size)
	if err != nil {
		t.Fatalf("build decrypting reader: %v", err)
	}
	sink := &countingWriter{digest: sha256.New()}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()

	copied, err := io.Copy(sink, plaintext)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)

	if copied != size {
		t.Fatalf("expected %d plaintext bytes back, got %d", size, copied)
	}
	uploadedSum := hex.EncodeToString(source.digest.Sum(nil))
	downloadedSum := hex.EncodeToString(sink.digest.Sum(nil))
	if uploadedSum != downloadedSum {
		t.Fatalf("SHA-256 mismatch: original %s, round trip %s", uploadedSum, downloadedSum)
	}

	// HeapAlloc is sampled after the copy, so it reports what the streams were
	// still holding rather than everything they ever touched. A construction
	// that buffered the object would sit at gigabytes here, not megabytes.
	const heapCeiling = 32 << 20
	heap := after.HeapAlloc
	t.Logf(
		"payload=%d MiB chunk=%d B envelope=%d B sha256(original)=%s sha256(round-trip)=%s "+
			"duration=%s heap_after=%d B heap_before=%d B",
		sizeMiB, crypto.ChunkSize, crypto.CiphertextSize(size),
		uploadedSum, downloadedSum, elapsed, heap, before.HeapAlloc,
	)
	if heap > heapCeiling {
		t.Fatalf("heap after a %d MiB round trip is %d bytes, ceiling is %d", sizeMiB, heap, heapCeiling)
	}
}
