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
//     another attachment fails. The wrapped data key is bound to the attachment,
//     its workspace and the key-encryption key that sealed it, so moving a row
//     between tenants or relabelling which key opened it fails too.
//   - Truncation: the last chunk is the only one sealed with the final marker
//     set. Dropping trailing chunks leaves a stream whose last chunk is not
//     final, and the reader fails instead of returning a short plaintext.
//   - Format confusion: the version magic is authenticated in every chunk, so a
//     future version's bytes cannot be replayed as v1.
//
// The format is versioned by the four-byte magic that opens the object and is
// bound into every chunk's associated data.
//
// # Key rotation
//
// Key-encryption keys are held in a Keyring: exactly one active key, which every
// new upload's data key is wrapped under, plus any number of previous keys kept
// only so already-stored objects can still be read. The non-secret id of the key
// that did the wrapping is returned by Wrap and persisted next to the wrapped
// data key, and Unwrap selects by that id alone — an unknown id is refused and no
// key is ever tried speculatively. Rotating therefore means re-wrapping a 32-byte
// data key, never re-encrypting the object.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// EnvelopeVersion is the version of the NCF1 content stream, persisted with
// every attachment so a future format can be introduced without re-encrypting
// existing objects.
const EnvelopeVersion = 1

// KeyWrapVersion is the version of the wrapped-DEK format, persisted separately
// from EnvelopeVersion because the two evolve independently: binding a new field
// into the key's associated data changes nothing about the object's bytes.
//
// Version 1 was the NCD1 binding, which did not authenticate the plaintext
// length. It is not supported: nothing was ever wrapped under it outside this
// branch, and migration 000002 refuses to run against a non-empty table rather
// than leaving rows only a removed parser could open.
const KeyWrapVersion = 2

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

	// EnvelopeHeaderSize is the number of leading bytes of a stored object that
	// carry the version magic and the base nonce. It is exported because random
	// access needs it: a reader that starts at a later chunk still has to know
	// the base nonce, which lives only here.
	EnvelopeHeaderSize = headerSize

	// dekAADSize is the fixed width of the wrapped-key associated data, laid out
	// in dekAAD. It is a constant precisely because no field is variable-length.
	dekAADSize = 4 + 2 + 2 + 16 + 16 + sha256.Size + 8
)

var magic = [4]byte{'N', 'C', 'F', '1'}

// dekMagic domain-separates key wrapping from content encryption, so a wrapped
// data key can never be opened as, or replaced by, a content chunk. The trailing
// digit tracks KeyWrapVersion, which is also authenticated as a field, so the
// two can never disagree about which binding is in force.
var dekMagic = [4]byte{'N', 'C', 'D', '2'}

// maxKeyIDLength bounds the non-secret key id. It matches the CHECK on
// files.attachments.kek_key_id, so a value the service accepts is a value the
// column can hold, and it is comfortably longer than a dated label like
// "kek-2026-08".
const maxKeyIDLength = 64

// keyIDPattern is the closed shape of a key id. It is persisted, logged and
// compared, so it stays lowercase and free of separators used by the
// configuration format.
var keyIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Sentinel errors. None of them carries key material, plaintext or storage
// detail; callers log the sentinel and nothing else.
var (
	// ErrInvalidKey rejects a master key of the wrong encoding or length, and a
	// key id of the wrong shape.
	ErrInvalidKey = errors.New("invalid encryption key")
	// ErrUnknownKey rejects a wrapped data key whose key id is not configured.
	// It is deliberately not ErrCiphertext: an operator who removed a key from
	// the ring needs to see that in a log, and the two are never distinguishable
	// to a client because the handler maps both to the same generic failure.
	ErrUnknownKey = errors.New("unknown key encryption key")
	// ErrCiphertext covers every integrity failure: a modified, truncated,
	// reordered, duplicated or substituted object, a wrong key, and a
	// malformed envelope. They are deliberately indistinguishable to a caller.
	ErrCiphertext = errors.New("ciphertext authentication failed")
	// ErrStreamTooLong rejects a plaintext that would overflow the chunk
	// counter before a nonce could repeat.
	ErrStreamTooLong = errors.New("stream exceeds addressable chunks")
	// ErrInvalidBinding rejects a binding this package cannot serialise: a
	// negative plaintext length, or a version outside the wire encoding. It is a
	// programming or data-integrity failure, never something a client causes.
	ErrInvalidBinding = errors.New("invalid key binding")
	// ErrUnsupportedVersion rejects a persisted format version this build does
	// not implement. Versions are never tried in sequence and there is no
	// fallback to an older binding.
	ErrUnsupportedVersion = errors.New("unsupported envelope version")
)

