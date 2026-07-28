// Package crypto implements the file-service envelope encryption format
// (RF-33 / RNF-17): a per-object data key sealed under a configured master key,
// and a chunked AES-256-GCM content stream that can be produced and consumed
// without holding the plaintext in memory.
//
// # Why chunks
//
// A single crypto/cipher.AEAD Seal authenticates one complete message, which
// would require buffering the whole file — up to the configured cap — in RAM.
// The stream is therefore split into fixed-size plaintext chunks, each sealed
// independently, in the STREAM construction of Hoang, Reyhanitabar, Rogaway and
// Vizár ("Online Authenticated-Encryption and its Nonce-Reuse Misuse-Resistance",
// CRYPTO 2015). Only standard library primitives are used; no cipher is
// implemented here.
//
// Each property the format has to provide, and where it comes from:
//
//   - Confidentiality and per-chunk integrity: AES-256-GCM with a 32-byte data
//     key drawn from crypto/rand, unique per object.
//   - Nonce uniqueness: nonce = 8 random base bytes || big-endian uint32 chunk
//     index. The base is fresh per object and the counter never repeats within
//     one object, so no (key, nonce) pair is ever reused. The counter is
//     rejected before it can wrap.
//   - Modification: any edited byte fails that chunk's GCM tag.
//   - Reordering and duplication: the chunk index is authenticated as
//     associated data, so a chunk opened at a different position fails.
//   - Substitution across objects: the attachment UUID is authenticated as
//     associated data and the data key is per-object, so a chunk lifted from
//     another attachment fails.
//   - Truncation: the last chunk is the only one sealed with the final marker
//     set. Dropping trailing chunks leaves a stream whose last chunk is not
//     final, and the reader fails instead of returning a short plaintext.
//   - Format confusion: the version magic is authenticated in every chunk, so a
//     future version's bytes cannot be replayed as v1.
//
// The format is versioned by the four-byte magic that opens the object and is
// bound into every chunk's associated data.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/google/uuid"
)

// EnvelopeVersion is persisted with every attachment so a future format can be
// introduced without re-encrypting existing objects.
const EnvelopeVersion = 1

const (
	// ChunkSize is the plaintext size of every chunk but the last. It is part
	// of the format: a reader derives chunk boundaries from it, so changing it
	// requires a new EnvelopeVersion.
	ChunkSize = 64 * 1024

	// KeySize is the AES-256 key length, for both the master key and the
	// per-object data key.
	KeySize = 32

	nonceSize     = 12 // crypto/cipher GCM standard nonce
	tagSize       = 16 // GCM authentication tag
	baseNonceSize = nonceSize - 4
	headerSize    = len(magic) + baseNonceSize
	blockSize     = ChunkSize + tagSize
)

var magic = [4]byte{'N', 'C', 'F', '1'}

// dekAAD domain-separates key wrapping from content encryption, so a wrapped
// data key can never be opened as, or replaced by, a content chunk.
const dekAADPrefix = "nchat:file:dek:v1:"

// Sentinel errors. None of them carries key material, plaintext or storage
// detail; callers log the sentinel and nothing else.
var (
	// ErrInvalidKey rejects a master key of the wrong encoding or length.
	ErrInvalidKey = errors.New("invalid encryption key")
	// ErrCiphertext covers every integrity failure: a modified, truncated,
	// reordered, duplicated or substituted object, a wrong key, and a
	// malformed envelope. They are deliberately indistinguishable to a caller.
	ErrCiphertext = errors.New("ciphertext authentication failed")
	// ErrStreamTooLong rejects a plaintext that would overflow the chunk
	// counter before a nonce could repeat.
	ErrStreamTooLong = errors.New("stream exceeds addressable chunks")
)

// KeyEncryptionKey wraps and unwraps per-object data keys under the configured
// master key. There is no default and no fallback: a nil KEK cannot encrypt.
type KeyEncryptionKey struct {
	aead cipher.AEAD
}

// NewKeyEncryptionKey decodes a standard-base64 master key and validates its
// length. It never logs or echoes the key, and it fails closed: an absent,
// mis-encoded or wrong-length key yields an error, never a weaker key.
func NewKeyEncryptionKey(base64Key string) (*KeyEncryptionKey, error) {
	key, err := decodeMasterKey(base64Key)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &KeyEncryptionKey{aead: aead}, nil
}

// ValidateMasterKey reports whether a configured master key is usable, without
// building or retaining a cipher. Configuration validation uses it so a broken
// key is refused at start-up rather than at the first upload.
func ValidateMasterKey(base64Key string) error {
	_, err := decodeMasterKey(base64Key)
	return err
}

