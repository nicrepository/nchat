package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// rangeReader is a seekable plaintext view of one stored NCF1 object (RF-31).
//
// # Why this exists
//
// Playing a video means seeking, and seeking over an encrypted object used to be
// impossible here: the only reader was sequential, so answering "bytes
// 90000000-90065535" would have meant decrypting ninety megabytes to throw them
// away. That is the cost the whole feature exists to avoid, and it is what makes
// a Range header worth honouring rather than merely echoing.
//
// It is possible because the envelope's geometry is fixed: every chunk but the
// last holds exactly crypto.ChunkSize plaintext bytes and occupies a constant
// number of stored bytes, and no chunk depends on the one before it. So the
// chunk holding any offset, and where it starts in the object, are arithmetic —
// crypto.ChunkLocation — and reading it costs one storage request and one GCM
// open, whatever the offset.
//
// # What it does not relax
//
// Nothing above it. By the time one of these exists the caller has been
// authenticated, the attachment's visibility has been re-checked, the malware
// scan has been confirmed clean and the data key has been unwrapped against the
// authenticated plaintext length. A seek moves a cursor; it cannot reach an
// object the request was not already entitled to read.
//
// # Lifetime
//
// The context is held in the struct because io.Reader has nowhere to put one.
// It is the request's, so a client that hangs up cancels the storage read in
// flight, and Close releases whatever stream is open. Every path that replaces
// the open stream closes the old one first: a leaked body is a connection this
// process keeps until it dies.
type rangeReader struct {
	ctx     context.Context //nolint:containedctx // io.Reader has no ctx parameter; this is the request's.
	objects ObjectStore
	logger  *slog.Logger
	object  encryptedObject
	dataKey []byte
	// objectID is the parsed identity every chunk is authenticated against.
	objectID uuid.UUID

	// header is the object's magic and base nonce, fetched once and reused by
	// every subsequent seek. It is not secret and it is not authenticated on its
	// own — an edited base nonce makes every chunk's tag fail, which is the same
	// refusal by a different route.
	header []byte

	// pos is the logical plaintext cursor. stream is the decrypting reader
	// currently open and streamPos is the plaintext offset it will next yield,
	// so a seek that lands back where the open stream already is — which is
	// exactly what net/http does when it measures the size — costs nothing.
	pos       int64
	streamPos int64
	stream    io.Reader
	body      io.ReadCloser
}

// Read yields plaintext from the current position, opening the object at that
// position first if a seek moved away from the open stream.
func (r *rangeReader) Read(p []byte) (int, error) {
	if r.stream == nil || r.streamPos != r.pos {
		if err := r.openAt(r.pos); err != nil {
			return 0, err
		}
	}
	n, err := r.stream.Read(p)
	r.pos += int64(n)
	r.streamPos = r.pos
	return n, err
}

// Seek moves the plaintext cursor. It performs no I/O: the storage read is
// deferred to the next Read, so measuring the object — seek to the end, seek
// back — never opens anything.
//
// The cursor is allowed to sit exactly at the end of the plaintext, which is
// where a size measurement leaves it; it is not allowed past it, nor before the
// start.
func (r *rangeReader) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.pos + offset
	case io.SeekEnd:
		next = r.object.size + offset
	default:
		return 0, fmt.Errorf("%w: unknown seek whence", domain.ErrInvalidInput)
	}
	// Both bounds are checked against a length that was authenticated by the key
	// unwrap, so neither an edited row nor a hostile offset can move the cursor
	// somewhere the object does not reach.
	if next < 0 || next > r.object.size {
		return 0, fmt.Errorf("%w: seek out of range", domain.ErrInvalidInput)
	}
	r.pos = next
	return next, nil
}

// Close releases the storage stream. It is safe to call more than once.
func (r *rangeReader) Close() error {
	return r.closeStream()
}

