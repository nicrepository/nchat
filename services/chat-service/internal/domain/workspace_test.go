package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The RF-19 and RF-32 policy helpers are thin re-exports of the shared platform
// packages, and that is exactly why they are worth pinning here: the whole point
// of the re-export is that chat-service enforces the same bounds the database
// CHECK and the Admin Console do. A helper rewired to a different package, or to
// a hand-rolled bound, would still compile and still return a number.
//
// What every case below protects is the one property that turns a policy bug
// into a security bug: no input may produce "no limit".

func TestValidMaxUploadBytes_EnforcesTheRF32Bounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		value int64
		want  bool
	}{
		{name: "at the floor", value: domain.MinMaxUploadBytes, want: true},
		{name: "at the ceiling", value: domain.MaxMaxUploadBytes, want: true},
		{name: "the default is itself acceptable", value: domain.DefaultMaxUploadBytes, want: true},
		{name: "below the floor", value: domain.MinMaxUploadBytes - 1, want: false},
		{name: "above the ceiling", value: domain.MaxMaxUploadBytes + 1, want: false},
		// "Unlimited" must not be expressible, in either spelling.
		{name: "zero is not a limit", value: 0, want: false},
		{name: "negative is not a limit", value: -1, want: false},
		// The column is stored in bytes but administered in MiB, so a value
		// that is not a whole MiB is a typo rather than a policy.
		{name: "not a whole MiB", value: domain.MinMaxUploadBytes + 1, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := domain.ValidMaxUploadBytes(test.value); got != test.want {
				t.Fatalf("ValidMaxUploadBytes(%d) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestEffectiveMaxUploadBytes_NeverAnswersNoLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		value int64
		want  int64
	}{
		{name: "an in-range policy is kept", value: domain.MinMaxUploadBytes, want: domain.MinMaxUploadBytes},
		{name: "the ceiling is kept", value: domain.MaxMaxUploadBytes, want: domain.MaxMaxUploadBytes},
		// A row written before migration 000020, or a struct nobody populated,
		// reads as zero. Enforcing with it would mean accepting any file.
		{name: "an unpopulated value falls back to the default", value: 0, want: domain.DefaultMaxUploadBytes},
		{name: "a negative value falls back to the default", value: -1, want: domain.DefaultMaxUploadBytes},
		{name: "an over-ceiling value falls back to the default", value: domain.MaxMaxUploadBytes + 1, want: domain.DefaultMaxUploadBytes},
		{name: "an under-floor value falls back to the default", value: domain.MinMaxUploadBytes - 1, want: domain.DefaultMaxUploadBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := domain.EffectiveMaxUploadBytes(test.value)
			if got != test.want {
				t.Fatalf("EffectiveMaxUploadBytes(%d) = %d, want %d", test.value, got, test.want)
			}
			if got <= 0 {
				t.Fatalf("EffectiveMaxUploadBytes(%d) = %d, which enforces no limit at all", test.value, got)
			}
		})
	}
}

func TestValidMessageRateLimitPerMinute_EnforcesTheRF19Bounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		value int
		want  bool
	}{
		{name: "at the floor", value: domain.MinMessageRateLimitPerMinute, want: true},
		{name: "at the ceiling", value: domain.MaxMessageRateLimitPerMinute, want: true},
		{name: "the default is itself acceptable", value: domain.DefaultMessageRateLimitPerMinute, want: true},
		// Zero would turn an anti-spam control into a workspace-wide mute, so
		// the floor is 1 rather than 0.
		{name: "zero would mute the workspace", value: 0, want: false},
		{name: "negative", value: -1, want: false},
		// And there is no value meaning "unlimited", which would disable the
		// protection outright.
		{name: "above the ceiling", value: domain.MaxMessageRateLimitPerMinute + 1, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := domain.ValidMessageRateLimitPerMinute(test.value); got != test.want {
				t.Fatalf("ValidMessageRateLimitPerMinute(%d) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestEffectiveMessageRateLimitPerMinute_NeverAnswersNoLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		value int
		want  int
	}{
		{name: "an in-range policy is kept", value: 10, want: 10},
		{name: "the floor is kept", value: domain.MinMessageRateLimitPerMinute, want: domain.MinMessageRateLimitPerMinute},
		{name: "the ceiling is kept", value: domain.MaxMessageRateLimitPerMinute, want: domain.MaxMessageRateLimitPerMinute},
		// A row written before migration 000018 reads as zero. Handing that to
		// the limiter as-is would either mute the workspace or, if read as
		// "unset", stop limiting anyone.
		{name: "an unpopulated value falls back to the default", value: 0, want: domain.DefaultMessageRateLimitPerMinute},
		{name: "a negative value falls back to the default", value: -5, want: domain.DefaultMessageRateLimitPerMinute},
		{name: "an over-ceiling value falls back to the default", value: domain.MaxMessageRateLimitPerMinute + 1, want: domain.DefaultMessageRateLimitPerMinute},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := domain.EffectiveMessageRateLimitPerMinute(test.value)
			if got != test.want {
				t.Fatalf("EffectiveMessageRateLimitPerMinute(%d) = %d, want %d", test.value, got, test.want)
			}
			if got < domain.MinMessageRateLimitPerMinute {
				t.Fatalf("EffectiveMessageRateLimitPerMinute(%d) = %d, below the enforceable floor", test.value, got)
			}
		})
	}
}

