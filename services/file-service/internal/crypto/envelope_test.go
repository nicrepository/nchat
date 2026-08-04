package crypto_test

import (
	"bytes"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
)

// testMasterKey builds a random key at run time. No base64 key literal is
// committed anywhere, in tests included.
func testMasterKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(cryptorand.Reader, key); err != nil {
		t.Fatalf("generate master key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

// testKeyID is the active key id every ring in this file uses unless the case
// is about rotation itself.
const testKeyID = "kek-test-active"

func newTestKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	return newTestKeyringWith(t, testKeyID, testMasterKey(t), "")
}

func newTestKeyringWith(t *testing.T, activeID, activeKey, previous string) *crypto.Keyring {
	t.Helper()
	ring, err := crypto.NewKeyring(activeID, activeKey, previous)
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}
	return ring
}

// testBinding is a fresh, fully populated binding: a new attachment, a new
// workspace, a plaintext length and the current pair of format versions.
func testBinding() crypto.Binding {
	return bindingOfSize(1024)
}

func bindingOfSize(size int64) crypto.Binding {
	return crypto.Binding{
		AttachmentID:           uuid.New(),
		WorkspaceID:            uuid.New(),
		PlaintextSize:          size,
		KeyWrapVersion:         crypto.KeyWrapVersion,
		ContentEnvelopeVersion: crypto.EnvelopeVersion,
	}
}

// withAttachment and withWorkspace copy a binding with one field changed, so a
// test can express "the same key, claimed for something else" without restating
// every field.
func withAttachment(b crypto.Binding, id uuid.UUID) crypto.Binding {
	b.AttachmentID = id
	return b
}

func withWorkspace(b crypto.Binding, id uuid.UUID) crypto.Binding {
	b.WorkspaceID = id
	return b
}

func withSize(b crypto.Binding, size int64) crypto.Binding {
	b.PlaintextSize = size
	return b
}

func newTestDataKey(t *testing.T) []byte {
	t.Helper()
	key, err := crypto.NewDataKey()
	if err != nil {
		t.Fatalf("generate data key: %v", err)
	}
	return key
}

func encrypt(t *testing.T, plaintext []byte, dataKey []byte, id uuid.UUID) []byte {
	t.Helper()
	reader, err := crypto.NewEncryptingReader(bytes.NewReader(plaintext), dataKey, id)
	if err != nil {
		t.Fatalf("build encrypting reader: %v", err)
	}
	ciphertext, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return ciphertext
}

// decrypt opens an envelope against the plaintext length the caller says it
// should have — in production, the length authenticated by the wrapped data key.
func decrypt(t *testing.T, ciphertext []byte, dataKey []byte, id uuid.UUID, size int64) ([]byte, error) {
	t.Helper()
	reader, err := crypto.NewDecryptingReader(bytes.NewReader(ciphertext), dataKey, id, size)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(cryptorand.Reader, buf); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	return buf
}

// --- key ring -----------------------------------------------------------

func TestNewKeyringRejectsBadActiveKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "whitespace", key: "   "},
		{name: "not base64", key: "not base64!!"},
		{name: "31 bytes", key: base64.StdEncoding.EncodeToString(make([]byte, 31))},
		{name: "33 bytes", key: base64.StdEncoding.EncodeToString(make([]byte, 33))},
		{name: "16 bytes", key: base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{name: "64 bytes", key: base64.StdEncoding.EncodeToString(make([]byte, 64))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := crypto.NewKeyring(testKeyID, tt.key, ""); !errors.Is(err, crypto.ErrInvalidKey) {
				t.Fatalf("expected ErrInvalidKey, got %v", err)
			}
			if err := crypto.ValidateKeyring(testKeyID, tt.key, ""); !errors.Is(err, crypto.ErrInvalidKey) {
				t.Fatalf("expected ValidateKeyring to reject, got %v", err)
			}
		})
	}
}

func TestNewKeyringRejectsBadKeyIDs(t *testing.T) {
	key := testMasterKey(t)
	for _, keyID := range []string{
		"", "   ", "Uppercase", "has space", "-leading-dash", "colon:inside",
		"comma,inside", strings.Repeat("k", 65),
	} {
		t.Run(keyID, func(t *testing.T) {
			if _, err := crypto.NewKeyring(keyID, key, ""); !errors.Is(err, crypto.ErrInvalidKey) {
				t.Fatalf("expected ErrInvalidKey for key id %q, got %v", keyID, err)
			}
		})
	}
}