func (r *rangeReader) closeStream() error {
	if r.body == nil {
		return nil
	}
	body := r.body
	r.body, r.stream = nil, nil
	return body.Close()
}

// openAt positions a fresh decrypting stream at a plaintext offset.
//
// Offset zero is deliberately not a special case of the general one: it reads
// the object from the start in a single request, exactly as a download always
// has, so the ordinary full transfer costs no extra round trip and its failure
// modes are unchanged. Only a genuine seek pays for the header fetch.
func (r *rangeReader) openAt(offset int64) error {
	if err := r.closeStream(); err != nil {
		return err
	}
	r.pos, r.streamPos = offset, offset
	if offset == 0 {
		return r.openFromStart()
	}
	return r.openFromChunk(offset)
}

func (r *rangeReader) openFromStart() error {
	body, err := openStoredObject(r.ctx, r.objects, r.logger, r.object)
	if err != nil {
		return err
	}
	// The authenticated length is handed to the reader as a second, independent
	// check: the binding proves the number was not edited, and the reader proves
	// the stored stream really produces it, all the way to the authenticated
	// final frame.
	plaintext, err := crypto.NewDecryptingReader(body, r.dataKey, r.objectID, r.object.size)
	if err != nil {
		_ = body.Close()
		return fmt.Errorf("prepare attachment envelope: %w", err)
	}
	r.body, r.stream = body, plaintext
	return nil
}

// openFromChunk reads the one chunk the offset lands in, and the chunks after
// it, without touching anything before.
func (r *rangeReader) openFromChunk(offset int64) error {
	if err := r.loadHeader(); err != nil {
		return err
	}
	chunkIndex, ciphertextOffset, skip := crypto.ChunkLocation(offset)
	body, err := r.objects.OpenRange(r.ctx, r.object.objectKey, ciphertextOffset)
	if err != nil {
		return r.storageError(err)
	}
	plaintext, err := crypto.NewChunkReader(
		r.header, body, r.dataKey, r.objectID, r.object.size, chunkIndex,
	)
	if err != nil {
		_ = body.Close()
		return fmt.Errorf("prepare attachment envelope: %w", err)
	}
	// The offset can land inside a chunk, and a chunk is the smallest thing that
	// can be authenticated, so its leading bytes are decrypted and dropped. That
	// is bounded by crypto.ChunkSize — 64 KiB — however far into the file the
	// seek went, which is the whole point of addressing chunks rather than bytes.
	if skip > 0 {
		if _, err := io.CopyN(io.Discard, plaintext, skip); err != nil {
			_ = body.Close()
			return fmt.Errorf("seek attachment content: %w", err)
		}
	}
	r.body, r.stream = body, plaintext
	return nil
}

// loadHeader fetches the object's magic and base nonce once. Every later seek
// reuses them, so seeking repeatedly — which is what a video player does — costs
// one storage request per seek, not two.
func (r *rangeReader) loadHeader() error {
	if r.header != nil {
		return nil
	}
	body, err := r.objects.OpenRange(r.ctx, r.object.objectKey, 0)
	if err != nil {
		return r.storageError(err)
	}
	defer func() { _ = body.Close() }()
	header := make([]byte, crypto.EnvelopeHeaderSize)
	if _, err := io.ReadFull(body, header); err != nil {
		return fmt.Errorf("%w: attachment object unreadable", domain.ErrUnavailable)
	}
	r.header = header
	return nil
}

// storageError keeps a ranged read's failures indistinguishable from a whole
// read's: metadata says the object exists, so a store that cannot produce it is
// an operational failure and never a client-visible "not found".
func (r *rangeReader) storageError(err error) error {
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	r.logger.LogAttrs(r.ctx, slog.LevelError, "attachment object missing in storage",
		slog.String("object_id", r.object.objectID),
		slog.String("workspace_id", r.object.workspaceID),
	)
	return fmt.Errorf("%w: attachment object unavailable", domain.ErrUnavailable)
}
