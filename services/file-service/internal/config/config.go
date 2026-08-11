package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	platformconfig "github.com/nicrepository/nchat/libs/go/platform/config"
	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

const (
	serviceName = "file-service"
	defaultPort = 8083

	defaultJWTIssuer   = "nchat-auth"
	defaultJWTAudience = "nchat-api"

	// Upload and download stream up to the effective size limit, so the body
	// timeouts are generous. Slow-request protection comes from
	// ReadHeaderTimeoutSeconds and from the byte cap applied to the body, not
	// from these.
	defaultReadTimeoutSeconds  = 300
	defaultWriteTimeoutSeconds = 300

	defaultSeaweedFSTimeoutSeconds = 120

	// defaultMalwareScanTimeoutSeconds bounds one clamd exchange (RF-22). It has
	// to cover streaming the largest attachment this deployment accepts, so it
	// is generous for the same reason the upload body timeouts are: a budget
	// tuned to a small file would make the largest ones permanently unscannable,
	// and therefore permanently undownloadable.
	defaultMalwareScanTimeoutSeconds = 300

	// maxMalwareScanTimeoutSeconds is an operational ceiling, not a policy.
	//
	// It exists for two reasons and neither is "keep the old 300s behaviour":
	// the value is multiplied by time.Second, so an absurd one overflows the
	// int64 duration and produces a negative deadline that cancels instantly;
	// and the claim lease is derived from it, so a budget measured in years
	// would leave a crashed worker's row unclaimable for that long. An hour is
	// twelve times the default and far beyond what streaming the 512 MiB
	// ceiling to a local daemon can take.
	maxMalwareScanTimeoutSeconds = 3600

	// Upload admission defaults. Conservative on purpose: each global slot is a
	// reserved database connection and, at the 512 MiB ceiling, up to that much
	// plaintext being streamed, encrypted and forwarded at once. Four slots put
	// the theoretical worst case at 2 GiB of simultaneous transfers for the
	// whole cluster.
	defaultUploadMaxConcurrent        = 4
	defaultUploadMaxConcurrentPerUser = 2
	defaultUploadRetryAfterSeconds    = 30

	// maxUploadMaxConcurrent is an operational ceiling, not a policy. Beyond it
	// the reserved connections alone would outgrow any sensible pool.
	maxUploadMaxConcurrent = 64

	// dbConnectionHeadroom is how many connections must remain for ordinary
	// queries while every upload slot is occupied — authorization, metadata
	// writes, readiness probes. Without it a fully booked service would
	// deadlock against its own pool.
	dbConnectionHeadroom = 4

	defaultDBMaxConnections = defaultUploadMaxConcurrent + dbConnectionHeadroom + 2

	// RF-10 Open Graph link previews.
	//
	// The timeout is short and it is meant to be: it is the budget for reaching
	// a third-party site nobody here controls, and a request the user is
	// waiting on. A site that cannot answer a document request in five seconds
	// does not get a preview. The per-phase budgets inside the fetcher (dial,
	// TLS, response headers) are smaller still and are not configurable —
	// they are properties of the sandbox, not of a deployment.
	defaultLinkPreviewTimeoutSeconds = 5
	maxLinkPreviewTimeoutSeconds     = 30

	// The cache TTL is long enough that a link pasted into a busy channel is
	// fetched once rather than once per reader, and short enough that an edited
	// page is picked up the same day.
	defaultLinkPreviewCacheTTLSeconds = 900
	maxLinkPreviewCacheTTLSeconds     = 86400
)