func decodeMasterKey(base64Key string) ([]byte, error) {
	trimmed := strings.TrimSpace(base64Key)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: master key is required", ErrInvalidKey)
	}
	key, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: master key must be standard base64", ErrInvalidKey)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: master key must decode to %d bytes", ErrInvalidKey, KeySize)
	}
	return key, nil
}

// NewDataKey returns a fresh 32-byte data key. Data keys are never derived from
// a filename, an identifier, a timestamp or a password.
func NewDataKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate data key: %w", err)
	}
	return key, nil
}

// Wrap seals a data key for the given attachment. The returned value is
// nonce || ciphertext and is the only form persisted.
func (k *KeyEncryptionKey) Wrap(dataKey []byte, attachmentID uuid.UUID) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, fmt.Errorf("%w: key encryption key unavailable", ErrInvalidKey)
	}
	if len(dataKey) != KeySize {
		return nil, fmt.Errorf("%w: data key must be %d bytes", ErrInvalidKey, KeySize)
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate wrap nonce: %w", err)
	}
	sealed := k.aead.Seal(nil, nonce, dataKey, dekAAD(attachmentID))
	return append(nonce, sealed...), nil
}

// Unwrap opens a wrapped data key. A wrapped key bound to a different
// attachment, or sealed under a different master key, fails with ErrCiphertext.
func (k *KeyEncryptionKey) Unwrap(wrapped []byte, attachmentID uuid.UUID) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, fmt.Errorf("%w: key encryption key unavailable", ErrInvalidKey)
	}
	if len(wrapped) < nonceSize+tagSize {
		return nil, fmt.Errorf("%w: wrapped key is malformed", ErrCiphertext)
	}
	dataKey, err := k.aead.Open(nil, wrapped[:nonceSize], wrapped[nonceSize:], dekAAD(attachmentID))
	if err != nil {
		return nil, fmt.Errorf("%w: wrapped key", ErrCiphertext)
	}
	if len(dataKey) != KeySize {
		return nil, fmt.Errorf("%w: wrapped key length", ErrCiphertext)
	}
	return dataKey, nil
}

func dekAAD(attachmentID uuid.UUID) []byte {
	return []byte(dekAADPrefix + attachmentID.String())
}

// CiphertextSize returns the exact stored size for a plaintext of n bytes. It
// mirrors the writer so callers can assert what storage reported without
// re-reading the object.
func CiphertextSize(plaintextSize int64) int64 {
	fullChunks := plaintextSize / ChunkSize
	remainder := plaintextSize % ChunkSize
	// Every plaintext ends with exactly one final chunk, which is empty when the
	// plaintext length is a whole multiple of ChunkSize.
	return int64(headerSize) + fullChunks*int64(blockSize) + remainder + tagSize
}

// NewEncryptingReader returns a reader that yields the encrypted envelope for
// src. Nothing is buffered beyond a single chunk, so a 50 MiB upload costs
// 64 KiB of memory. Errors from src are propagated unchanged so a caller can
// tell a client-side stream failure from a storage failure.
func NewEncryptingReader(src io.Reader, dataKey []byte, attachmentID uuid.UUID) (io.Reader, error) {
	aead, err := newAEAD(dataKey)
	if err != nil {
		return nil, err
	}
	reader := &encryptingReader{
		src:          src,
		aead:         aead,
		attachmentID: attachmentID,
		plain:        make([]byte, ChunkSize),
		buf:          make([]byte, 0, headerSize+blockSize),
	}
	if _, err := io.ReadFull(rand.Reader, reader.baseNonce[:]); err != nil {
		return nil, fmt.Errorf("generate base nonce: %w", err)
	}
	reader.buf = append(reader.buf, magic[:]...)
	reader.buf = append(reader.buf, reader.baseNonce[:]...)
	return reader, nil
}

type encryptingReader struct {
	src          io.Reader
	aead         cipher.AEAD
	attachmentID uuid.UUID
	baseNonce    [baseNonceSize]byte
	index        uint32
	plain        []byte
	buf          []byte
	offset       int
	done         bool
	err          error
}

func (r *encryptingReader) Read(p []byte) (int, error) {
	for r.offset == len(r.buf) {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		r.buf, r.offset = r.buf[:0], 0
		r.fill()
	}
	n := copy(p, r.buf[r.offset:])
	r.offset += n
	return n, nil
}

