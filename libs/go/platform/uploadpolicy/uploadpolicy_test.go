package uploadpolicy_test

import (
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
)

func TestDefaultIsTwoHundredFiftyMiB(t *testing.T) {
	if uploadpolicy.DefaultMaxUploadBytes != 262144000 {
		t.Fatalf("RF-32 default must be 250 MiB (262144000 bytes), got %d",
			uploadpolicy.DefaultMaxUploadBytes)
	}
	// The requirement is stated as "250 MB". Binary is the more permissive
	// reading, so a file a user calls 250 MB must still fit.
	if uploadpolicy.DefaultMaxUploadBytes < 250_000_000 {
		t.Fatal("the default must accept a 250,000,000-byte file")
	}
	if uploadpolicy.MinMaxUploadBytes >= uploadpolicy.DefaultMaxUploadBytes {
		t.Fatal("minimum bound must be below the default")
	}
	if uploadpolicy.MaxMaxUploadBytes <= uploadpolicy.DefaultMaxUploadBytes {
		t.Fatal("maximum bound must be above the default")
	}
}

func TestValidAcceptsOnlyWholeMiBInsideTheBounds(t *testing.T) {
	valid := []int64{
		uploadpolicy.MinMaxUploadBytes,     // 1 MiB
		uploadpolicy.DefaultMaxUploadBytes, // 250 MiB
		uploadpolicy.MaxMaxUploadBytes,     // 512 MiB
		100 * uploadpolicy.BytesPerMiB,
	}
	for _, value := range valid {
		if !uploadpolicy.Valid(value) {
			t.Fatalf("%d must be a valid limit", value)
		}
	}

	invalid := []int64{
		0, -1, -uploadpolicy.DefaultMaxUploadBytes,
		uploadpolicy.MinMaxUploadBytes - 1, // one byte below the floor
		uploadpolicy.MinMaxUploadBytes + 1, // one byte above the floor
		uploadpolicy.MaxMaxUploadBytes + 1, // one byte above the ceiling
		uploadpolicy.MaxMaxUploadBytes - 1, // inside the range, not a whole MiB
		3 * uploadpolicy.BytesPerMiB / 2,   // 1.5 MiB — the rounding case
		250*uploadpolicy.BytesPerMiB - 1,
		1 << 62,
	}
	for _, value := range invalid {
		if uploadpolicy.Valid(value) {
			t.Fatalf("%d must be rejected", value)
		}
	}
}

// The unit itself is part of the contract: the database CHECK, the admin UI and
// this package all have to agree on what one MiB is.
func TestBytesPerMiBIsBinary(t *testing.T) {
	if uploadpolicy.BytesPerMiB != 1048576 {
		t.Fatalf("expected 1048576 bytes per MiB, got %d", uploadpolicy.BytesPerMiB)
	}
	for _, bound := range []int64{
		uploadpolicy.MinMaxUploadBytes,
		uploadpolicy.DefaultMaxUploadBytes,
		uploadpolicy.MaxMaxUploadBytes,
	} {
		if bound%uploadpolicy.BytesPerMiB != 0 {
			t.Fatalf("bound %d must itself be a whole number of MiB", bound)
		}
	}
}

func TestEffectiveFallsBackToTheDefaultNeverToUnlimited(t *testing.T) {
	// A row written before the migration reads as zero; an over-wide value can
	// only come from a struct that bypassed validation. Both resolve to the
	// default, which is the only safe answer.
	for _, value := range []int64{
		0, -1, uploadpolicy.MaxMaxUploadBytes + 1, 3 * uploadpolicy.BytesPerMiB / 2,
	} {
		if got := uploadpolicy.Effective(value); got != uploadpolicy.DefaultMaxUploadBytes {
			t.Fatalf("Effective(%d) = %d, want the default %d",
				value, got, uploadpolicy.DefaultMaxUploadBytes)
		}
	}
	if got := uploadpolicy.Effective(uploadpolicy.MinMaxUploadBytes); got != uploadpolicy.MinMaxUploadBytes {
		t.Fatalf("a valid limit must be returned unchanged, got %d", got)
	}
}

func TestEffectiveUnderNarrowsToTheDeploymentCeiling(t *testing.T) {
	const ceiling = 8 << 20

	if got := uploadpolicy.EffectiveUnder(uploadpolicy.DefaultMaxUploadBytes, ceiling); got != ceiling {
		t.Fatalf("a ceiling below the policy must win, got %d", got)
	}
	if got := uploadpolicy.EffectiveUnder(ceiling, uploadpolicy.MaxMaxUploadBytes); got != ceiling {
		t.Fatalf("a ceiling above the policy must not widen it, got %d", got)
	}
	// No ceiling configured leaves the policy alone rather than resolving to
	// zero, which would refuse every upload.
	if got := uploadpolicy.EffectiveUnder(ceiling, 0); got != ceiling {
		t.Fatalf("an absent ceiling must leave the policy alone, got %d", got)
	}
	// An invalid stored policy still falls back to the default before the
	// ceiling is applied, so a corrupt row never becomes "unlimited".
	if got := uploadpolicy.EffectiveUnder(0, uploadpolicy.MaxMaxUploadBytes); got != uploadpolicy.DefaultMaxUploadBytes {
		t.Fatalf("an invalid policy must resolve to the default, got %d", got)
	}
}

// The gateway's hard cap is duplicated into nginx configuration, Kubernetes
// manifests, the CI gate and the documentation, so the one number they all
// restate is pinned here.
func TestGatewayHardCapIsTheCeilingPlusMultipartOverhead(t *testing.T) {
	if uploadpolicy.MultipartOverheadBytes != 8192 {
		t.Fatalf("expected 8 KiB of multipart overhead, got %d", uploadpolicy.MultipartOverheadBytes)
	}
	if uploadpolicy.GatewayHardCapBytes != 536879104 {
		t.Fatalf("expected a 536879104-byte hard cap, got %d", uploadpolicy.GatewayHardCapBytes)
	}
	// The cap must never be able to refuse a file the policy allows.
	if uploadpolicy.GatewayHardCapBytes <= uploadpolicy.MaxMaxUploadBytes {
		t.Fatal("the hard cap must leave room above the largest administrative limit")
	}
	// The overhead is headroom for framing, never extra file size: the cap is
	// not itself a valid policy value.
	if uploadpolicy.Valid(uploadpolicy.GatewayHardCapBytes) {
		t.Fatal("the hard cap must not be expressible as an administrative limit")
	}
}