type Config struct {
	ServiceName              string
	Env                      string
	Port                     int
	ReadHeaderTimeoutSeconds int
	ReadTimeoutSeconds       int
	WriteTimeoutSeconds      int

	// UploadsEnabled gates every attachment route and every dependency they
	// need. While false the service is health-only, exactly as before RF-30.
	UploadsEnabled bool

	DatabaseURL             string
	DBConnectTimeoutSeconds int
	// DBMaxConnections bounds the pool. It matters here because admission
	// control reserves one connection per in-flight upload, so the pool has to
	// be big enough that a fully booked service can still run ordinary queries.
	DBMaxConnections int

	// UploadMaxConcurrent is how many uploads may be in flight across the whole
	// cluster, and UploadMaxConcurrentPerUser how many of those one
	// authenticated user may hold.
	//
	// These bound work already admitted, which the per-minute rate limiter
	// cannot: that one counts request starts, in one process, so N replicas
	// grant N budgets and a client holding several slow transfers open is never
	// counted at all.
	UploadMaxConcurrent        int
	UploadMaxConcurrentPerUser int
	// UploadRetryAfterSeconds is what a refused client is told to wait.
	UploadRetryAfterSeconds int

	AuthJWTHMACSecret string
	AuthJWTIssuer     string
	AuthJWTAudience   string

	// MaxUploadBytes is the deployment ceiling for one attachment, in bytes.
	//
	// It is NOT the RF-32 product limit. Since issue #458 the limit an upload is
	// judged against is administrative — chat.workspaces.max_upload_bytes, set
	// through the admin endpoint and read per request — and this value only
	// narrows it further for a cluster that cannot absorb the configured policy.
	// It defaults to the domain ceiling, so out of the box it never binds and
	// the administrative value is what applies.
	//
	// Neither control silently rewrites the other: the narrowing happens per
	// request in uploadpolicy.EffectiveUnder and is documented, rather than
	// being clamped into the database.
	MaxUploadBytes int64

	// EncryptionMasterKey is the standard-base64 32-byte KEK used to wrap each
	// object's data key. There is no default and no hardcoded fallback.
	EncryptionMasterKey string

	// EncryptionMasterKeyID is the non-secret identifier persisted with every
	// wrapped data key, so a download knows which KEK opens it without trying
	// any. It is required whenever uploads are enabled: without it a rotation
	// could not tell the two generations of rows apart.
	EncryptionMasterKeyID string

	// EncryptionPreviousKeys are the KEKs kept only so objects wrapped before a
	// rotation can still be read, as comma-separated "id:base64key" pairs. It is
	// optional and empty by default; new uploads always use the active key.
	EncryptionPreviousKeys string

	// MalwareScanRequired keeps a fresh upload in pending_scan, which is not
	// downloadable, until the asynchronous antimalware worker clears it. It
	// defaults to true and must stay true wherever SECURITY.md applies. Setting
	// it to false is a local-development affordance for an environment that has
	// no scanner: uploads finalise as clean and are immediately downloadable.
	MalwareScanRequired bool

	// MalwareScannerAddress is the clamd endpoint, "host:port" (RF-22).
	//
	// It is optional and empty by default, and an empty value does NOT relax the
	// gate: the scan worker simply does not start, so every upload stays in
	// pending_scan and stays undownloadable. That is the fail-closed reading —
	// "no scanner configured" means "nothing is approved", never "everything is".
	// FILE_MALWARE_SCAN_REQUIRED is the only thing that changes what an upload
	// finalises as, and validateScanPolicy governs it.
	MalwareScannerAddress string
	// MalwareScanTimeoutSeconds is the effective budget for scanning one
	// attachment, and it is the *only* one.
	//
	// It bounds the exchange with the daemon (connect, stream, reply) and it is
	// also what the worker's job deadline and its claim lease are derived from.
	// Those used to be a fixed 300s inside the service, which made a longer
	// configured value unreachable: the service cancelled first, the job was
	// recorded as a retry, and no configuration could fix it.
	MalwareScanTimeoutSeconds int

	// ValkeyURL is the broadcast bus this service publishes attachment status
	// changes onto, shared with chat-service. Optional: without it a verdict is
	// still persisted and still authoritative, and clients see it on their next
	// read instead of immediately.
	ValkeyURL string
	// WSInstanceID identifies this process on that bus, for the consumer's
	// echo suppression. Defaults to a generated value.
	WSInstanceID string

	SeaweedFSFilerURL       string
	SeaweedFSTimeoutSeconds int

	// LinkPreviewEnabled gates the RF-10 route. It defaults to false for the
	// same reason FILE_UPLOADS_ENABLED does: the feature makes this service
	// open outbound connections to hosts a user names, so a deployment gets it
	// by asking for it, never by forgetting to say otherwise. While false the
	// route answers 503 and no fetcher is built.
	LinkPreviewEnabled bool
	// LinkPreviewTimeoutSeconds bounds one whole remote exchange, and
	// LinkPreviewCacheTTLSeconds how long a successful preview is reused.
	// Everything else about the fetch — body ceiling, redirect limit, cache
	// size, allowed content types — is a constant, because a deployment that
	// could widen them could weaken the sandbox.
	LinkPreviewTimeoutSeconds  int
	LinkPreviewCacheTTLSeconds int

	uploadsEnabledInvalid      bool
	malwareScanRequiredInvalid bool
	maxUploadBytesInvalid      bool
	linkPreviewEnabledInvalid  bool
}