// fill seals exactly one chunk.
//
// A short read ends the stream. A read that fills the buffer exactly is treated
// as non-final even when it happens to be the end of src: the following
// iteration then reads zero bytes and seals an empty final chunk. That keeps
// "the last chunk is the final chunk" true for every plaintext length,
// including zero and exact multiples of ChunkSize, without look-ahead.
func (r *encryptingReader) fill() {
	if r.index == math.MaxUint32 {
		r.err = ErrStreamTooLong
		return
	}
	n, err := io.ReadFull(r.src, r.plain)
	switch {
	case err == nil:
		r.buf = r.aead.Seal(r.buf, r.nonce(), r.plain[:n], r.aad(false))
		r.index++
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		r.buf = r.aead.Seal(r.buf, r.nonce(), r.plain[:n], r.aad(true))
		r.done = true
	default:
		r.err = err
	}
}

func (r *encryptingReader) nonce() []byte {
	return chunkNonce(r.baseNonce, r.index)
}

func (r *encryptingReader) aad(final bool) []byte {
	return chunkAAD(r.attachmentID, r.index, final)
}

// NewDecryptingReader returns a reader that yields the plaintext of an envelope
// produced by NewEncryptingReader. Every integrity failure surfaces as
// ErrCiphertext; a caller can never distinguish tampering from truncation from
// a wrong key, and must never serve partial output as success.
func NewDecryptingReader(src io.Reader, dataKey []byte, attachmentID uuid.UUID) (io.Reader, error) {
	aead, err := newAEAD(dataKey)
	if err != nil {
		return nil, err
	}
	return &decryptingReader{
		src:          src,
		aead:         aead,
		attachmentID: attachmentID,
		block:        make([]byte, blockSize),
		buf:          make([]byte, 0, ChunkSize),
	}, nil
}

type decryptingReader struct {
	src          io.Reader
	aead         cipher.AEAD
	attachmentID uuid.UUID
	baseNonce    [baseNonceSize]byte
	index        uint32
	block        []byte
	buf          []byte
	offset       int
	headerRead   bool
	done         bool
	err          error
}

func (r *decryptingReader) Read(p []byte) (int, error) {
	for r.offset == len(r.buf) {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		r.buf, r.offset = r.buf[:0], 0
		r.fill()
	}
	n := copy(p, r.buf[r.offset:])
	r.offset += n
	return n, nil
}

func (r *decryptingReader) fill() {
	if !r.headerRead {
		if !r.readHeader() {
			return
		}
	}
	if r.index == math.MaxUint32 {
		r.err = ErrStreamTooLong
		return
	}

	n, err := io.ReadFull(r.src, r.block)
	switch {
	case err == nil:
		// A full block cannot be the final chunk: the writer always ends with a
		// short one, even if it is only a tag.
		r.open(r.block[:n], false)
	case errors.Is(err, io.ErrUnexpectedEOF):
		r.open(r.block[:n], true)
		r.done = true
	case errors.Is(err, io.EOF):
		// The stream ended on a chunk boundary, so the chunk that was supposed
		// to close it is missing: the object was truncated.
		r.err = fmt.Errorf("%w: truncated stream", ErrCiphertext)
	default:
		r.err = err
	}
}

func (r *decryptingReader) readHeader() bool {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r.src, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			r.err = fmt.Errorf("%w: truncated header", ErrCiphertext)
			return false
		}
		r.err = err
		return false
	}
	if string(header[:len(magic)]) != string(magic[:]) {
		r.err = fmt.Errorf("%w: unsupported envelope", ErrCiphertext)
		return false
	}
	copy(r.baseNonce[:], header[len(magic):])
	r.headerRead = true
	return true
}

func (r *decryptingReader) open(block []byte, final bool) {
	if len(block) < tagSize {
		r.err = fmt.Errorf("%w: truncated chunk", ErrCiphertext)
		return
	}
	plain, err := r.aead.Open(
		r.buf[:0],
		chunkNonce(r.baseNonce, r.index),
		block,
		chunkAAD(r.attachmentID, r.index, final),
	)
	if err != nil {
		r.err = fmt.Errorf("%w: chunk %d", ErrCiphertext, r.index)
		return
	}
	r.buf = plain
	r.index++
}

func chunkNonce(base [baseNonceSize]byte, index uint32) []byte {
	nonce := make([]byte, nonceSize)
	copy(nonce, base[:])
	binary.BigEndian.PutUint32(nonce[baseNonceSize:], index)
	return nonce
}

// chunkAAD binds a chunk to the format version, the attachment it belongs to,
// its position in the stream and whether it closes the stream.
func chunkAAD(attachmentID uuid.UUID, index uint32, final bool) []byte {
	aad := make([]byte, 0, len(magic)+len(attachmentID)+4+1)
	aad = append(aad, magic[:]...)
	aad = append(aad, attachmentID[:]...)
	aad = binary.BigEndian.AppendUint32(aad, index)
	if final {
		return append(aad, 1)
	}
	return append(aad, 0)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: key must be %d bytes", ErrInvalidKey, KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	return aead, nil
}