// Binding is the identity a wrapped data key is cryptographically tied to.
// Every field is server-derived and stable for the lifetime of an attachment,
// so all of them are available unchanged at upload and at download.
//
// Together with the key id — which enters the associated data as its SHA-256,
// see dekAAD — these fields are the whole authenticated binding. Changing any
// one of them in the database makes the wrapped data key fail to open, which is
// the point:
//
//   - AttachmentID and WorkspaceID stop a row being lifted into another
//     attachment or another tenant.
//   - PlaintextSize stops the recorded length being edited. Without it, an
//     attacker with write access to PostgreSQL could lower size_bytes, and the
//     handler would publish that smaller Content-Length and serve a prefix that
//     a client would accept as the whole file. Authenticating the length means
//     the unwrap fails before any header is written.
//   - KeyWrapVersion and ContentEnvelopeVersion stop a row being reinterpreted
//     under a different format.
type Binding struct {
	AttachmentID uuid.UUID
	WorkspaceID  uuid.UUID
	// PlaintextSize is the number of plaintext bytes actually read from the
	// upload stream — counted by the pipeline, never taken from Content-Length
	// or from any other client-supplied value — and it is the plaintext length,
	// not the size of the stored envelope.
	PlaintextSize int64
	// KeyWrapVersion is the version of this binding's own format, and
	// ContentEnvelopeVersion the version of the NCF1 object it protects. They
	// are separate fields because they version separate things.
	KeyWrapVersion         int
	ContentEnvelopeVersion int
}

// wireBinding is a Binding whose values have been checked and converted to the
// exact widths dekAAD writes. Doing the conversion once, behind a validation
// that rejects everything out of range, keeps the encoder free of casts that
// could silently truncate.
type wireBinding struct {
	keyWrapVersion         uint16
	contentEnvelopeVersion uint16
	plaintextSize          uint64
}

// validate refuses any binding that cannot be encoded, and any version this
// build does not implement. Wrap and Unwrap both go through it, so the two
// directions cannot disagree about what is representable.
func (b Binding) validate() (wireBinding, error) {
	if b.PlaintextSize < 0 {
		return wireBinding{}, fmt.Errorf("%w: plaintext size must not be negative", ErrInvalidBinding)
	}
	if b.KeyWrapVersion != KeyWrapVersion {
		return wireBinding{}, fmt.Errorf(
			"%w: key wrap version %d", ErrUnsupportedVersion, b.KeyWrapVersion,
		)
	}
	if b.ContentEnvelopeVersion != EnvelopeVersion {
		return wireBinding{}, fmt.Errorf(
			"%w: content envelope version %d", ErrUnsupportedVersion, b.ContentEnvelopeVersion,
		)
	}
	// Both versions are now known to equal a small constant, and the size is
	// known to be non-negative, so neither conversion below can lose a value.
	return wireBinding{
		keyWrapVersion:         uint16(b.KeyWrapVersion),         //nolint:gosec // G115: checked equal to KeyWrapVersion above.
		contentEnvelopeVersion: uint16(b.ContentEnvelopeVersion), //nolint:gosec // G115: checked equal to EnvelopeVersion above.
		plaintextSize:          uint64(b.PlaintextSize),          //nolint:gosec // G115: checked non-negative above.
	}, nil
}

// keyEncryptionKey wraps and unwraps per-object data keys under one master key.
// It is never used directly outside this file: callers hold a Keyring, so a
// wrapped key can only ever be opened through the id that identifies its key.
type keyEncryptionKey struct {
	aead cipher.AEAD
}

// Keyring is the set of key-encryption keys this process may use: exactly one
// active key, under which every new data key is wrapped, and zero or more
// previous keys retained only for reading objects wrapped before a rotation.
//
// There is no default key and no fallback. A zero Keyring cannot encrypt, an
// unknown key id is refused, and no key is ever tried speculatively.
type Keyring struct {
	activeID string
	keys     map[string]*keyEncryptionKey
}