func Load() Config {
	uploadsEnabled, uploadsEnabledInvalid := configuredBool("FILE_UPLOADS_ENABLED", false)
	scanRequired, scanRequiredInvalid := configuredBool("FILE_MALWARE_SCAN_REQUIRED", true)
	maxUploadBytes, maxUploadBytesInvalid := configuredInt64("FILE_MAX_UPLOAD_BYTES", domain.MaxMaxUploadBytes)
	linkPreviewEnabled, linkPreviewEnabledInvalid := configuredBool("FILE_LINK_PREVIEW_ENABLED", false)

	return Config{
		ServiceName: serviceName,
		// No default. An absent APP_ENV must stay absent: defaulting it to
		// "development" would hand the malware-scan escape hatch to any
		// deployment that simply forgot to set the variable, which is the
		// opposite of failing closed. Every overlay and every .env example sets
		// it explicitly, so the empty case only ever means "not configured".
		Env:                        strings.TrimSpace(platformconfig.GetString("APP_ENV", "")),
		Port:                       platformconfig.GetInt("PORT", defaultPort),
		ReadHeaderTimeoutSeconds:   positiveInt("READ_HEADER_TIMEOUT_SECONDS", 5),
		ReadTimeoutSeconds:         positiveInt("READ_TIMEOUT_SECONDS", defaultReadTimeoutSeconds),
		WriteTimeoutSeconds:        positiveInt("WRITE_TIMEOUT_SECONDS", defaultWriteTimeoutSeconds),
		UploadsEnabled:             uploadsEnabled,
		DatabaseURL:                strings.TrimSpace(platformconfig.GetString("DATABASE_URL", "")),
		DBConnectTimeoutSeconds:    positiveInt("DB_CONNECT_TIMEOUT_SECONDS", 5),
		DBMaxConnections:           platformconfig.GetInt("FILE_DB_MAX_CONNECTIONS", defaultDBMaxConnections),
		UploadMaxConcurrent:        platformconfig.GetInt("FILE_UPLOAD_MAX_CONCURRENT", defaultUploadMaxConcurrent),
		UploadMaxConcurrentPerUser: platformconfig.GetInt("FILE_UPLOAD_MAX_CONCURRENT_PER_USER", defaultUploadMaxConcurrentPerUser),
		UploadRetryAfterSeconds:    positiveInt("FILE_UPLOAD_RETRY_AFTER_SECONDS", defaultUploadRetryAfterSeconds),
		AuthJWTHMACSecret:          platformconfig.GetString("AUTH_JWT_HMAC_SECRET", ""),
		AuthJWTIssuer:              strings.TrimSpace(platformconfig.GetString("AUTH_JWT_ISSUER", defaultJWTIssuer)),
		AuthJWTAudience:            strings.TrimSpace(platformconfig.GetString("AUTH_JWT_AUDIENCE", defaultJWTAudience)),
		MaxUploadBytes:             maxUploadBytes,
		EncryptionMasterKey:        platformconfig.GetString("FILE_ENCRYPTION_MASTER_KEY", ""),
		EncryptionMasterKeyID:      platformconfig.GetString("FILE_ENCRYPTION_MASTER_KEY_ID", ""),
		EncryptionPreviousKeys:     platformconfig.GetString("FILE_ENCRYPTION_PREVIOUS_KEYS", ""),
		MalwareScanRequired:        scanRequired,
		MalwareScannerAddress:      strings.TrimSpace(platformconfig.GetString("FILE_MALWARE_SCANNER_ADDRESS", "")),
		MalwareScanTimeoutSeconds:  positiveInt("FILE_MALWARE_SCAN_TIMEOUT_SECONDS", defaultMalwareScanTimeoutSeconds),
		ValkeyURL:                  strings.TrimSpace(platformconfig.GetString("VALKEY_URL", "")),
		WSInstanceID:               sanitizeInstanceID(platformconfig.GetString("WS_INSTANCE_ID", "")),
		SeaweedFSFilerURL:          strings.TrimSpace(platformconfig.GetString("SEAWEEDFS_FILER_URL", "")),
		SeaweedFSTimeoutSeconds:    positiveInt("SEAWEEDFS_TIMEOUT_SECONDS", defaultSeaweedFSTimeoutSeconds),
		LinkPreviewEnabled:         linkPreviewEnabled,
		LinkPreviewTimeoutSeconds: positiveInt(
			"FILE_LINK_PREVIEW_TIMEOUT_SECONDS", defaultLinkPreviewTimeoutSeconds,
		),
		LinkPreviewCacheTTLSeconds: positiveInt(
			"FILE_LINK_PREVIEW_CACHE_TTL_SECONDS", defaultLinkPreviewCacheTTLSeconds,
		),
		uploadsEnabledInvalid:      uploadsEnabledInvalid,
		malwareScanRequiredInvalid: scanRequiredInvalid,
		maxUploadBytesInvalid:      maxUploadBytesInvalid,
		linkPreviewEnabledInvalid:  linkPreviewEnabledInvalid,
	}
}

