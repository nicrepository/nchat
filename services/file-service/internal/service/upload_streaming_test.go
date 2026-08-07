package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"sync/atomic"
	"testing"

	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// streamingPayloadBytes spans many chunks while staying fast enough for every
// CI run. The point is the ratio, not the absolute size: at 8 MiB the bound
// below is a thirty-second of the payload, so a reader that buffered the file
// would miss it by orders of magnitude.
const streamingPayloadBytes = 8 << 20

// maxBufferedBytes is how far the upload pipeline may get ahead of storage.
// The encrypting reader holds one plaintext chunk plus one sealed block, and
// the service holds the 512-byte sniff window, so four chunks is a generous
// ceiling that still fails immediately if anything starts accumulating.
const maxBufferedBytes = 4 * crypto.ChunkSize

// generatedSource produces a deterministic byte stream of a given length
// without ever materialising it, and hashes what it produced. It is what a real
// multipart body looks like to the service: a reader, not a slice.
type generatedSource struct {
	remaining int64
	next      byte
	read      atomic.Int64
	digest    hash.Hash
}

func newGeneratedSource(size int64) *generatedSource {
	return &generatedSource{remaining: size, digest: sha256.New()}
}

func (g *generatedSource) Read(p []byte) (int, error) {
	if g.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > g.remaining {
		p = p[:g.remaining]
	}
	for i := range p {
		// A cheap non-constant pattern: constant bytes would let a broken
		// chunk boundary go unnoticed in the round-trip comparison.
		g.next = g.next*31 + 17
		p[i] = g.next
	}
	g.digest.Write(p)
	g.remaining -= int64(len(p))
	g.read.Add(int64(len(p)))
	return len(p), nil
}

func (g *generatedSource) plaintextRead() int64 { return g.read.Load() }

func (g *generatedSource) sum() string { return hex.EncodeToString(g.digest.Sum(nil)) }

// probeObjectStore is an object store that checks, on every read, how far the
// upload pipeline has run ahead of it.
//
// A fake that drains the body with io.ReadAll — like fakeObjects — cannot see
// the difference between a streaming producer and one that buffered the whole
// file, because ReadAll buffers either way. This one consumes the body in fixed
// steps and compares the plaintext consumed from the source against the
// ciphertext it has accepted: the difference is exactly what the service is
// holding in memory, and it must stay bounded regardless of file size.
type probeObjectStore struct {
	source *generatedSource

	objects  map[string][]byte
	maxAhead int64
}

func newProbeObjectStore(source *generatedSource) *probeObjectStore {
	return &probeObjectStore{source: source, objects: map[string][]byte{}}
}

func (p *probeObjectStore) Put(_ context.Context, key string, body io.Reader) (int64, error) {
	// The test process keeps the ciphertext so the download can be checked. That
	// buffer is the test's, never the service's, and is not what is measured.
	var stored bytes.Buffer
	buf := make([]byte, 32*1024)
	var written int64
	for {
		n, err := body.Read(buf)
		if n > 0 {
			stored.Write(buf[:n])
			written += int64(n)
			if ahead := p.source.plaintextRead() - written; ahead > p.maxAhead {
				p.maxAhead = ahead
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	p.objects[key] = stored.Bytes()
	return written, nil
}

func (p *probeObjectStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	content, ok := p.objects[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (p *probeObjectStore) OpenRange(_ context.Context, key string, offset int64) (io.ReadCloser, error) {
	content, ok := p.objects[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if offset < 0 || offset > int64(len(content)) {
		return nil, domain.ErrInvalidInput
	}
	return io.NopCloser(bytes.NewReader(content[offset:])), nil
}

func (p *probeObjectStore) Delete(_ context.Context, key string) error {
	delete(p.objects, key)
	return nil
}

// TestUploadStreamsWithoutBufferingTheWholeFile is the RNF-17 memory claim as a
// test: an 8 MiB upload must never have more than a few chunks in flight, and
// the bytes that come back out must hash to the bytes that went in.
func TestUploadStreamsWithoutBufferingTheWholeFile(t *testing.T) {
	source := newGeneratedSource(streamingPayloadBytes)
	f := newFixture(t)
	objects := newProbeObjectStore(source)
	svc := service.NewAttachmentService(
		f.authorizer, f.store, objects, f.keys,
		domain.DefaultMaxUploadBytes, false, f.orphans, discardLogger(),
	)

	ctx := context.Background()
	target, err := svc.AuthorizeUpload(ctx, service.AuthorizeUploadInput{
		Destination: domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		UserID:      testUserID,
		SessionID:   testSessionID,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	view, err := svc.Upload(ctx, service.UploadInput{
		Target: target, Filename: "large.bin",
		DeclaredMIME: "application/octet-stream", Content: source,
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	if objects.maxAhead > maxBufferedBytes {
		t.Fatalf(
			"upload buffered %d bytes ahead of storage, ceiling is %d: the stream is not incremental",
			objects.maxAhead, maxBufferedBytes,
		)
	}
	if view.Size != streamingPayloadBytes {
		t.Fatalf("expected size %d, got %d", streamingPayloadBytes, view.Size)
	}

	// The stored object must not be the plaintext: its size is the envelope's,
	// which is larger by the header and one tag per chunk.
	_, uploaded, _ := f.store.snapshot()
	if want := crypto.CiphertextSize(streamingPayloadBytes); uploaded[0].CiphertextSize != want {
		t.Fatalf("expected a %d-byte envelope in storage, got %d", want, uploaded[0].CiphertextSize)
	}

	created, finalised, _ := f.store.snapshot()
	f.store.authorized = service.StoredAttachment{
		ID: view.ID, WorkspaceID: testWorkspaceID, Kind: domain.DestinationKindChannel,
		Status: domain.StatusClean, Filename: view.Filename, Size: view.Size,
		StorageObjectKey: created[0].StorageObjectKey,
		EnvelopeVersion:  created[0].EnvelopeVersion,
		WrappedDEK:       finalised[0].WrappedDEK,
		KEKKeyID:         finalised[0].KEKKeyID,
		KeyWrapVersion:   created[0].KeyWrapVersion,
	}
	download, err := svc.Download(ctx, downloadInput(view.ID))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	downloaded := sha256.New()
	copied, err := io.Copy(downloaded, download.Content)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	if copied != streamingPayloadBytes {
		t.Fatalf("expected %d plaintext bytes back, got %d", streamingPayloadBytes, copied)
	}
	if got := hex.EncodeToString(downloaded.Sum(nil)); got != source.sum() {
		t.Fatalf("SHA-256 mismatch: uploaded %s, downloaded %s", source.sum(), got)
	}
}
