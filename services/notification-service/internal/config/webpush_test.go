package config_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/notification-service/internal/config"
)

// Issue #746: Web Push configuration is a fail-safe boundary.
//
// Two properties are being defended. A deployment that cannot send correctly
// must be refused at start-up rather than at the first notification, because a
// misconfigured send is either a failed delivery nobody notices or an
// Authorization header signed with something that is not a key. And a refusal
// must never quote the value it refused: this is the last place a VAPID private
// key could become a log line.

// vapidPair generates a structurally valid key pair the way a VAPID generator
// would. Generated, never committed: a private key in the repository would be a
// secret in version control whatever it was for.
func vapidPair(t *testing.T) (public, private string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256 key: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(key.Bytes())
}

func validWebPush(t *testing.T) config.WebPushConfig {
	t.Helper()
	public, private := vapidPair(t)
	return config.WebPushConfig{
		VAPIDPublicKey:  public,
		VAPIDPrivateKey: private,
		VAPIDSubject:    "mailto:ops@example.test",
		TTLSeconds:      3600,
	}
}

func TestWebPushAcceptsACompleteConfiguration(t *testing.T) {
	ready, reason := validWebPush(t).Ready()
	if !ready {
		t.Fatalf("a complete configuration was refused: %s", reason)
	}
	if reason != "" {
		t.Fatalf("a ready configuration produced a reason: %q", reason)
	}
}

// Padded base64url is accepted, because generators differ on padding and a
// correct key must not be refused over it.
func TestWebPushAcceptsPaddedBase64URL(t *testing.T) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256 key: %v", err)
	}
	cfg := config.WebPushConfig{
		VAPIDPublicKey:  base64.URLEncoding.EncodeToString(key.PublicKey().Bytes()),
		VAPIDPrivateKey: base64.URLEncoding.EncodeToString(key.Bytes()),
		VAPIDSubject:    "https://example.test/contact",
		TTLSeconds:      3600,
	}
	if ready, reason := cfg.Ready(); !ready {
		t.Fatalf("padded base64url keys were refused: %s", reason)
	}
}

// The standard alphabet is refused, and that is a compatibility requirement
// rather than strictness: webpush-go decodes VAPID keys as base64url only, so a
// standard-alphabet key accepted here would fail at the first send. This is the
// exact shape of the defect the semantic validation exists to remove — a
// configuration that passes startup and breaks in production.
//
// The key is chosen so its standard encoding actually differs from its URL-safe
// one; otherwise the two alphabets agree and the test proves nothing.
func TestWebPushRefusesTheStandardBase64Alphabet(t *testing.T) {
	var key *ecdh.PrivateKey
	for range 64 {
		candidate, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate P-256 key: %v", err)
		}
		encoded := base64.RawStdEncoding.EncodeToString(candidate.PublicKey().Bytes())
		if strings.ContainsAny(encoded, "+/") {
			key = candidate
			break
		}
	}
	if key == nil {
		t.Skip("no key encoded with an alphabet-specific character")
	}

	cfg := config.WebPushConfig{
		VAPIDPublicKey:  base64.RawStdEncoding.EncodeToString(key.PublicKey().Bytes()),
		VAPIDPrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()),
		VAPIDSubject:    "mailto:ops@example.test",
		TTLSeconds:      3600,
	}
	if ready, _ := cfg.Ready(); ready {
		t.Fatal("a standard-alphabet key was accepted; webpush-go cannot decode one")
	}
}

func TestWebPushRefusesAnIncompleteConfiguration(t *testing.T) {
	complete := validWebPush(t)

	cases := map[string]func(c *config.WebPushConfig){
		"no public key":  func(c *config.WebPushConfig) { c.VAPIDPublicKey = "" },
		"no private key": func(c *config.WebPushConfig) { c.VAPIDPrivateKey = "" },
		"no subject":     func(c *config.WebPushConfig) { c.VAPIDSubject = "" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := complete
			breakIt(&cfg)
			ready, reason := cfg.Ready()
			if ready {
				t.Fatal("an incomplete configuration was accepted")
			}
			if reason == "" {
				t.Fatal("a refusal produced no reason")
			}
		})
	}
}