// NewKeyring builds the key ring from the active key and the previous keys kept
// for reading.
//
// activeKey is standard base64 of exactly KeySize bytes. previous is the same
// encoding, one "id:key" pair per comma-separated entry, and may be empty. Every
// value is validated here and nothing is defaulted: a missing, mis-encoded,
// wrong-length or duplicate entry is an error, never a weaker ring.
//
// The key material itself never reaches an error message.
func NewKeyring(activeID, activeKey, previous string) (*Keyring, error) {
	id, err := validateKeyID(activeID)
	if err != nil {
		return nil, err
	}
	active, err := newKeyEncryptionKey(activeKey)
	if err != nil {
		return nil, err
	}
	ring := &Keyring{activeID: id, keys: map[string]*keyEncryptionKey{id: active}}
	if err := ring.addPrevious(previous); err != nil {
		return nil, err
	}
	return ring, nil
}

// addPrevious parses the read-only half of the ring. An entry that repeats an id
// already present — including the active one — is refused rather than silently
// overriding it: which key opens an object must never depend on parse order.
func (r *Keyring) addPrevious(previous string) error {
	for _, entry := range strings.Split(previous, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		rawID, rawKey, found := strings.Cut(entry, ":")
		if !found {
			return fmt.Errorf("%w: previous keys must be id:key pairs", ErrInvalidKey)
		}
		id, err := validateKeyID(rawID)
		if err != nil {
			return err
		}
		if _, exists := r.keys[id]; exists {
			return fmt.Errorf("%w: duplicate key id", ErrInvalidKey)
		}
		key, err := newKeyEncryptionKey(rawKey)
		if err != nil {
			return err
		}
		r.keys[id] = key
	}
	return nil
}

// ValidateKeyring reports whether the configured keys are usable. Configuration
// validation uses it so a broken key is refused at start-up rather than at the
// first upload; it builds the same ring New would, so the two cannot drift.
func ValidateKeyring(activeID, activeKey, previous string) error {
	_, err := NewKeyring(activeID, activeKey, previous)
	return err
}

// ActiveKeyID is the non-secret id of the key new uploads are wrapped under.
func (r *Keyring) ActiveKeyID() string {
	if r == nil {
		return ""
	}
	return r.activeID
}

// Wrap seals a data key under the active key and returns it with the id that
// has to be persisted alongside it. The returned bytes are nonce || ciphertext
// and are the only form persisted.
func (r *Keyring) Wrap(dataKey []byte, binding Binding) (wrapped []byte, keyID string, err error) {
	if r == nil || r.keys == nil {
		return nil, "", fmt.Errorf("%w: key ring unavailable", ErrInvalidKey)
	}
	active, ok := r.keys[r.activeID]
	if !ok {
		return nil, "", fmt.Errorf("%w: key ring has no active key", ErrInvalidKey)
	}
	sealed, err := active.wrap(dataKey, r.activeID, binding)
	if err != nil {
		return nil, "", err
	}
	return sealed, r.activeID, nil
}

// Unwrap opens a wrapped data key using the key named by keyID and nothing else.
// An id that is not on the ring yields ErrUnknownKey; every other failure —
// wrong key, tampered bytes, another attachment, another workspace — yields
// ErrCiphertext and is indistinguishable.
func (r *Keyring) Unwrap(wrapped []byte, keyID string, binding Binding) ([]byte, error) {
	if r == nil || r.keys == nil {
		return nil, fmt.Errorf("%w: key ring unavailable", ErrInvalidKey)
	}
	key, ok := r.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: key id is not configured", ErrUnknownKey)
	}
	return key.unwrap(wrapped, keyID, binding)
}

// newKeyEncryptionKey decodes a standard-base64 master key and validates its
// length. It never logs or echoes the key, and it fails closed: an absent,
// mis-encoded or wrong-length key yields an error, never a weaker key.
func newKeyEncryptionKey(base64Key string) (*keyEncryptionKey, error) {
	key, err := decodeMasterKey(base64Key)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &keyEncryptionKey{aead: aead}, nil
}

