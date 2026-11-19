package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// The owned runner mounts only the versioned synthetic #1128 corpus here.
func testStructuralNativeRoundTrips(t *testing.T, ctx context.Context, pool *pgxpool.Pool, malware scanner.Scanner) {
	t.Helper()
	entries, err := os.ReadDir("/corpus")
	efficacyOK(t, err)
	for _, entry := range entries {
		if entry.Name() == "README.md" {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("/corpus", entry.Name()))
			efficacyOK(t, err)
			input := service.UploadInput{Filename: entry.Name(), DeclaredMIME: "application/octet-stream", Content: bytes.NewReader(body)}
			f, worker, _, _ := newEfficacyFixture(t, ctx, pool, malware, input, domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID})
			worker.SetFormatProfiles(filepolicy.Profiles{Data: true, Office: true, Archives: true})
			assertEfficacyBlocked(t, f)
			n, err := worker.ProcessDue(ctx)
			efficacyOK(t, err)
			if n != 1 {
				t.Fatal("no composed scan")
			}
			assertStructuralOriginalAndRanges(t, f, body)
		})
	}
}

func assertStructuralOriginalAndRanges(t *testing.T, f *gateFixture, body []byte) {
	t.Helper()
	original := f.get(t, "/content", nil)
	if original.Code != http.StatusOK || !bytes.Equal(original.Body.Bytes(), body) || sha256.Sum256(original.Body.Bytes()) != sha256.Sum256(body) || original.Header().Get("Content-Length") != fmt.Sprint(len(body)) {
		t.Fatal("original bytes, size or SHA-256 changed")
	}
	var rebuilt []byte
	for _, segment := range [][2]int{{0, len(body)/2 - 1}, {len(body) / 2, len(body) - 1}} {
		r := f.get(t, "/content", map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", segment[0], segment[1])})
		if r.Code != http.StatusPartialContent {
			t.Fatalf("range status=%d", r.Code)
		}
		rebuilt = append(rebuilt, r.Body.Bytes()...)
	}
	if !bytes.Equal(rebuilt, body) || sha256.Sum256(rebuilt) != sha256.Sum256(body) {
		t.Fatal("ranges changed the original")
	}
}