// Every way a key can be wrong is caught at start-up, not discovered by the
// first notification.
//
// This is the finding: a length and a leading 0x04 prove nothing. A scalar can
// be zero or past the curve order; sixty-five bytes beginning 0x04 can name a
// point that is not on P-256. Each of these used to start a worker and fail at
// send time, where the failure was classified as a permanent delivery error and
// the outbox row was retired — a configuration mistake turned into lost
// notifications.
func TestWebPushRefusesKeysThatAreNotKeys(t *testing.T) {
	public, private := vapidPair(t)

	cases := map[string]config.WebPushConfig{
		"private key is not base64": {
			VAPIDPublicKey: public, VAPIDPrivateKey: "not base64 at all!!",
		},
		"private key is the wrong length": {
			VAPIDPublicKey:  public,
			VAPIDPrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 31)),
		},
		// Structurally perfect and cryptographically meaningless: the right
		// length, and a scalar the curve refuses.
		"private key is the zero scalar": {
			VAPIDPublicKey:  public,
			VAPIDPrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		},
		"private key is at or past the curve order": {
			VAPIDPublicKey:  public,
			VAPIDPrivateKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 32)),
		},
		"public key is not base64": {
			VAPIDPublicKey: "not base64 at all!!", VAPIDPrivateKey: private,
		},
		"public key is the wrong length": {
			VAPIDPublicKey:  base64.RawURLEncoding.EncodeToString([]byte("short")),
			VAPIDPrivateKey: private,
		},
		// Sixty-five bytes, leading 0x04, and not a point on P-256. The old
		// structural check accepted this.
		"public key is not a point on the curve": {
			VAPIDPublicKey:  base64.RawURLEncoding.EncodeToString(offCurvePoint()),
			VAPIDPrivateKey: private,
		},
		"public key is all zeroes behind an uncompressed tag": {
			VAPIDPublicKey:  base64.RawURLEncoding.EncodeToString(append([]byte{0x04}, make([]byte, 64)...)),
			VAPIDPrivateKey: private,
		},
		"public key is compressed": {
			VAPIDPublicKey:  base64.RawURLEncoding.EncodeToString(compressedPoint(t)),
			VAPIDPrivateKey: private,
		},
		"the pair is swapped": {
			VAPIDPublicKey: private, VAPIDPrivateKey: public,
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			cfg.VAPIDSubject = "mailto:ops@example.test"
			ready, reason := cfg.Ready()
			if ready {
				t.Fatal("a key that is not a key was accepted")
			}
			assertNoKeyMaterial(t, reason, cfg)
		})
	}
}

// Two perfectly good keys that are not each other's.
//
// Nothing structural can catch this — both decode, both are the right length,
// both are valid P-256 values. Only deriving the public key from the private
// one and comparing does. Left unchecked, every push would be signed with a
// JWT the push service rejects.
func TestWebPushRefusesAMismatchedPair(t *testing.T) {
	publicA, privateA := vapidPair(t)
	publicB, privateB := vapidPair(t)

	mismatched := config.WebPushConfig{
		VAPIDPublicKey:  publicB,
		VAPIDPrivateKey: privateA,
		VAPIDSubject:    "mailto:ops@example.test",
	}
	ready, reason := mismatched.Ready()
	if ready {
		t.Fatal("a mismatched VAPID pair was accepted")
	}
	if reason == "" {
		t.Fatal("a mismatched pair produced no reason")
	}
	assertNoKeyMaterial(t, reason, mismatched)

	// The control: each key is fine with its own partner, so the refusal above
	// is about the pairing and not about either key.
	for _, pair := range []config.WebPushConfig{
		{VAPIDPublicKey: publicA, VAPIDPrivateKey: privateA},
		{VAPIDPublicKey: publicB, VAPIDPrivateKey: privateB},
	} {
		pair.VAPIDSubject = "mailto:ops@example.test"
		if ready, reason := pair.Ready(); !ready {
			t.Fatalf("a matched pair was refused: %s", reason)
		}
	}
}

// offCurvePoint is 65 bytes with the uncompressed tag whose coordinates do not
// satisfy the P-256 equation.
func offCurvePoint() []byte {
	point := make([]byte, 65)
	point[0] = 0x04
	point[1] = 0x01
	return point
}