// validateKeyID accepts only the closed shape a persisted, logged and compared
// identifier may have. The id is not secret, but it selects a key, so nothing
// about it is inferred or normalised beyond trimming surrounding whitespace.
func validateKeyID(keyID string) (string, error) {
	trimmed := strings.TrimSpace(keyID)
	if trimmed == "" {
		return "", fmt.Errorf("%w: key id is required", ErrInvalidKey)
	}
	if len(trimmed) > maxKeyIDLength || !keyIDPattern.MatchString(trimmed) {
		return "", fmt.Errorf(
			"%w: key id must match %s", ErrInvalidKey, keyIDPattern.String(),
		)
	}
	return trimmed, nil
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

// wrap seals a data key for one attachment under one key. The returned value is
// nonce || ciphertext and is the only form persisted.
func (k *keyEncryptionKey) wrap(dataKey []byte, keyID string, binding Binding) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, fmt.Errorf("%w: key encryption key unavailable", ErrInvalidKey)
	}
	if len(dataKey) != KeySize {
		return nil, fmt.Errorf("%w: data key must be %d bytes", ErrInvalidKey, KeySize)
	}
	wire, err := binding.validate()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate wrap nonce: %w", err)
	}
	sealed := k.aead.Seal(nil, nonce, dataKey, dekAAD(keyID, binding, wire))
	return append(nonce, sealed...), nil
}

// unwrap opens a wrapped data key. A key bound to a different attachment, a
// different workspace or a different key id, or sealed under different key
// material, fails with ErrCiphertext.
func (k *keyEncryptionKey) unwrap(wrapped []byte, keyID string, binding Binding) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, fmt.Errorf("%w: key encryption key unavailable", ErrInvalidKey)
	}
	// The persisted versions decide which binding is reconstructed. An
	// unsupported one stops here: no other version is attempted, and there is no
	// fallback to the pre-size-binding format.
	wire, err := binding.validate()
	if err != nil {
		return nil, err
	}
	if len(wrapped) < nonceSize+tagSize {
		return nil, fmt.Errorf("%w: wrapped key is malformed", ErrCiphertext)
	}
	dataKey, err := k.aead.Open(nil, wrapped[:nonceSize], wrapped[nonceSize:], dekAAD(keyID, binding, wire))
	if err != nil {
		return nil, fmt.Errorf("%w: wrapped key", ErrCiphertext)
	}
	if len(dataKey) != KeySize {
		return nil, fmt.Errorf("%w: wrapped key length", ErrCiphertext)
	}
	return dataKey, nil
}