// Validate fails closed: while uploads are enabled every dependency the feature
// needs must be present and well formed. It is split into the three questions
// it actually asks — limits, scan policy, dependencies — so each stays readable
// on its own.
func (c Config) Validate() error {
	if c.uploadsEnabledInvalid {
		return errors.New("FILE_UPLOADS_ENABLED must be a valid boolean")
	}
	if c.malwareScanRequiredInvalid {
		return errors.New("FILE_MALWARE_SCAN_REQUIRED must be a valid boolean")
	}
	// Link previews are validated before the early return below, because they
	// are independent of uploads: a deployment may run either, both or neither.
	if err := c.validateLinkPreview(); err != nil {
		return err
	}
	if !c.UploadsEnabled {
		return nil
	}
	if err := c.validateUploadLimits(); err != nil {
		return err
	}
	if err := c.validateUploadConcurrency(); err != nil {
		return err
	}
	if err := c.validateScanPolicy(); err != nil {
		return err
	}
	return c.validateUploadDependencies()
}

// validateUploadLimits checks the deployment ceiling. An explicitly configured
// value that is not an acceptable limit is an error rather than something
// silently corrected.
//
// It reuses uploadpolicy.Valid rather than restating the bounds, so the ceiling
// and the administrative policy live in exactly the same value space — the same
// range and the same whole-MiB rule — and the two can never drift into a
// configuration where the operator's knob accepts something the policy cannot.
func (c Config) validateUploadLimits() error {
	if c.maxUploadBytesInvalid {
		return errors.New("FILE_MAX_UPLOAD_BYTES must be a valid integer")
	}
	if !uploadpolicy.Valid(c.MaxUploadBytes) {
		return fmt.Errorf(
			"FILE_MAX_UPLOAD_BYTES must be a whole number of MiB between %d and %d bytes",
			domain.MinMaxUploadBytes, domain.MaxMaxUploadBytes,
		)
	}
	return nil
}

// validateUploadConcurrency checks the cluster-wide admission limits and the
// pool they draw connections from.
//
// Zero never means "unlimited" here: an upload control that can be switched off
// by typing 0 is not a control, so a non-positive value is a start-up error.
// The pool check is the one that is easy to get wrong — every occupied upload
// slot is a reserved connection, so a pool sized at or below the slot count
// would let a fully booked service starve its own authorization queries and
// deadlock instead of answering 503.
func (c Config) validateUploadConcurrency() error {
	if c.UploadMaxConcurrent <= 0 || c.UploadMaxConcurrent > maxUploadMaxConcurrent {
		return fmt.Errorf(
			"FILE_UPLOAD_MAX_CONCURRENT must be between 1 and %d", maxUploadMaxConcurrent,
		)
	}
	if c.UploadMaxConcurrentPerUser <= 0 {
		return errors.New("FILE_UPLOAD_MAX_CONCURRENT_PER_USER must be greater than zero")
	}
	if c.UploadMaxConcurrentPerUser > c.UploadMaxConcurrent {
		return errors.New(
			"FILE_UPLOAD_MAX_CONCURRENT_PER_USER must not exceed FILE_UPLOAD_MAX_CONCURRENT",
		)
	}
	if required := c.UploadMaxConcurrent + dbConnectionHeadroom; c.DBMaxConnections < required {
		return fmt.Errorf(
			"FILE_DB_MAX_CONNECTIONS must be at least %d: every in-flight upload reserves "+
				"one connection and %d must remain for other queries",
			required, dbConnectionHeadroom,
		)
	}
	return nil
}