func TestValidateKeyringAcceptsACorrectRing(t *testing.T) {
	if err := crypto.ValidateKeyring(testKeyID, testMasterKey(t), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	previous := "kek-2026-01:" + testMasterKey(t) + ", kek-2025-07:" + testMasterKey(t)
	if err := crypto.ValidateKeyring(testKeyID, testMasterKey(t), previous); err != nil {
		t.Fatalf("unexpected error for a rotating ring: %v", err)
	}
}

func TestNewKeyringRejectsMalformedPreviousKeys(t *testing.T) {
	key := testMasterKey(t)
	tests := []struct {
		name     string
		previous string
	}{
		{name: "no separator", previous: key},
		{name: "invalid id", previous: "BAD ID:" + key},
		{name: "invalid key", previous: "kek-old:not base64!!"},
		{name: "short key", previous: "kek-old:" + base64.StdEncoding.EncodeToString(make([]byte, 31))},
		{name: "duplicate previous", previous: "kek-old:" + key + ",kek-old:" + testMasterKey(t)},
		{name: "shadows the active key", previous: testKeyID + ":" + testMasterKey(t)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := crypto.NewKeyring(testKeyID, key, tt.previous); !errors.Is(err, crypto.ErrInvalidKey) {
				t.Fatalf("expected ErrInvalidKey, got %v", err)
			}
		})
	}
}

func TestKeyringMethodsFailClosedWhenNil(t *testing.T) {
	var ring *crypto.Keyring
	if id := ring.ActiveKeyID(); id != "" {
		t.Fatalf("a nil ring must have no active key id, got %q", id)
	}
	if _, _, err := ring.Wrap(make([]byte, crypto.KeySize), testBinding()); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey from nil ring, got %v", err)
	}
	if _, err := ring.Unwrap([]byte("x"), testKeyID, testBinding()); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey from nil ring, got %v", err)
	}
}

