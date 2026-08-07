package crypto_test

import (
	"bytes"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
)

// sealEnvelope produces one stored object from plaintext, the way an upload
// does, so the reads below run against a real envelope rather than a fixture.
func sealEnvelope(t *testing.T, plaintext []byte, objectID uuid.UUID) ([]byte, []byte) {
	t.Helper()
	dataKey, err := crypto.NewDataKey()
	if err != nil {
		t.Fatalf("data key: %v", err)
	}
	reader, err := crypto.NewEncryptingReader(bytes.NewReader(plaintext), dataKey, objectID)
	if err != nil {
		t.Fatalf("encrypting reader: %v", err)
	}
	stored, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if int64(len(stored)) != crypto.CiphertextSize(int64(len(plaintext))) {
		t.Fatalf("stored %d bytes, expected %d", len(stored), crypto.CiphertextSize(int64(len(plaintext))))
	}
	return stored, dataKey
}

// randomPlaintext spans several chunks so an offset can land on a boundary, one
// byte either side of one, and deep inside the last partial chunk.
func randomPlaintext(t *testing.T, size int) []byte {
	t.Helper()
	plaintext := make([]byte, size)
	if _, err := io.ReadFull(cryptorand.Reader, plaintext); err != nil {
		t.Fatalf("random plaintext: %v", err)
	}
	return plaintext
}

// readFrom opens the envelope at a plaintext offset the way a ranged download
// does: locate the chunk, hand over the header and the ciphertext from that
// chunk on, then drop the bytes inside the chunk that precede the offset.
func readFrom(t *testing.T, stored, dataKey []byte, objectID uuid.UUID, size, offset int64) io.Reader {
	t.Helper()
	chunkIndex, ciphertextOffset, skip := crypto.ChunkLocation(offset)
	reader, err := crypto.NewChunkReader(
		stored[:crypto.EnvelopeHeaderSize], bytes.NewReader(stored[ciphertextOffset:]),
		dataKey, objectID, size, chunkIndex,
	)
	if err != nil {
		t.Fatalf("chunk reader at %d: %v", offset, err)
	}
	if _, err := io.CopyN(io.Discard, reader, skip); err != nil {
		t.Fatalf("skip %d bytes: %v", skip, err)
	}
	return reader
}

// The arithmetic the whole feature rests on: a plaintext offset names one chunk,
// at one place in the stored object, with one number of bytes to drop.
func TestChunkLocationMapsOffsetsOntoTheStoredObject(t *testing.T) {
	const block = crypto.ChunkSize + 16 // plaintext chunk plus its GCM tag
	const header = int64(crypto.EnvelopeHeaderSize)
	tests := []struct {
		offset int64
		index  uint32
		stored int64
		skip   int64
	}{
		{0, 0, header, 0},
		{1, 0, header, 1},
		{crypto.ChunkSize - 1, 0, header, crypto.ChunkSize - 1},
		{crypto.ChunkSize, 1, header + block, 0},
		{crypto.ChunkSize + 1, 1, header + block, 1},
		{3*crypto.ChunkSize + 17, 3, header + 3*block, 17},
		// A negative offset cannot come from anywhere real, and is clamped
		// rather than allowed to produce a negative storage offset.
		{-1, 0, header, 0},
	}
	for _, tt := range tests {
		index, stored, skip := crypto.ChunkLocation(tt.offset)
		if index != tt.index || stored != tt.stored || skip != tt.skip {
			t.Fatalf("offset %d: got (%d, %d, %d), want (%d, %d, %d)",
				tt.offset, index, stored, skip, tt.index, tt.stored, tt.skip)
		}
	}
}

// The property a byte range depends on: reading from any offset yields exactly
// the plaintext at that offset, whichever chunk it lands in.
func TestChunkReaderYieldsThePlaintextAtEveryOffset(t *testing.T) {
	const size = 3*crypto.ChunkSize + 5000
	objectID := uuid.New()
	plaintext := randomPlaintext(t, size)
	stored, dataKey := sealEnvelope(t, plaintext, objectID)

	offsets := []int64{
		0, 1, 512,
		crypto.ChunkSize - 1, crypto.ChunkSize, crypto.ChunkSize + 1,
		2 * crypto.ChunkSize, 3 * crypto.ChunkSize, 3*crypto.ChunkSize + 4999,
		size - 1,
	}
	for _, offset := range offsets {
		got, err := io.ReadAll(readFrom(t, stored, dataKey, objectID, size, offset))
		if err != nil {
			t.Fatalf("read from %d: %v", offset, err)
		}
		if !bytes.Equal(got, plaintext[offset:]) {
			t.Fatalf("offset %d: plaintext does not match", offset)
		}
	}
}

// Reading a short window from deep inside the object must produce exactly that
// window — this is what a player's seek turns into.
func TestChunkReaderServesAShortWindowFromDeepInsideTheObject(t *testing.T) {
	const size = 5 * crypto.ChunkSize
	const offset = 4*crypto.ChunkSize + 1234
	const length = 256
	objectID := uuid.New()
	plaintext := randomPlaintext(t, size)
	stored, dataKey := sealEnvelope(t, plaintext, objectID)

	window := make([]byte, length)
	if _, err := io.ReadFull(readFrom(t, stored, dataKey, objectID, size, offset), window); err != nil {
		t.Fatalf("read window: %v", err)
	}
	if !bytes.Equal(window, plaintext[offset:offset+length]) {
		t.Fatal("window does not match the plaintext")
	}
}