// validateUploadDependencies requires every external dependency the attachment
// routes need to be present and well formed before the service starts.
func (c Config) validateUploadDependencies() error {
	if c.DatabaseURL == "" {
		return errors.New("database URL is required when uploads are enabled")
	}
	if err := c.validateAuthDependencies(); err != nil {
		return err
	}
	// One check for the whole key ring: the active key, its id and any previous
	// key kept for reading. It builds the ring the service will use, so a
	// configuration that starts is a configuration that can wrap and unwrap.
	// No key material and no key id value reaches the message.
	if err := crypto.ValidateKeyring(
		c.EncryptionMasterKeyID, c.EncryptionMasterKey, c.EncryptionPreviousKeys,
	); err != nil {
		return fmt.Errorf(
			"FILE_ENCRYPTION_MASTER_KEY_ID / FILE_ENCRYPTION_MASTER_KEY / "+
				"FILE_ENCRYPTION_PREVIOUS_KEYS are invalid: %w", err,
		)
	}
	if !validFilerURL(c.SeaweedFSFilerURL) {
		return errors.New("SEAWEEDFS_FILER_URL must be a valid HTTP or HTTPS URL without credentials")
	}
	// The scanner endpoint is optional, but a configured one must be usable: a
	// typo would otherwise start the worker against an address it can never
	// reach, and every upload would sit in pending_scan while the logs filled
	// with retries. Absent is a supported deployment (no worker, nothing
	// approved); malformed is a mistake and is refused at start-up.
	if c.MalwareScannerAddress != "" && !validScannerAddress(c.MalwareScannerAddress) {
		return errors.New("FILE_MALWARE_SCANNER_ADDRESS must be host:port")
	}
	// The budget is the *effective* one: it bounds the scan job and derives the
	// claim lease as well as the client's socket deadline. A non-positive value
	// has already fallen back to the default by the time it gets here, exactly
	// like every other timeout in this service, so the only thing left to refuse
	// is a value large enough to overflow the duration it becomes.
	if c.MalwareScanTimeoutSeconds > maxMalwareScanTimeoutSeconds {
		return fmt.Errorf(
			"FILE_MALWARE_SCAN_TIMEOUT_SECONDS must not exceed %d",
			maxMalwareScanTimeoutSeconds,
		)
	}
	return nil
}

// validateAuthDependencies checks the access-token settings every
// authenticated route needs. It is shared rather than restated because uploads
// and link previews are enabled independently and both are authenticated: a
// deployment running only one of them must still be refused if it cannot
// validate a token.
func (c Config) validateAuthDependencies() error {
	if len([]byte(c.AuthJWTHMACSecret)) < 32 {
		return errors.New("JWT HMAC secret must be at least 32 bytes")
	}
	if c.AuthJWTIssuer == "" {
		return errors.New("JWT issuer is required")
	}
	if c.AuthJWTAudience == "" {
		return errors.New("JWT audience is required")
	}
	return nil
}

// validateLinkPreview checks the RF-10 settings.
//
// The ceilings are operational, not policy. A timeout is multiplied by
// time.Second, so an absurd value overflows the duration it becomes and yields
// a deadline that has already passed; and a budget measured in minutes would
// hold a request — and a socket — open for a third-party site that has already
// failed. The TTL ceiling exists so a typo cannot make a cached preview
// effectively permanent. A non-positive value has already fallen back to the
// default in Load, exactly like every other timeout in this service.
func (c Config) validateLinkPreview() error {
	if c.linkPreviewEnabledInvalid {
		return errors.New("FILE_LINK_PREVIEW_ENABLED must be a valid boolean")
	}
	if !c.LinkPreviewEnabled {
		return nil
	}
	if c.LinkPreviewTimeoutSeconds > maxLinkPreviewTimeoutSeconds {
		return fmt.Errorf(
			"FILE_LINK_PREVIEW_TIMEOUT_SECONDS must not exceed %d", maxLinkPreviewTimeoutSeconds,
		)
	}
	if c.LinkPreviewCacheTTLSeconds > maxLinkPreviewCacheTTLSeconds {
		return fmt.Errorf(
			"FILE_LINK_PREVIEW_CACHE_TTL_SECONDS must not exceed %d", maxLinkPreviewCacheTTLSeconds,
		)
	}
	return c.validateAuthDependencies()
}