func TestWrapUnwrapRoundTrip(t *testing.T) {
	ring := newTestKeyring(t)
	binding := testBinding()
	dataKey := newTestDataKey(t)

	wrapped, keyID, err := ring.Wrap(dataKey, binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if keyID != testKeyID || keyID != ring.ActiveKeyID() {
		t.Fatalf("wrap must report the active key id, got %q", keyID)
	}
	if bytes.Contains(wrapped, dataKey) {
		t.Fatal("wrapped key must not contain the plaintext data key")
	}
	unwrapped, err := ring.Unwrap(wrapped, keyID, binding)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(unwrapped, dataKey) {
		t.Fatal("unwrapped key differs from the original")
	}
}

func TestWrapRejectsWrongSizedDataKey(t *testing.T) {
	ring := newTestKeyring(t)
	if _, _, err := ring.Wrap(make([]byte, 16), testBinding()); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

// The wrapped key is tied to the attachment, its workspace and the key id, so
// none of the three can be swapped in the database and still open the object.
func TestUnwrapIsBoundToTheAttachmentWorkspaceAndKeyID(t *testing.T) {
	other := testMasterKey(t)
	ring := newTestKeyringWith(t, testKeyID, testMasterKey(t), "kek-previous:"+other)
	binding := testBinding()
	wrapped, keyID, err := ring.Wrap(newTestDataKey(t), binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	tests := []struct {
		name    string
		keyID   string
		binding crypto.Binding
	}{
		{
			name:    "another attachment",
			keyID:   keyID,
			binding: withAttachment(binding, uuid.New()),
		},
		{
			name:    "another workspace",
			keyID:   keyID,
			binding: withWorkspace(binding, uuid.New()),
		},
		{
			name:    "another configured key id",
			keyID:   "kek-previous",
			binding: binding,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ring.Unwrap(wrapped, tt.keyID, tt.binding); !errors.Is(err, crypto.ErrCiphertext) {
				t.Fatalf("expected ErrCiphertext, got %v", err)
			}
		})
	}
}

func TestUnwrapRejectsTamperedAndTruncatedKeys(t *testing.T) {
	ring := newTestKeyring(t)
	binding := testBinding()
	wrapped, keyID, err := ring.Wrap(newTestDataKey(t), binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	tampered := append([]byte(nil), wrapped...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := ring.Unwrap(tampered, keyID, binding); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for tampered key, got %v", err)
	}
	// The nonce is the first nonceSize bytes of the wrapped value.
	nonceEdited := append([]byte(nil), wrapped...)
	nonceEdited[0] ^= 0xff
	if _, err := ring.Unwrap(nonceEdited, keyID, binding); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for an edited wrap nonce, got %v", err)
	}
	if _, err := ring.Unwrap(wrapped[:8], keyID, binding); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for truncated key, got %v", err)
	}
}

func TestUnwrapRejectsAnotherMasterKeyUnderTheSameID(t *testing.T) {
	binding := testBinding()
	wrapped, keyID, err := newTestKeyring(t).Wrap(newTestDataKey(t), binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	// Same id, different key material: a deployment that regenerated the key
	// without changing its label must not be able to open the old object.
	if _, err := newTestKeyring(t).Unwrap(wrapped, keyID, binding); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext under a different master key, got %v", err)
	}
}

// An id the ring does not have is refused outright. Nothing is tried, and the
// error is distinguishable from a tampered ciphertext in the log — never to a
// client, which sees the handler's generic failure either way.
func TestUnwrapRejectsAnUnknownKeyID(t *testing.T) {
	ring := newTestKeyring(t)
	binding := testBinding()
	wrapped, _, err := ring.Wrap(newTestDataKey(t), binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	for _, keyID := range []string{"", "kek-never-configured"} {
		if _, err := ring.Unwrap(wrapped, keyID, binding); !errors.Is(err, crypto.ErrUnknownKey) {
			t.Fatalf("expected ErrUnknownKey for %q, got %v", keyID, err)
		}
	}
}

// Rotation: a key moved from active to previous still opens what it wrapped,
// and new wraps use the new active key. The object itself is never re-encrypted.
func TestRotatedKeyStillOpensObjectsItWrapped(t *testing.T) {
	firstKey, secondKey := testMasterKey(t), testMasterKey(t)
	binding := testBinding()

	before := newTestKeyringWith(t, "kek-first", firstKey, "")
	dataKey := newTestDataKey(t)
	wrapped, keyID, err := before.Wrap(dataKey, binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if keyID != "kek-first" {
		t.Fatalf("expected the first key id, got %q", keyID)
	}

	after := newTestKeyringWith(t, "kek-second", secondKey, "kek-first:"+firstKey)
	if after.ActiveKeyID() != "kek-second" {
		t.Fatalf("new uploads must use the new key, got %q", after.ActiveKeyID())
	}
	unwrapped, err := after.Unwrap(wrapped, keyID, binding)
	if err != nil {
		t.Fatalf("rotated ring must still open the old wrapped key: %v", err)
	}
	if !bytes.Equal(unwrapped, dataKey) {
		t.Fatal("rotated ring returned a different data key")
	}

	// Rewrap without touching the object: the same data key under the new key.
	rewrapped, newKeyID, err := after.Wrap(unwrapped, binding)
	if err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	if newKeyID != "kek-second" {
		t.Fatalf("rewrap must use the active key, got %q", newKeyID)
	}
	reopened, err := after.Unwrap(rewrapped, newKeyID, binding)
	if err != nil {
		t.Fatalf("unwrap after rewrap: %v", err)
	}
	if !bytes.Equal(reopened, dataKey) {
		t.Fatal("rewrap changed the data key")
	}

	// A ring that no longer carries the old key refuses it instead of guessing.
	retired := newTestKeyringWith(t, "kek-second", secondKey, "")
	if _, err := retired.Unwrap(wrapped, keyID, binding); !errors.Is(err, crypto.ErrUnknownKey) {
		t.Fatalf("expected ErrUnknownKey after retiring the key, got %v", err)
	}
}

// Two attachments never share a wrapped data key, even under the same KEK.
func TestWrapIsDistinctPerAttachment(t *testing.T) {
	ring := newTestKeyring(t)
	dataKey := newTestDataKey(t)
	first, _, err := ring.Wrap(dataKey, testBinding())
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	second, _, err := ring.Wrap(dataKey, testBinding())
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("wrapping the same data key twice must not produce the same bytes")
	}
}

func TestNewDataKeyReturnsDistinctKeys(t *testing.T) {
	first, second := newTestDataKey(t), newTestDataKey(t)
	if len(first) != crypto.KeySize {
		t.Fatalf("expected %d bytes, got %d", crypto.KeySize, len(first))
	}
	if bytes.Equal(first, second) {
		t.Fatal("each object must get a distinct data key")
	}
}

// --- content stream -----------------------------------------------------

// TestContentRoundTripAcrossSizes checks the envelope at every boundary the
// chunking has: empty, one byte, either side of a chunk, and several chunks.
//
// It asserts structure and round-trip, never "the ciphertext does not contain
// the plaintext". That search is not a confidentiality property: for a short
// plaintext the same byte values occur naturally in the magic, the base nonce,
// the ciphertext and the GCM tag without anything having leaked. This test used
// to make that assertion and failed in CI on the one-byte case; measured over
// 20000 runs of the real encryptor it reported a false failure 10.41% of the
// time at one byte and 0.04% at two, purely because a 29-byte envelope is very
// likely to contain any given single byte.
//
// Confidentiality and integrity are carried by the tests that can actually
// fail deterministically when the construction is wrong:
// TestDecryptRejectsWrongDataKey, TestDecryptRejectsTamperedCiphertext,
// TestDecryptRejectsTruncation, TestDecryptRejectsReorderedDuplicatedAndRemovedChunks,
// TestDecryptRejectsSubstitutionFromAnotherAttachment and
// TestChunksInsideOneObjectUseDistinctNonces. A forged or misdirected chunk
// gets past those with probability 2^-128, not 1 in 10.
func TestContentRoundTripAcrossSizes(t *testing.T) {
	sizes := []int{
		0,
		1,
		crypto.ChunkSize - 1,
		crypto.ChunkSize,
		crypto.ChunkSize + 1,
		3*crypto.ChunkSize + 17,
	}
	for _, size := range sizes {
		t.Run(sizeName(size), func(t *testing.T) {
			id := uuid.New()
			dataKey := newTestDataKey(t)
			plaintext := randomBytes(t, size)

			// encrypt fails the test on error, so reaching here means the
			// envelope was produced.
			ciphertext := encrypt(t, plaintext, dataKey, id)

			if len(ciphertext) == 0 {
				t.Fatal("encrypting produced no envelope")
			}
			// Deliberately a whole-value comparison and not a substring search:
			// see the note above this test for why searching for the plaintext
			// inside the ciphertext is not a valid confidentiality assertion.
			if bytes.Equal(ciphertext, plaintext) {
				t.Fatal("the envelope must not be the plaintext")
			}
			if int64(len(ciphertext)) != crypto.CiphertextSize(int64(size)) {
				t.Fatalf("expected ciphertext size %d, got %d",
					crypto.CiphertextSize(int64(size)), len(ciphertext))
			}
			// The envelope is framed and versioned rather than raw bytes, at
			// every size including the smallest.
			if !bytes.HasPrefix(ciphertext, []byte("NCF1")) {
				t.Fatalf("envelope does not start with the version magic: %x", ciphertext[:min(4, len(ciphertext))])
			}

			got, err := decrypt(t, ciphertext, dataKey, id, int64(size))
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if !bytes.Equal(got, plaintext) {
				t.Fatal("round trip changed the plaintext")
			}
		})
	}
}

func TestEncryptingReaderUsesDistinctNoncesAndKeysPerObject(t *testing.T) {
	plaintext := randomBytes(t, crypto.ChunkSize+64)
	first := encrypt(t, plaintext, newTestDataKey(t), uuid.New())
	second := encrypt(t, plaintext, newTestDataKey(t), uuid.New())
	if bytes.Equal(first, second) {
		t.Fatal("two objects with the same plaintext must not produce identical ciphertext")
	}

	// The same data key twice still yields a fresh base nonce per object.
	dataKey := newTestDataKey(t)
	id := uuid.New()
	a := encrypt(t, plaintext, dataKey, id)
	b := encrypt(t, plaintext, dataKey, id)
	if bytes.Equal(a, b) {
		t.Fatal("base nonce must be fresh for every encryption")
	}
}

func TestChunksInsideOneObjectUseDistinctNonces(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	// Two identical chunks of plaintext must not encrypt to identical blocks;
	// that would imply a repeated (key, nonce) pair.
	plaintext := bytes.Repeat([]byte{0x5a}, 2*crypto.ChunkSize)
	ciphertext := encrypt(t, plaintext, dataKey, id)

	const headerSize = 12
	blockSize := crypto.ChunkSize + 16
	first := ciphertext[headerSize : headerSize+blockSize]
	second := ciphertext[headerSize+blockSize : headerSize+2*blockSize]
	if bytes.Equal(first, second) {
		t.Fatal("identical plaintext chunks must not produce identical ciphertext chunks")
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	const plaintextSize = 2*crypto.ChunkSize + 5
	ciphertext := encrypt(t, randomBytes(t, plaintextSize), dataKey, id)

	positions := map[string]int{
		"magic":       0,
		"base nonce":  6,
		"first chunk": 100,
		"last byte":   len(ciphertext) - 1,
	}
	for name, position := range positions {
		t.Run(name, func(t *testing.T) {
			tampered := append([]byte(nil), ciphertext...)
			tampered[position] ^= 0xff
			if _, err := decrypt(t, tampered, dataKey, id, plaintextSize); !errors.Is(err, crypto.ErrCiphertext) {
				t.Fatalf("expected ErrCiphertext, got %v", err)
			}
		})
	}
}

func TestDecryptRejectsTruncation(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	const plaintextSize = 2*crypto.ChunkSize + 5
	ciphertext := encrypt(t, randomBytes(t, plaintextSize), dataKey, id)

	const headerSize = 12
	blockSize := crypto.ChunkSize + 16

	tests := map[string][]byte{
		"empty":                    nil,
		"partial header":           ciphertext[:5],
		"header only":              ciphertext[:headerSize],
		"mid chunk":                ciphertext[:headerSize+blockSize/2],
		"dropped final chunk":      ciphertext[:headerSize+2*blockSize],
		"final chunk missing tag":  ciphertext[:len(ciphertext)-8],
		"whole final chunk minus1": ciphertext[:len(ciphertext)-1],
	}
	for name, truncated := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decrypt(t, truncated, dataKey, id, plaintextSize); !errors.Is(err, crypto.ErrCiphertext) {
				t.Fatalf("expected ErrCiphertext, got %v", err)
			}
		})
	}
}