// dekAAD binds a wrapped data key to both format versions, the attachment, its
// workspace, the id of the key that sealed it and the exact plaintext length of
// the object.
//
// The layout is fixed-width throughout, 80 bytes:
//
//	"NCD2"                          4  domain separation from a content chunk
//	key wrap version                2  big endian
//	content envelope version        2  big endian
//	attachment id                  16  raw UUID
//	workspace id                   16  raw UUID
//	SHA-256(key id)                32
//	plaintext size                  8  big endian, bytes
//
// Because nothing is variable-length, no two different bindings can produce the
// same associated data and no length prefix is needed: a key id ending in a
// UUID-shaped suffix cannot be made to look like another workspace, which a
// textual join with separators would allow. The key id enters as a digest for
// framing, not for secrecy — the id is public.
func dekAAD(keyID string, binding Binding, wire wireBinding) []byte {
	keyIDDigest := sha256.Sum256([]byte(keyID))
	aad := make([]byte, 0, dekAADSize)
	aad = append(aad, dekMagic[:]...)
	aad = binary.BigEndian.AppendUint16(aad, wire.keyWrapVersion)
	aad = binary.BigEndian.AppendUint16(aad, wire.contentEnvelopeVersion)
	aad = append(aad, binding.AttachmentID[:]...)
	aad = append(aad, binding.WorkspaceID[:]...)
	aad = append(aad, keyIDDigest[:]...)
	return binary.BigEndian.AppendUint64(aad, wire.plaintextSize)
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

// ChunkLocation maps a plaintext offset onto the stored envelope.
//
// Random access is possible at all because every chunk but the last holds
// exactly ChunkSize plaintext bytes and is stored as exactly blockSize bytes,
// so the chunk holding any offset — and where that chunk starts in the object —
// are pure arithmetic. Nothing has to be decrypted to find them, and no chunk
// depends on the one before it: the nonce is baseNonce||index and the
// associated data names the index outright.
//
// It returns the index of the chunk containing plaintextOffset, the byte offset
// of that chunk inside the stored object, and how many plaintext bytes of that
// chunk precede the requested offset and must be discarded.
//
// plaintextOffset must not be negative. An offset at or past the end of the
// plaintext still names a real chunk — the final one — because the writer always
// closes the stream with a short chunk, empty when the length is a whole
// multiple of ChunkSize.
func ChunkLocation(plaintextOffset int64) (chunkIndex uint32, ciphertextOffset, skip int64) {
	if plaintextOffset < 0 {
		plaintextOffset = 0
	}
	index := plaintextOffset / ChunkSize
	// An index this large cannot exist: the writer refuses to seal a stream whose
	// counter would reach MaxUint32, so no stored object addresses that far.
	if index > math.MaxUint32 {
		index = math.MaxUint32
	}
	return uint32(index), //nolint:gosec // G115: clamped to MaxUint32 above.
		int64(headerSize) + index*int64(blockSize),
		plaintextOffset % ChunkSize
}

// NewChunkReader returns a plaintext reader over an envelope's chunks starting
// at firstChunk, for a caller that has already read the object's header and is
// supplying the ciphertext from ChunkLocation's offset.
//
// It is the random-access form of NewDecryptingReader and gives up exactly one
// guarantee, unavoidably: a reader that stops before the authenticated final
// frame cannot notice that the object's tail was truncated. Everything else
// holds unchanged — every chunk it does yield is authenticated against the
// object's id, its own index in the stream and the format version, so a chunk
// moved, duplicated, lifted from another object or edited in any byte fails.
//
// The header's own bytes are not authenticated directly, and do not need to be:
// an edited base nonce simply makes every chunk's tag fail to verify.
func NewChunkReader(
	header []byte, chunks io.Reader, dataKey []byte,
	objectID uuid.UUID, plaintextSize int64, firstChunk uint32,
) (io.Reader, error) {
	reader, err := newDecryptingReader(chunks, dataKey, objectID, plaintextSize)
	if err != nil {
		return nil, err
	}
	if len(header) != headerSize || string(header[:len(magic)]) != string(magic[:]) {
		return nil, fmt.Errorf("%w: unsupported envelope", ErrCiphertext)
	}
	// Every chunk before the first one requested is a full chunk, by the format's
	// own definition, so the plaintext already skipped is exact rather than
	// assumed. Seeding the counter with it keeps the length invariant meaningful:
	// a stream that runs past the authenticated size, or ends short of it, still
	// fails.
	produced := int64(firstChunk) * ChunkSize
	if produced > plaintextSize {
		return nil, fmt.Errorf("%w: chunk beyond plaintext length", ErrCiphertext)
	}
	copy(reader.baseNonce[:], header[len(magic):])
	reader.headerRead = true
	reader.index = firstChunk
	reader.produced = produced
	return reader, nil
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
//
// plaintextSize is the length the caller believes the object has — in practice
// the authenticated size from the wrapped data key's binding. It is an
// invariant checked against the stream, never a limit that ends it: the reader
// keeps consuming until the authenticated final frame, and only then does it
// require the totals to agree. Returning io.EOF at plaintextSize instead would
// turn a shortened length into a silently truncated file, which is exactly the
// failure the binding exists to prevent.
func NewDecryptingReader(
	src io.Reader, dataKey []byte, attachmentID uuid.UUID, plaintextSize int64,
) (io.Reader, error) {
	return newDecryptingReader(src, dataKey, attachmentID, plaintextSize)
}

func newDecryptingReader(
	src io.Reader, dataKey []byte, attachmentID uuid.UUID, plaintextSize int64,
) (*decryptingReader, error) {
	if plaintextSize < 0 {
		return nil, fmt.Errorf("%w: plaintext size must not be negative", ErrInvalidBinding)
	}
	aead, err := newAEAD(dataKey)
	if err != nil {
		return nil, err
	}
	return &decryptingReader{
		src:          src,
		aead:         aead,
		attachmentID: attachmentID,
		expected:     plaintextSize,
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
	// expected is the authenticated plaintext length; produced counts what the
	// stream has actually yielded so far.
	expected   int64
	produced   int64
	block      []byte
	buf        []byte
	offset     int
	headerRead bool
	done       bool
	err        error
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
	// The chunk is authentic, so the count it contributes is authentic too. Both
	// comparisons are made before the bytes are handed out: an object longer
	// than its recorded length fails as soon as it overruns, and one that is
	// shorter fails when the authenticated end of the stream arrives early.
	produced := r.produced + int64(len(plain))
	if produced > r.expected || (final && produced != r.expected) {
		r.err = fmt.Errorf("%w: plaintext length", ErrCiphertext)
		return
	}
	r.produced = produced
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