// validScannerAddress accepts a plain "host:port" and nothing else.
//
// No scheme, no path, no credentials: the value is a dial target, so anything
// that looks like a URL is a configuration mistake rather than something to
// interpret. The port must be numeric and in range, which is also what stops a
// value like "clamav:notaport" from failing later, per scan, instead of once.
func validScannerAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number > 0 && number <= 65535
}

// sanitizeInstanceID mirrors chat-service's rule for WS_INSTANCE_ID: the value
// ends up in a bus envelope the consumer validates, so a value it would reject
// is dropped here and replaced by a generated one rather than silently causing
// every event to be discarded.
func sanitizeInstanceID(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" || len(trimmed) > wsInstanceIDMaxLen || !wsInstanceIDRe.MatchString(trimmed) {
		return ""
	}
	return trimmed
}

const wsInstanceIDMaxLen = 64

var wsInstanceIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateScanPolicy refuses to disable the malware-scan gate unless the
// deployment has explicitly declared itself a development environment.
//
// SECURITY.md requires downloads to stay blocked until the scan approves them,
// so FILE_MALWARE_SCAN_REQUIRED=false is a local-development affordance for a
// cluster that has no scanner, never a deployment option. APP_ENV is the
// discriminator, and it has to be *stated*: every Kubernetes overlay sets it
// (infra/k8s/base/configmap.yaml and infra/k8s/overlays/*/configmap-patch.yaml
// use development, staging and nchat-dev) and so does every .env example.
//
// The exception is granted by a positive match against a closed allowlist,
// never by the absence of a production marker. An APP_ENV that is missing,
// empty, whitespace, misspelled or simply unrecognised is treated as a deployed
// environment and the escape hatch is refused.
func (c Config) validateScanPolicy() error {
	if c.MalwareScanRequired || isExplicitDevelopmentEnvironment(c.Env) {
		return nil
	}
	// The value is echoed because APP_ENV is an environment label, never a
	// secret, and seeing the typo is what makes the failure actionable.
	return fmt.Errorf(
		"FILE_MALWARE_SCAN_REQUIRED may only be false when APP_ENV is explicitly one of "+
			"%s, got APP_ENV=%q",
		strings.Join(developmentEnvironments, ", "), c.Env,
	)
}

// developmentEnvironments is the closed set of APP_ENV values that count as an
// explicitly declared development environment. It is the only way the
// malware-scan gate can be switched off, so it is deliberately short and is not
// extended without a matching change to SECURITY.md.
var developmentEnvironments = []string{"development", "dev", "local", "test", "nchat-dev"}

// isExplicitDevelopmentEnvironment answers the one question the scan policy
// asks. It is the single place that decides what "development" means, so the
// rule cannot drift between call sites.
//
// Comparison is case-insensitive and ignores surrounding whitespace; nothing
// else is inferred. In particular an empty or unknown value is false, not
// "probably local".
func isExplicitDevelopmentEnvironment(env string) bool {
	normalized := strings.ToLower(strings.TrimSpace(env))
	if normalized == "" {
		return false
	}
	for _, allowed := range developmentEnvironments {
		if normalized == allowed {
			return true
		}
	}
	return false
}

// validFilerURL restricts the storage endpoint to a plain http/https origin
// supplied by configuration. Credentials, query strings and fragments are
// refused so the endpoint cannot smuggle anything into a storage request.
func validFilerURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "" || parsed.Path == "/"
}

func configuredBool(key string, fallback bool) (value, invalid bool) {
	raw, configured := os.LookupEnv(key)
	if !configured {
		return fallback, false
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, true
	}
	return parsed, false
}

func configuredInt64(key string, fallback int64) (value int64, invalid bool) {
	raw, configured := os.LookupEnv(key)
	if !configured {
		return fallback, false
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, true
	}
	return parsed, false
}

func positiveInt(key string, fallback int) int {
	value := platformconfig.GetInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}