func TestDecryptRejectsReorderedDuplicatedAndRemovedChunks(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	// Three full chunks plus a short final one gives room to shuffle.
	const plaintextSize = 3*crypto.ChunkSize + 9
	ciphertext := encrypt(t, randomBytes(t, plaintextSize), dataKey, id)

	const headerSize = 12
	blockSize := crypto.ChunkSize + 16
	header := ciphertext[:headerSize]
	chunk := func(i int) []byte {
		return ciphertext[headerSize+i*blockSize : headerSize+(i+1)*blockSize]
	}
	final := ciphertext[headerSize+3*blockSize:]

	tests := map[string][]byte{
		"reordered": concat(header, chunk(1), chunk(0), chunk(2), final),
		"duplicated": concat(header, chunk(0), chunk(0), chunk(1), chunk(2),
			final),
		"removed":                           concat(header, chunk(0), chunk(2), final),
		"final chunk promoted to the front": concat(header, final),
	}
	for name, corrupted := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decrypt(t, corrupted, dataKey, id, plaintextSize); !errors.Is(err, crypto.ErrCiphertext) {
				t.Fatalf("expected ErrCiphertext, got %v", err)
			}
		})
	}
}

func TestDecryptRejectsSubstitutionFromAnotherAttachment(t *testing.T) {
	dataKey := newTestDataKey(t)
	plaintext := randomBytes(t, 1024)
	ciphertext := encrypt(t, plaintext, dataKey, uuid.New())

	// Same data key, different attachment id: the AAD binding must reject it.
	if _, err := decrypt(t, ciphertext, dataKey, uuid.New(), int64(len(plaintext))); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for a foreign attachment id, got %v", err)
	}
}

