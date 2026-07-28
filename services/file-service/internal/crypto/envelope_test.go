package crypto_test

import (
	"bytes"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"io"
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

func newTestKEK(t *testing.T) *crypto.KeyEncryptionKey {
	t.Helper()
	kek, err := crypto.NewKeyEncryptionKey(testMasterKey(t))
	if err != nil {
		t.Fatalf("build kek: %v", err)
	}
	return kek
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

func decrypt(t *testing.T, ciphertext []byte, dataKey []byte, id uuid.UUID) ([]byte, error) {
	t.Helper()
	reader, err := crypto.NewDecryptingReader(bytes.NewReader(ciphertext), dataKey, id)
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

// --- master key ---------------------------------------------------------

func TestNewKeyEncryptionKeyRejectsBadKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "whitespace", key: "   "},
		{name: "not base64", key: "not base64!!"},
		{name: "short", key: base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{name: "long", key: base64.StdEncoding.EncodeToString(make([]byte, 64))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := crypto.NewKeyEncryptionKey(tt.key); !errors.Is(err, crypto.ErrInvalidKey) {
				t.Fatalf("expected ErrInvalidKey, got %v", err)
			}
			if err := crypto.ValidateMasterKey(tt.key); !errors.Is(err, crypto.ErrInvalidKey) {
				t.Fatalf("expected ValidateMasterKey to reject, got %v", err)
			}
		})
	}
}

func TestValidateMasterKeyAcceptsCorrectKey(t *testing.T) {
	if err := crypto.ValidateMasterKey(testMasterKey(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestKeyEncryptionKeyMethodsFailClosedWhenNil(t *testing.T) {
	var kek *crypto.KeyEncryptionKey
	if _, err := kek.Wrap(make([]byte, crypto.KeySize), uuid.New()); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey from nil kek, got %v", err)
	}
	if _, err := kek.Unwrap([]byte("x"), uuid.New()); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey from nil kek, got %v", err)
	}
}

func TestWrapUnwrapRoundTrip(t *testing.T) {
	kek := newTestKEK(t)
	id := uuid.New()
	dataKey := newTestDataKey(t)

	wrapped, err := kek.Wrap(dataKey, id)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if bytes.Contains(wrapped, dataKey) {
		t.Fatal("wrapped key must not contain the plaintext data key")
	}
	unwrapped, err := kek.Unwrap(wrapped, id)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(unwrapped, dataKey) {
		t.Fatal("unwrapped key differs from the original")
	}
}

func TestWrapRejectsWrongSizedDataKey(t *testing.T) {
	kek := newTestKEK(t)
	if _, err := kek.Wrap(make([]byte, 16), uuid.New()); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestUnwrapIsBoundToTheAttachment(t *testing.T) {
	kek := newTestKEK(t)
	dataKey := newTestDataKey(t)
	wrapped, err := kek.Wrap(dataKey, uuid.New())
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if _, err := kek.Unwrap(wrapped, uuid.New()); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for another attachment, got %v", err)
	}
}

func TestUnwrapRejectsTamperedAndTruncatedKeys(t *testing.T) {
	kek := newTestKEK(t)
	id := uuid.New()
	wrapped, err := kek.Wrap(newTestDataKey(t), id)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	tampered := append([]byte(nil), wrapped...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := kek.Unwrap(tampered, id); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for tampered key, got %v", err)
	}
	if _, err := kek.Unwrap(wrapped[:8], id); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for truncated key, got %v", err)
	}
}

func TestUnwrapRejectsAnotherMasterKey(t *testing.T) {
	id := uuid.New()
	wrapped, err := newTestKEK(t).Wrap(newTestDataKey(t), id)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if _, err := newTestKEK(t).Unwrap(wrapped, id); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext under a different master key, got %v", err)
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

			got, err := decrypt(t, ciphertext, dataKey, id)
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
	ciphertext := encrypt(t, randomBytes(t, 2*crypto.ChunkSize+5), dataKey, id)

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
			if _, err := decrypt(t, tampered, dataKey, id); !errors.Is(err, crypto.ErrCiphertext) {
				t.Fatalf("expected ErrCiphertext, got %v", err)
			}
		})
	}
}

func TestDecryptRejectsTruncation(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	ciphertext := encrypt(t, randomBytes(t, 2*crypto.ChunkSize+5), dataKey, id)

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
			if _, err := decrypt(t, truncated, dataKey, id); !errors.Is(err, crypto.ErrCiphertext) {
				t.Fatalf("expected ErrCiphertext, got %v", err)
			}
		})
	}
}

func TestDecryptRejectsReorderedDuplicatedAndRemovedChunks(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	// Three full chunks plus a short final one gives room to shuffle.
	ciphertext := encrypt(t, randomBytes(t, 3*crypto.ChunkSize+9), dataKey, id)

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
			if _, err := decrypt(t, corrupted, dataKey, id); !errors.Is(err, crypto.ErrCiphertext) {
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
	if _, err := decrypt(t, ciphertext, dataKey, uuid.New()); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for a foreign attachment id, got %v", err)
	}
}

func TestDecryptRejectsWrongDataKey(t *testing.T) {
	id := uuid.New()
	ciphertext := encrypt(t, randomBytes(t, 4096), newTestDataKey(t), id)
	if _, err := decrypt(t, ciphertext, newTestDataKey(t), id); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext, got %v", err)
	}
}

func TestDecryptRejectsUnsupportedEnvelopeMagic(t *testing.T) {
	id := uuid.New()
	dataKey := newTestDataKey(t)
	ciphertext := encrypt(t, randomBytes(t, 32), dataKey, id)
	foreign := append([]byte("NCF9"), ciphertext[4:]...)

	if _, err := decrypt(t, foreign, dataKey, id); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext, got %v", err)
	}
}

func TestReadersRejectInvalidDataKeys(t *testing.T) {
	if _, err := crypto.NewEncryptingReader(bytes.NewReader(nil), make([]byte, 8), uuid.New()); !errors.Is(err, crypto.ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
	if _, err := crypto.NewDecryptingReader(bytes.NewReader(nil), make([]byte, 8), uuid.New()); !errors.Is(err, crypto.ErrInvalidKey) {
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
		dataKey, id,
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
	reader, err := crypto.NewDecryptingReader(&failingReader{err: sourceErr}, newTestDataKey(t), uuid.New())
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

	reader, err := crypto.NewDecryptingReader(bytes.NewReader(ciphertext), dataKey, id)
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

	decrypting, err := crypto.NewDecryptingReader(bytes.NewReader(ciphertext.Bytes()), dataKey, id)
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