// NormalizeChannelDisplayName is the single rule behind every writer of
// chat.channels.display_name — creation, update and the workspace bootstrap.
// A weaker rule on any one of those paths is a resource cap bypass.

func TestNormalizeChannelDisplayName_TrimsAndAccepts(t *testing.T) {
	atCap := strings.Repeat("a", domain.MaxChannelDisplayNameCodePoints)
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain", input: "Engenharia", want: "Engenharia"},
		{name: "surrounding spaces are trimmed", input: "   Engenharia   ", want: "Engenharia"},
		{name: "inner spaces are preserved", input: " Engenharia de Plataforma ", want: "Engenharia de Plataforma"},
		{name: "tabs and newlines around the value are trimmed", input: "\t Engenharia \n", want: "Engenharia"},
		{name: "at the cap", input: atCap, want: atCap},
		// The cap counts code points, so a name of multi-byte characters gets
		// the same allowance as an ASCII one rather than a fraction of it.
		{name: "at the cap in accented characters", input: strings.Repeat("á", domain.MaxChannelDisplayNameCodePoints), want: strings.Repeat("á", domain.MaxChannelDisplayNameCodePoints)},
		{name: "at the cap in emoji", input: strings.Repeat("🙂", domain.MaxChannelDisplayNameCodePoints), want: strings.Repeat("🙂", domain.MaxChannelDisplayNameCodePoints)},
		// Trimming happens before measuring, so padding cannot push an
		// otherwise-legal name over the cap.
		{name: "padding is not counted against the cap", input: "   " + atCap + "   ", want: atCap},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := domain.NormalizeChannelDisplayName(test.input)
			if err != nil {
				t.Fatalf("NormalizeChannelDisplayName(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeChannelDisplayName_Rejects(t *testing.T) {
	tooLong := strings.Repeat("a", domain.MaxChannelDisplayNameCodePoints+1)
	for _, test := range []struct {
		name    string
		input   string
		wantErr error
	}{
		{name: "empty", input: "", wantErr: domain.ErrChannelDisplayNameRequired},
		{name: "only spaces", input: "   ", wantErr: domain.ErrChannelDisplayNameRequired},
		{name: "only whitespace characters", input: "\t\n  \r", wantErr: domain.ErrChannelDisplayNameRequired},
		{name: "one code point past the cap", input: tooLong, wantErr: domain.ErrChannelDisplayNameTooLong},
		// Multi-byte characters are counted as code points here too, so a name
		// of emoji is refused at the same length an ASCII one is — not at a
		// byte count that would refuse it far earlier.
		{name: "one emoji past the cap", input: strings.Repeat("🙂", domain.MaxChannelDisplayNameCodePoints+1), wantErr: domain.ErrChannelDisplayNameTooLong},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := domain.NormalizeChannelDisplayName(test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NormalizeChannelDisplayName(%q) error = %v, want %v", test.input, err, test.wantErr)
			}
			// Both errors are also ErrInvalidInput, which is what maps them to
			// 400 rather than 500 at the HTTP edge.
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error %v does not classify as ErrInvalidInput", err)
			}
			if got != "" {
				t.Fatalf("a rejected name must not be returned for storage, got %q", got)
			}
		})
	}
}

// A rejected name can be tens of kilobytes of caller-controlled text. Echoing it
// back would put it in every error body and every log line that wraps the error.
func TestNormalizeChannelDisplayName_ErrorsDoNotEchoTheRejectedValue(t *testing.T) {
	secret := strings.Repeat("segredo", domain.MaxChannelDisplayNameCodePoints)

	_, err := domain.NormalizeChannelDisplayName(secret)
	if err == nil {
		t.Fatal("expected an over-cap name to be refused")
	}
	if strings.Contains(err.Error(), "segredo") {
		t.Fatalf("error echoes the rejected value: %v", err)
	}
}