func TestDecryptRejectsWrongDataKey(t *testing.T) {
	id := uuid.New()
	ciphertext := encrypt(t, randomBytes(t, 4096), newTestDataKey(t), id)
	if _, err := decrypt(t, ciphertext, newTestDataKey(t), id, 4096); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext, got %v", err)
	}
}

func TestDecryptRejectsUnsupportedEnvelopeMagic(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	ciphertext := encrypt(t, randomBytes(t, 32), dataKey, id)
	foreign := append([]byte("NCF9"), ciphertext[4:]...)

	if _, err := decrypt(t, foreign, dataKey, id, 32); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext, got %v", err)
	}
}

func TestReadersRejectInvalidDataKeys(t *testing.T) {
	if _, err := crypto.NewEncryptingReader(bytes.NewReader(nil), make([]byte, 8), uuid.New()); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
	if _, err := crypto.NewDecryptingReader(bytes.NewReader(nil), make([]byte, 8), uuid.New(), 0); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestEncryptingReaderPropagatesSourceErrors(t *testing.T) {
	sourceErr := errors.New("source exploded")
	reader, err := crypto.NewEncryptingReader(
		&failingReader{after: 10, err: sourceErr}, newTestDataKey(t), uuid.New(),
	)
	if err != nil {
		t.Fatalf("build reader: %v", err)
	}
	if _, err := io.ReadAll(reader); !errors.Is(err, sourceErr) {
		t.Fatalf("expected the source error to propagate, got %v", err)
	}
}

func TestDecryptingReaderPropagatesSourceErrors(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	ciphertext := encrypt(t, randomBytes(t, 2048), dataKey, id)

	sourceErr := errors.New("storage exploded")
	reader, err := crypto.NewDecryptingReader(
		io.MultiReader(bytes.NewReader(ciphertext[:20]), &failingReader{err: sourceErr}),
		dataKey, id, 2048,
	)
	if err != nil {
		t.Fatalf("build reader: %v", err)
	}
	if _, err := io.ReadAll(reader); !errors.Is(err, sourceErr) {
		t.Fatalf("expected the source error to propagate, got %v", err)
	}
}

func TestDecryptingReaderPropagatesHeaderReadErrors(t *testing.T) {
	sourceErr := errors.New("storage exploded")
	reader, err := crypto.NewDecryptingReader(&failingReader{err: sourceErr}, newTestDataKey(t), uuid.New(), 0)
	if err != nil {
		t.Fatalf("build reader: %v", err)
	}
	if _, err := io.ReadAll(reader); !errors.Is(err, sourceErr) {
		t.Fatalf("expected the source error to propagate, got %v", err)
	}
}

func TestReadersReturnTheSameErrorOnRepeatedReads(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	ciphertext := encrypt(t, randomBytes(t, 64), dataKey, id)
	ciphertext[len(ciphertext)-1] ^= 0xff

	reader, err := crypto.NewDecryptingReader(bytes.NewReader(ciphertext), dataKey, id, 64)
	if err != nil {
		t.Fatalf("build reader: %v", err)
	}
	buf := make([]byte, 16)
	if _, first := reader.Read(buf); !errors.Is(first, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext, got %v", first)
	}
	if _, second := reader.Read(buf); !errors.Is(second, crypto.ErrCiphertext) {
		t.Fatalf("expected the error to persist, got %v", second)
	}
}

func TestReadersTolerateTinyBuffers(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	plaintext := randomBytes(t, crypto.ChunkSize+123)

	encrypting, err := crypto.NewEncryptingReader(bytes.NewReader(plaintext), dataKey, id)
	if err != nil {
		t.Fatalf("build reader: %v", err)
	}
	var ciphertext bytes.Buffer
	if _, err := io.CopyBuffer(&ciphertext, encrypting, make([]byte, 7)); err != nil {
		t.Fatalf("encrypt with a tiny buffer: %v", err)
	}

	decrypting, err := crypto.NewDecryptingReader(
		bytes.NewReader(ciphertext.Bytes()), dataKey, id, int64(len(plaintext)),
	)
	if err != nil {
		t.Fatalf("build reader: %v", err)
	}
	var got bytes.Buffer
	if _, err := io.CopyBuffer(&got, decrypting, make([]byte, 5)); err != nil {
		t.Fatalf("decrypt with a tiny buffer: %v", err)
	}
	if !bytes.Equal(got.Bytes(), plaintext) {
		t.Fatal("round trip through tiny buffers changed the plaintext")
	}
}

func TestCiphertextSizeMatchesTheWriter(t *testing.T) {
	if crypto.CiphertextSize(0) != 12+16 {
		t.Fatalf("unexpected empty envelope size %d", crypto.CiphertextSize(0))
	}
	if crypto.EnvelopeVersion != 1 {
		t.Fatalf("envelope version must stay 1 while the format is v1, got %d", crypto.EnvelopeVersion)
	}
}

// --- helpers ------------------------------------------------------------

type failingReader struct {
	after int
	err   error
	read  int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.read >= r.after {
		return 0, r.err
	}
	n := min(len(p), r.after-r.read)
	for i := range n {
		p[i] = byte(i)
	}
	r.read += n
	return n, nil
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func sizeName(size int) string {
	switch size {
	case 0:
		return "empty"
	case 1:
		return "single byte"
	case crypto.ChunkSize:
		return "exactly one chunk"
	case crypto.ChunkSize - 1:
		return "one byte under a chunk"
	case crypto.ChunkSize + 1:
		return "one byte over a chunk"
	default:
		return "several chunks"
	}
}

// --- plaintext size binding ---------------------------------------------

// The recorded plaintext length is authenticated, so an attacker with write
// access to PostgreSQL cannot lower size_bytes and have the service serve a
// prefix under a smaller Content-Length. Every one-byte deviation fails.
func TestUnwrapIsBoundToThePlaintextSize(t *testing.T) {
	ring := newTestKeyring(t)
	const realSize = 5_000_000
	binding := bindingOfSize(realSize)
	wrapped, keyID, err := ring.Wrap(newTestDataKey(t), binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	for name, size := range map[string]int64{
		"zero":          0,
		"one byte less": realSize - 1,
		"one byte more": realSize + 1,
		"halved":        realSize / 2,
		"much larger":   realSize * 4,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ring.Unwrap(wrapped, keyID, withSize(binding, size)); !errors.Is(err, crypto.ErrCiphertext) {
				t.Fatalf("expected ErrCiphertext for size %d, got %v", size, err)
			}
		})
	}
	if _, err := ring.Unwrap(wrapped, keyID, binding); err != nil {
		t.Fatalf("the recorded size must still open the key: %v", err)
	}
}

// The size occupies eight authenticated bytes, so a flip anywhere in the
// big-endian encoding — including the high bytes a small file never uses — has
// to fail. Testing every bit position proves the whole field is covered rather
// than just its low byte.
func TestUnwrapRejectsEverySingleBitFlipInTheSize(t *testing.T) {
	ring := newTestKeyring(t)
	const realSize int64 = 1 << 20
	binding := bindingOfSize(realSize)
	wrapped, keyID, err := ring.Wrap(newTestDataKey(t), binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	// Bit 63 would make the value negative, which is refused before the AEAD.
	for bit := 0; bit < 63; bit++ {
		flipped := realSize ^ (int64(1) << bit)
		if flipped == realSize {
			continue
		}
		if _, err := ring.Unwrap(wrapped, keyID, withSize(binding, flipped)); !errors.Is(err, crypto.ErrCiphertext) {
			t.Fatalf("bit %d flipped to size %d was accepted: %v", bit, flipped, err)
		}
	}
}

// The size is serialised big-endian, in the last eight bytes of the associated
// data. The check is behavioural rather than a byte comparison against a
// literal: two sizes that are byte swaps of each other must not be
// interchangeable, which is exactly what a little-endian encoder would break.
func TestPlaintextSizeIsBigEndianInTheBinding(t *testing.T) {
	ring := newTestKeyring(t)
	// 0x0102 and 0x0201 share their bytes and differ only in order.
	binding := bindingOfSize(0x0102)
	wrapped, keyID, err := ring.Wrap(newTestDataKey(t), binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if _, err := ring.Unwrap(wrapped, keyID, withSize(binding, 0x0201)); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("a byte-swapped size must not open the key, got %v", err)
	}
}

func TestBindingRejectsANegativeSize(t *testing.T) {
	ring := newTestKeyring(t)
	binding := bindingOfSize(-1)
	if _, _, err := ring.Wrap(newTestDataKey(t), binding); !errors.Is(err, crypto.ErrInvalidBinding) {
		t.Fatalf("expected ErrInvalidBinding on wrap, got %v", err)
	}
	if _, err := ring.Unwrap(make([]byte, 64), testKeyID, binding); !errors.Is(err, crypto.ErrInvalidBinding) {
		t.Fatalf("expected ErrInvalidBinding on unwrap, got %v", err)
	}
	// The largest representable length is fine: only negatives are refused, so
	// there is no size a real counter could produce that cannot be bound.
	if _, _, err := ring.Wrap(newTestDataKey(t), bindingOfSize(math.MaxInt64)); err != nil {
		t.Fatalf("the maximum int64 size must be representable: %v", err)
	}
}

// Both versions are authenticated and neither is guessed. An unknown one is
// refused outright — there is no attempt to open the key under the previous
// binding, because that would be a downgrade path.
func TestBindingRejectsUnknownVersions(t *testing.T) {
	ring := newTestKeyring(t)
	binding := testBinding()
	wrapped, keyID, err := ring.Wrap(newTestDataKey(t), binding)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	tests := map[string]crypto.Binding{
		"older key wrap version":  {KeyWrapVersion: crypto.KeyWrapVersion - 1, ContentEnvelopeVersion: crypto.EnvelopeVersion},
		"newer key wrap version":  {KeyWrapVersion: crypto.KeyWrapVersion + 1, ContentEnvelopeVersion: crypto.EnvelopeVersion},
		"absent key wrap version": {KeyWrapVersion: 0, ContentEnvelopeVersion: crypto.EnvelopeVersion},
		"newer content version":   {KeyWrapVersion: crypto.KeyWrapVersion, ContentEnvelopeVersion: crypto.EnvelopeVersion + 1},
		"absent content version":  {KeyWrapVersion: crypto.KeyWrapVersion, ContentEnvelopeVersion: 0},
		"both versions unknown":   {KeyWrapVersion: 99, ContentEnvelopeVersion: 99},
		"versions swapped over":   {KeyWrapVersion: crypto.EnvelopeVersion, ContentEnvelopeVersion: crypto.KeyWrapVersion},
	}
	for name, versions := range tests {
		t.Run(name, func(t *testing.T) {
			tampered := binding
			tampered.KeyWrapVersion = versions.KeyWrapVersion
			tampered.ContentEnvelopeVersion = versions.ContentEnvelopeVersion

			if _, err := ring.Unwrap(wrapped, keyID, tampered); !errors.Is(err, crypto.ErrUnsupportedVersion) {
				t.Fatalf("expected ErrUnsupportedVersion, got %v", err)
			}
			if _, _, err := ring.Wrap(newTestDataKey(t), tampered); !errors.Is(err, crypto.ErrUnsupportedVersion) {
				t.Fatalf("expected ErrUnsupportedVersion on wrap, got %v", err)
			}
		})
	}
}

// --- plaintext size as a stream invariant --------------------------------

// The reader must not treat the expected size as a stopping point. If it did, a
// lowered size_bytes would produce a complete-looking prefix instead of an
// error, which is the whole attack.
func TestDecryptingReaderDoesNotStopAtTheExpectedSize(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	plaintext := randomBytes(t, 3*crypto.ChunkSize+77)
	ciphertext := encrypt(t, plaintext, dataKey, id)

	// A size one byte short: the stream really contains more, and the reader has
	// to say so rather than returning that one-byte-shorter prefix as a file.
	got, err := decrypt(t, ciphertext, dataKey, id, int64(len(plaintext))-1)
	if !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for an undersized expectation, got %v", err)
	}
	if int64(len(got)) >= int64(len(plaintext))-1 {
		t.Fatalf("the reader returned %d bytes before failing; it must not complete a prefix", len(got))
	}
}

func TestDecryptingReaderRejectsAnyMismatchedSize(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	plaintext := randomBytes(t, crypto.ChunkSize+11)
	ciphertext := encrypt(t, plaintext, dataKey, id)
	real := int64(len(plaintext))

	for name, expected := range map[string]int64{
		"zero":            0,
		"one short":       real - 1,
		"one long":        real + 1,
		"chunk short":     real - crypto.ChunkSize,
		"wildly too long": real * 10,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decrypt(t, ciphertext, dataKey, id, expected); !errors.Is(err, crypto.ErrCiphertext) {
				t.Fatalf("expected ErrCiphertext, got %v", err)
			}
		})
	}
	if _, err := decrypt(t, ciphertext, dataKey, id, real); err != nil {
		t.Fatalf("the real size must still decrypt: %v", err)
	}
}

// An empty object is the one case where the expectation and the first frame
// coincide, so it gets its own check in both directions.
func TestDecryptingReaderChecksTheSizeOfAnEmptyObject(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	ciphertext := encrypt(t, nil, dataKey, id)

	if got, err := decrypt(t, ciphertext, dataKey, id, 0); err != nil || len(got) != 0 {
		t.Fatalf("an empty object must round trip: %d bytes, %v", len(got), err)
	}
	if _, err := decrypt(t, ciphertext, dataKey, id, 1); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for a non-zero expectation, got %v", err)
	}
}

func TestDecryptingReaderRejectsANegativeSize(t *testing.T) {
	if _, err := crypto.NewDecryptingReader(
		bytes.NewReader(nil), newTestDataKey(t), uuid.New(), -1,
	); !errors.Is(err, crypto.ErrInvalidBinding) {
		t.Fatalf("expected ErrInvalidBinding, got %v", err)
	}
}
