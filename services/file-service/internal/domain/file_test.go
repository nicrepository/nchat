package domain_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

func TestDefaultMaxUploadBytesIsTwoHundredFiftyMiB(t *testing.T) {
	if domain.DefaultMaxUploadBytes != 250*1024*1024 {
		t.Fatalf("RF-32 default must be 250 MiB, got %d", domain.DefaultMaxUploadBytes)
	}
	if domain.MinMaxUploadBytes >= domain.DefaultMaxUploadBytes {
		t.Fatal("minimum bound must be below the default")
	}
	if domain.MaxMaxUploadBytes <= domain.DefaultMaxUploadBytes {
		t.Fatal("maximum bound must be above the default")
	}
}

func TestDestinationKindValid(t *testing.T) {
	if !domain.DestinationKindChannel.Valid() || !domain.DestinationKindDM.Valid() {
		t.Fatal("channel and dm must be valid kinds")
	}
	for _, kind := range []domain.DestinationKind{"", "workspace", "CHANNEL", "dm "} {
		if kind.Valid() {
			t.Fatalf("kind %q must be invalid", kind)
		}
	}
}

func TestNewDestinationAcceptsExactlyOneKindAndUUID(t *testing.T) {
	id := uuid.NewString()
	destination, err := domain.NewDestination(domain.DestinationKindChannel, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if destination.Kind != domain.DestinationKindChannel || destination.ID != id {
		t.Fatalf("unexpected destination: %+v", destination)
	}
}

func TestNewDestinationRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		kind domain.DestinationKind
		id   string
	}{
		{name: "unknown kind", kind: "workspace", id: uuid.NewString()},
		{name: "empty kind", kind: "", id: uuid.NewString()},
		{name: "non uuid id", kind: domain.DestinationKindChannel, id: "not-a-uuid"},
		{name: "empty id", kind: domain.DestinationKindDM, id: ""},
		{name: "path in id", kind: domain.DestinationKindDM, id: "../../etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := domain.NewDestination(tt.kind, tt.id); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestStatusValidAndDownloadable(t *testing.T) {
	valid := []domain.Status{
		domain.StatusPendingUpload, domain.StatusPendingScan, domain.StatusClean,
		domain.StatusRejected, domain.StatusFailed, domain.StatusDeleted,
	}
	for _, status := range valid {
		if !status.Valid() {
			t.Fatalf("status %q must be valid", status)
		}
	}
	for _, status := range []domain.Status{"", "scanned", "CLEAN", "pending"} {
		if status.Valid() {
			t.Fatalf("status %q must be invalid", status)
		}
	}
	if !domain.StatusClean.Downloadable() {
		t.Fatal("clean must be downloadable")
	}
	for _, status := range []domain.Status{
		domain.StatusPendingUpload, domain.StatusPendingScan,
		domain.StatusRejected, domain.StatusFailed, domain.StatusDeleted,
	} {
		if status.Downloadable() {
			t.Fatalf("status %q must not be downloadable", status)
		}
	}
}

func TestNormalizeFilenameStripsPathAndControlCharacters(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain", raw: "report.pdf", want: "report.pdf"},
		{name: "unix traversal", raw: "../../etc/passwd", want: "passwd"},
		{name: "windows traversal", raw: `..\..\windows\system32\cmd.exe`, want: "cmd.exe"},
		{name: "absolute path", raw: "/var/log/syslog", want: "syslog"},
		{name: "nul byte", raw: "invoice\x00.pdf", want: "invoice.pdf"},
		{name: "newline", raw: "a\r\nb.txt", want: "ab.txt"},
		{name: "tab and spaces", raw: "  spaced.txt \t", want: "spaced.txt"},
		{name: "leading dots", raw: "...hidden.txt", want: "hidden.txt"},
		{name: "unicode preserved", raw: "relatório-ção.pdf", want: "relatório-ção.pdf"},
		{name: "quotes kept as text", raw: `we"ird.txt`, want: `we"ird.txt`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.NormalizeFilename(tt.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
			if strings.ContainsAny(got, `/\`) {
				t.Fatalf("normalized name must not contain separators: %q", got)
			}
		})
	}
}

func TestNormalizeFilenameRejectsEmptyResults(t *testing.T) {
	for _, raw := range []string{"", "   ", "...", "/", `\`, "/tmp/", "\x00", "..", "."} {
		if _, err := domain.NormalizeFilename(raw); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for %q, got %v", raw, err)
		}
	}
}

func TestNormalizeFilenameRejectsInvalidUTF8(t *testing.T) {
	if _, err := domain.NormalizeFilename("bad\xffname.txt"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestNormalizeFilenameTruncatesLongNames(t *testing.T) {
	got, err := domain.NormalizeFilename(strings.Repeat("a", 4000) + ".pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != domain.MaxFilenameBytes {
		t.Fatalf("expected %d bytes, got %d", domain.MaxFilenameBytes, len(got))
	}
}

func TestNormalizeFilenameTruncatesOnRuneBoundary(t *testing.T) {
	// Each "é" is two bytes, so a cut at 255 lands mid-rune and must back off.
	got, err := domain.NormalizeFilename(strings.Repeat("é", 400))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) > domain.MaxFilenameBytes {
		t.Fatalf("expected at most %d bytes, got %d", domain.MaxFilenameBytes, len(got))
	}
	if !isValidUTF8(got) {
		t.Fatalf("truncation produced invalid UTF-8: %q", got)
	}
}

func TestNormalizeDeclaredMIME(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain", raw: "image/png", want: "image/png"},
		{name: "trimmed", raw: "  text/plain  ", want: "text/plain"},
		{name: "control characters removed", raw: "text/pl\x00ain", want: "text/plain"},
		{name: "empty falls back", raw: "", want: domain.DefaultContentType},
		{name: "non ascii falls back", raw: "çãé", want: domain.DefaultContentType},
		{name: "control only falls back", raw: "\x00\x01\x02", want: domain.DefaultContentType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.NormalizeDeclaredMIME(tt.raw); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestNormalizeDeclaredMIMETruncates(t *testing.T) {
	got := domain.NormalizeDeclaredMIME(strings.Repeat("a", 1000))
	if len(got) != domain.MaxDeclaredMIMEBytes {
		t.Fatalf("expected %d bytes, got %d", domain.MaxDeclaredMIMEBytes, len(got))
	}
}

func TestStorageObjectKeyUsesOnlyTheServerGeneratedID(t *testing.T) {
	id := uuid.New()
	key := domain.StorageObjectKey(id)
	if key != "nchat/attachments/"+id.String() {
		t.Fatalf("unexpected key %q", key)
	}
	if strings.Contains(key, "..") {
		t.Fatal("key must not contain a dot segment")
	}
}

func TestErrorsAreCategorised(t *testing.T) {
	if !errors.Is(domain.ErrEmptyFile, domain.ErrInvalidInput) {
		t.Fatal("empty file must be an invalid-input error")
	}
	if !errors.Is(domain.ErrTooManyFiles, domain.ErrInvalidInput) {
		t.Fatal("too many files must be an invalid-input error")
	}
	if !errors.Is(domain.ErrUploadsDisabled, domain.ErrUnavailable) {
		t.Fatal("uploads disabled must be an unavailable error")
	}
	if !errors.Is(domain.ErrDependenciesUnavailable, domain.ErrUnavailable) {
		t.Fatal("dependencies unavailable must be an unavailable error")
	}
	if errors.Is(domain.ErrTooLarge, domain.ErrInvalidInput) {
		t.Fatal("too large must stay distinguishable from a generic bad request")
	}
}

func isValidUTF8(value string) bool {
	for _, r := range value {
		if r == '�' {
			return false
		}
	}
	return true
}

// --- voice messages (issue #670) -----------------------------------------

func TestAudioKindValid(t *testing.T) {
	if !domain.AudioKind("").Valid() || !domain.AudioKindVoice.Valid() {
		t.Fatal("unset and voice must both be valid")
	}
	for _, kind := range []domain.AudioKind{"Voice", "music", "voice "} {
		if kind.Valid() {
			t.Fatalf("kind %q must be invalid", kind)
		}
	}
}

func TestNormalizeDeclaredDurationMs(t *testing.T) {
	if _, ok := domain.NormalizeDeclaredDurationMs(""); ok {
		t.Fatal("an absent value must not be declared")
	}
	if _, ok := domain.NormalizeDeclaredDurationMs("not-a-number"); ok {
		t.Fatal("garbage must not be declared")
	}
	if _, ok := domain.NormalizeDeclaredDurationMs("0"); ok {
		t.Fatal("zero must not be declared")
	}
	if _, ok := domain.NormalizeDeclaredDurationMs("-5"); ok {
		t.Fatal("a negative value must not be declared")
	}
	if _, ok := domain.NormalizeDeclaredDurationMs("99999999999"); ok {
		t.Fatal("an absurd value must not be declared")
	}
	value, ok := domain.NormalizeDeclaredDurationMs("4200")
	if !ok || value != 4200 {
		t.Fatalf("expected 4200, got %d (ok=%v)", value, ok)
	}
	value, ok = domain.NormalizeDeclaredDurationMs(" 4200 ")
	if !ok || value != 4200 {
		t.Fatalf("surrounding whitespace must be tolerated, got %d (ok=%v)", value, ok)
	}
	ceiling := domain.MaxDeclaredDurationMs
	if value, ok := domain.NormalizeDeclaredDurationMs(fmt.Sprint(ceiling)); !ok || int64(value) != int64(ceiling) {
		t.Fatalf("the ceiling itself must be accepted, got %d (ok=%v)", value, ok)
	}
	if _, ok := domain.NormalizeDeclaredDurationMs(fmt.Sprint(ceiling + 1)); ok {
		t.Fatal("one past the ceiling must not be declared")
	}
}

func TestVoiceCompatibleContent(t *testing.T) {
	for _, mime := range []string{
		"audio/mpeg", "audio/ogg", "audio/wav", "audio/wave", "audio/x-wav",
		"application/ogg", "video/webm", "video/mp4",
		// A codecs parameter must not change the answer: only the bare type
		// is ever compared.
		"video/webm; codecs=opus",
	} {
		if !domain.VoiceCompatibleContent(mime) {
			t.Fatalf("%q must be voice-compatible", mime)
		}
	}
	for _, mime := range []string{
		"application/pdf", "image/png", "text/plain", "video/quicktime", "",
	} {
		if domain.VoiceCompatibleContent(mime) {
			t.Fatalf("%q must not be voice-compatible", mime)
		}
	}
}