// compressedPoint is a real point in the encoding webpush-go and crypto/ecdh
// both refuse for this curve.
func compressedPoint(t *testing.T) []byte {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256 key: %v", err)
	}
	return append([]byte{0x02}, key.PublicKey().Bytes()[1:33]...)
}

func TestWebPushRefusesASubjectThatIsNotAContactURI(t *testing.T) {
	complete := validWebPush(t)

	for _, subject := range []string{
		"ops@example.test",    // a bare address: the library would mangle it
		"http://example.test", // not https
		"mailto:",             // scheme and nothing else
		"https://",            // scheme and nothing else
		"tel:+15550100",       // a scheme RFC 8292 does not admit
		"javascript:alert(1)", // not a contact at all
	} {
		t.Run(subject, func(t *testing.T) {
			cfg := complete
			cfg.VAPIDSubject = subject
			if ready, _ := cfg.Ready(); ready {
				t.Fatalf("subject %q was accepted", subject)
			}
		})
	}
}

// The reason an operator reads must name the variable and never its value.
func assertNoKeyMaterial(t *testing.T, reason string, cfg config.WebPushConfig) {
	t.Helper()
	if reason == "" {
		t.Fatal("a refusal produced no reason")
	}
	for _, secret := range []string{cfg.VAPIDPrivateKey, cfg.VAPIDPublicKey} {
		if secret != "" && strings.Contains(reason, secret) {
			t.Fatalf("the refusal quoted key material: %q", reason)
		}
	}
}

// The TTL is bounded on both sides. Below a minute it would expire events
// inside the worker's own retry ladder; above a day a "new message" is an
// artefact rather than news.
func TestWebPushTTLIsBounded(t *testing.T) {
	cases := map[string]struct {
		given int
		want  int
	}{
		"unset falls back":       {given: 0, want: 14400},
		"negative falls back":    {given: -5, want: 14400},
		"below the floor rises":  {given: 1, want: 60},
		"at the floor stays":     {given: 60, want: 60},
		"in range is kept":       {given: 3600, want: 3600},
		"at the ceiling stays":   {given: 86400, want: 86400},
		"above the ceiling caps": {given: 999999, want: 86400},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := config.WebPushConfig{TTLSeconds: tc.given}.Normalized().TTLSeconds
			if got != tc.want {
				t.Fatalf("TTLSeconds = %d, want %d", got, tc.want)
			}
		})
	}
}

// Normalisation is idempotent: the app wiring calls it more than once, on a
// value the config loader already normalised.
func TestWebPushNormalizationIsIdempotent(t *testing.T) {
	once := config.WebPushConfig{TTLSeconds: 5}.Normalized()
	if twice := once.Normalized(); twice != once {
		t.Fatalf("normalising twice changed the value: %+v then %+v", once, twice)
	}
}

// The loader reads the environment and normalises. The default deployment has
// no keys, which is exactly why the worker declines to start rather than
// starting with a channel it cannot use.
func TestWebPushIsUnconfiguredByDefault(t *testing.T) {
	t.Setenv("NOTIFICATION_VAPID_PUBLIC_KEY", "")
	t.Setenv("NOTIFICATION_VAPID_PRIVATE_KEY", "")
	t.Setenv("NOTIFICATION_VAPID_SUBJECT", "")
	t.Setenv("NOTIFICATION_PUSH_TTL_SECONDS", "")

	cfg := config.Load()
	if ready, reason := cfg.WebPush.Ready(); ready {
		t.Fatal("an unconfigured deployment reported Web Push ready")
	} else if reason == "" {
		t.Fatal("an unconfigured deployment gave no reason")
	}
	if cfg.WebPush.TTLSeconds != 14400 {
		t.Fatalf("default TTLSeconds = %d, want 14400", cfg.WebPush.TTLSeconds)
	}
}