// Integrity is not weakened by starting late. A chunk edited anywhere in the
// range still fails, and it fails as an authentication error rather than as
// altered plaintext.
func TestChunkReaderStillDetectsTamperingInsideTheRange(t *testing.T) {
	const size = 3 * crypto.ChunkSize
	objectID := uuid.New()
	stored, dataKey := sealEnvelope(t, randomPlaintext(t, size), objectID)

	// Flip a byte inside chunk 2, which is the chunk a read from 2*ChunkSize
	// starts on.
	tampered := append([]byte(nil), stored...)
	_, ciphertextOffset, _ := crypto.ChunkLocation(2 * crypto.ChunkSize)
	tampered[ciphertextOffset+100] ^= 0xff

	_, err := io.ReadAll(readFrom(t, tampered, dataKey, objectID, size, 2*crypto.ChunkSize))
	if !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected an authentication failure, got %v", err)
	}
}

// A chunk cannot be opened at a position it was not sealed at, so an object
// whose chunks were reordered fails even when only part of it is read.
func TestChunkReaderRefusesAChunkOpenedAtTheWrongIndex(t *testing.T) {
	const size = 3 * crypto.ChunkSize
	objectID := uuid.New()
	stored, dataKey := sealEnvelope(t, randomPlaintext(t, size), objectID)

	// Hand chunk 0's bytes to a reader that believes it is reading chunk 1.
	reader, err := crypto.NewChunkReader(
		stored[:crypto.EnvelopeHeaderSize], bytes.NewReader(stored[crypto.EnvelopeHeaderSize:]),
		dataKey, objectID, size, 1,
	)
	if err != nil {
		t.Fatalf("chunk reader: %v", err)
	}
	if _, err := io.ReadAll(reader); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected an authentication failure, got %v", err)
	}
}

// A chunk lifted from another object fails, because the object's identity is
// authenticated in every chunk and the key is per object.
func TestChunkReaderRefusesAnotherObjectsChunks(t *testing.T) {
	const size = 2 * crypto.ChunkSize
	objectID := uuid.New()
	stored, dataKey := sealEnvelope(t, randomPlaintext(t, size), objectID)

	_, ciphertextOffset, _ := crypto.ChunkLocation(crypto.ChunkSize)
	reader, err := crypto.NewChunkReader(
		stored[:crypto.EnvelopeHeaderSize], bytes.NewReader(stored[ciphertextOffset:]),
		dataKey, uuid.New(), size, 1,
	)
	if err != nil {
		t.Fatalf("chunk reader: %v", err)
	}
	if _, err := io.ReadAll(reader); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected an authentication failure, got %v", err)
	}
}

// The header is not authenticated on its own and does not need to be: an edited
// base nonce makes every chunk's tag fail, and a foreign magic is refused before
// a byte is read.
func TestChunkReaderRefusesABrokenHeader(t *testing.T) {
	const size = 2 * crypto.ChunkSize
	objectID := uuid.New()
	stored, dataKey := sealEnvelope(t, randomPlaintext(t, size), objectID)
	header := append([]byte(nil), stored[:crypto.EnvelopeHeaderSize]...)

	t.Run("foreign magic", func(t *testing.T) {
		broken := append([]byte(nil), header...)
		broken[0] = 'X'
		if _, err := crypto.NewChunkReader(
			broken, bytes.NewReader(stored[crypto.EnvelopeHeaderSize:]), dataKey, objectID, size, 0,
		); !errors.Is(err, crypto.ErrCiphertext) {
			t.Fatalf("expected a ciphertext error, got %v", err)
		}
	})

	t.Run("truncated header", func(t *testing.T) {
		if _, err := crypto.NewChunkReader(
			header[:4], bytes.NewReader(stored[crypto.EnvelopeHeaderSize:]), dataKey, objectID, size, 0,
		); !errors.Is(err, crypto.ErrCiphertext) {
			t.Fatalf("expected a ciphertext error, got %v", err)
		}
	})

	t.Run("edited base nonce", func(t *testing.T) {
		broken := append([]byte(nil), header...)
		broken[len(broken)-1] ^= 0xff
		reader, err := crypto.NewChunkReader(
			broken, bytes.NewReader(stored[crypto.EnvelopeHeaderSize:]), dataKey, objectID, size, 0,
		)
		if err != nil {
			t.Fatalf("chunk reader: %v", err)
		}
		if _, err := io.ReadAll(reader); !errors.Is(err, crypto.ErrCiphertext) {
			t.Fatalf("expected an authentication failure, got %v", err)
		}
	})
}

// A first chunk past the end of the plaintext describes an object that cannot
// exist, and is refused rather than producing a reader that would run past the
// authenticated length.
func TestChunkReaderRefusesAChunkBeyondThePlaintext(t *testing.T) {
	objectID := uuid.New()
	stored, dataKey := sealEnvelope(t, randomPlaintext(t, 100), objectID)

	if _, err := crypto.NewChunkReader(
		stored[:crypto.EnvelopeHeaderSize], bytes.NewReader(nil), dataKey, objectID, 100, 5,
	); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected a ciphertext error, got %v", err)
	}
}

// Reading to the end from a late chunk still checks the authenticated length, so
// a stored object shortened to fewer chunks than its size claims fails.
func TestChunkReaderStillEnforcesTheAuthenticatedLength(t *testing.T) {
	const size = 3 * crypto.ChunkSize
	objectID := uuid.New()
	stored, dataKey := sealEnvelope(t, randomPlaintext(t, size), objectID)

	// Claim a longer plaintext than the object holds: the authenticated final
	// frame then arrives before the length is reached.
	reader, err := crypto.NewChunkReader(
		stored[:crypto.EnvelopeHeaderSize], bytes.NewReader(stored[crypto.EnvelopeHeaderSize:]),
		dataKey, objectID, size+1, 0,
	)
	if err != nil {
		t.Fatalf("chunk reader: %v", err)
	}
	if _, err := io.ReadAll(reader); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected a length failure, got %v", err)
	}
}