// Values are read from the environment and surrounding whitespace is stripped,
// because a key pasted into a Kubernetes manifest routinely carries a newline.
func TestWebPushReadsTheEnvironment(t *testing.T) {
	public, private := vapidPair(t)
	t.Setenv("NOTIFICATION_VAPID_PUBLIC_KEY", "  "+public+"\n")
	t.Setenv("NOTIFICATION_VAPID_PRIVATE_KEY", private+"  ")
	t.Setenv("NOTIFICATION_VAPID_SUBJECT", " mailto:ops@example.test ")
	t.Setenv("NOTIFICATION_PUSH_TTL_SECONDS", "600")

	cfg := config.Load()
	if ready, reason := cfg.WebPush.Ready(); !ready {
		t.Fatalf("a configured deployment was refused: %s", reason)
	}
	if cfg.WebPush.TTLSeconds != 600 {
		t.Fatalf("TTLSeconds = %d, want 600", cfg.WebPush.TTLSeconds)
	}
}

// The reason must never disclose the private key, and must not reproduce the
// public key either: together with an endpoint the pair is the capability to
// push, and a log line is the wrong place for half of it.
func TestWebPushRefusalsDiscloseNoKeyMaterial(t *testing.T) {
	public, private := vapidPair(t)
	otherPublic, _ := vapidPair(t)

	for name, cfg := range map[string]config.WebPushConfig{
		"mismatched pair": {VAPIDPublicKey: otherPublic, VAPIDPrivateKey: private},
		"bad private key": {VAPIDPublicKey: public, VAPIDPrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
		"bad public key":  {VAPIDPublicKey: base64.RawURLEncoding.EncodeToString(offCurvePoint()), VAPIDPrivateKey: private},
		"bad subject":     {VAPIDPublicKey: public, VAPIDPrivateKey: private, VAPIDSubject: "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			if cfg.VAPIDSubject == "" {
				cfg.VAPIDSubject = "mailto:ops@example.test"
			}
			ready, reason := cfg.Ready()
			if ready {
				t.Fatal("an invalid configuration was accepted")
			}
			assertNoKeyMaterial(t, reason, cfg)
		})
	}
}

// An enabled worker with an unusable channel must not report ready.
//
// Without this the pod goes green while startNotificationWorker has quietly
// declined to start anything: a healthy-looking replica with a growing backlog
// and nothing draining it. A worker that was never enabled stays ready, because
// Web Push is opt-in and its absence is not a misconfiguration.
func TestNotificationWorkerReadinessFollowsTheWebPushChannel(t *testing.T) {
	enabled := func(webPush config.WebPushConfig) config.Config {
		return config.Config{
			DatabaseURL: "postgres://localhost/nchat",
			NotificationWorker: config.NotificationWorkerConfig{
				Enabled: true,
			}.Normalized(),
			WebPush: webPush,
		}
	}
	publicA, privateA := vapidPair(t)
	publicB, _ := vapidPair(t)

	cases := map[string]struct {
		cfg    config.Config
		wantOK bool
	}{
		"disabled worker without any configuration": {
			cfg:    config.Config{DatabaseURL: "postgres://localhost/nchat"},
			wantOK: true,
		},
		"enabled with no channel configured": {
			cfg:    enabled(config.WebPushConfig{}),
			wantOK: false,
		},
		"enabled with a partial configuration": {
			cfg:    enabled(config.WebPushConfig{VAPIDPublicKey: publicA}),
			wantOK: false,
		},
		"enabled with an invalid private key": {
			cfg: enabled(config.WebPushConfig{
				VAPIDPublicKey:  publicA,
				VAPIDPrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
				VAPIDSubject:    "mailto:ops@example.test",
			}),
			wantOK: false,
		},
		"enabled with a mismatched pair": {
			cfg: enabled(config.WebPushConfig{
				VAPIDPublicKey:  publicB,
				VAPIDPrivateKey: privateA,
				VAPIDSubject:    "mailto:ops@example.test",
			}),
			wantOK: false,
		},
		"enabled with a usable channel": {
			cfg:    enabled(validWebPush(t)),
			wantOK: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ok, reason := tc.cfg.NotificationWorkerReady()
			if ok != tc.wantOK {
				t.Fatalf("ready = %v (%q), want %v", ok, reason, tc.wantOK)
			}
			if !ok && reason == "" {
				t.Fatal("a refusal must say why")
			}
		})
	}
}
