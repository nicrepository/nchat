package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Configuration & Secrets Management (issue #580).
//
// The one rule this file exists to enforce: the platform decides which
// configuration exists, what type it has, who may change it and whether it can
// be changed at all. A client names a key; it never defines one. A key the
// registry does not hold is refused, so the failure mode of a typo, a stale
// console build or a crafted request is the same — nothing happens.

// ConfigKey is the stable identifier of one configuration setting.
//
// It is dotted and lowercase, and it is *not* the environment variable or the
// database column. Those are implementation details that differ per owner
// service and may change; the key is the contract the console and the audit
// trail are written against.
type ConfigKey string

// ConfigDocument is one unit of concurrency control.
//
// A document is the thing a revision counts, so every editable setting belongs
// to exactly one, and a single request may only change settings inside one
// document. There is one document today because there is one configuration
// store the Admin API can write.
type ConfigDocument string

const (
	// ConfigDocumentAuthPolicy is auth.auth_policy_settings: the single-row
	// authentication policy auth-service reads on every login, session touch,
	// password change, invite and device registration.
	ConfigDocumentAuthPolicy ConfigDocument = "auth.policy"
)

// ConfigClass is the impact classification every setting must carry.
//
// It is declared per definition rather than derived, because the class is a
// statement about how the platform behaves and not an accident of which fields
// happen to be set. ValidateConfigCatalog checks that the declaration agrees
// with the source, apply mode and editability, so the two cannot drift.
type ConfigClass string

const (
	// ConfigClassRuntime (A) takes effect without a restart. The enforcing
	// service reads the value on the path that enforces it.
	ConfigClassRuntime ConfigClass = "A"
	// ConfigClassRuntimeSecret (B) would be a credential changeable at
	// runtime. No setting is in this class today: NChat has no secret backend
	// the Admin API can write — credentials arrive as environment variables
	// from Sealed Secrets — so a definition claiming class B would be claiming
	// a mechanism that does not exist. The constant is declared, and
	// ValidateConfigCatalog asserts nothing uses it, so adding the first one
	// forces the secret write path to be designed first.
	ConfigClassRuntimeSecret ConfigClass = "B"
	// ConfigClassRollout (C) is read at boot by the owning workload. Changing
	// it is a Git change plus a rollout, never a click here.
	ConfigClassRollout ConfigClass = "C"
	// ConfigClassInfrastructure (D) is controlled outside the application
	// entirely — cluster topology, storage endpoints, credentials.
	ConfigClassInfrastructure ConfigClass = "D"
)

// ConfigSource is where the authoritative value lives.
type ConfigSource string

const (
	// ConfigSourceDatabase is the only source the Admin API writes.
	ConfigSourceDatabase ConfigSource = "database"
	// ConfigSourceGitOps is the Kustomize ConfigMap. Git is the source of
	// truth; the Admin API reports what this pod observes and never writes.
	ConfigSourceGitOps ConfigSource = "gitops"
	// ConfigSourceSealedSecret is a Sealed Secret rotated through
	// docs/runbooks/sealed-secrets-rotation.md. The Admin API reports whether
	// a value is present and never what it is.
	ConfigSourceSealedSecret ConfigSource = "sealed_secret"
)

// ConfigApply is how a change reaches the running platform.
type ConfigApply string

const (
	// ConfigApplyRuntime means persisting is applying: the enforcing service
	// reads the stored value on the next request.
	ConfigApplyRuntime ConfigApply = "runtime"
	// ConfigApplyRollout means the owning workload must be replaced for a new
	// value to be read.
	ConfigApplyRollout ConfigApply = "rollout"
	// ConfigApplyExternal means the change is performed outside the platform
	// and the Admin API only observes the result.
	ConfigApplyExternal ConfigApply = "external"
)

// ConfigCategory groups settings for presentation and for the audit trail.
type ConfigCategory string

const (
	ConfigCategoryAuthentication ConfigCategory = "authentication"
	ConfigCategoryPlatform       ConfigCategory = "platform"
	ConfigCategoryIntegrations   ConfigCategory = "integrations"
	ConfigCategoryInfrastructure ConfigCategory = "infrastructure"
	ConfigCategoryCredentials    ConfigCategory = "credentials"
)

// ConfigValueType is the wire and storage type of a setting's value.
type ConfigValueType string

const (
	ConfigTypeInt    ConfigValueType = "int"
	ConfigTypeBool   ConfigValueType = "bool"
	ConfigTypeString ConfigValueType = "string"
)

// ConfigValue is one typed configuration value.
//
// A tagged struct rather than `any`: the type is carried with the value, so a
// comparison, a diff or a JSON encoding cannot silently treat a boolean as a
// number or a missing value as a zero. Null is explicit and is not the zero
// int — "no password expiration" and "expires in zero days" are different
// policies and the second one is not allowed to exist.
type ConfigValue struct {
	Type ConfigValueType
	Int  int64
	Bool bool
	Text string
	Null bool
}

func IntValue(value int64) ConfigValue { return ConfigValue{Type: ConfigTypeInt, Int: value} }
func BoolValue(value bool) ConfigValue { return ConfigValue{Type: ConfigTypeBool, Bool: value} }
func TextValue(value string) ConfigValue {
	return ConfigValue{Type: ConfigTypeString, Text: value}
}

// NullValue is a typed absence: the type is still known, the value is not set.
func NullValue(valueType ConfigValueType) ConfigValue {
	return ConfigValue{Type: valueType, Null: true}
}

// Equal compares two values of the same type. Values of different types are
// never equal, which is what keeps a diff from reporting "changed" for a
// representation difference or "unchanged" across a type change.
func (v ConfigValue) Equal(other ConfigValue) bool {
	if v.Type != other.Type || v.Null != other.Null {
		return false
	}
	if v.Null {
		return true
	}
	switch v.Type {
	case ConfigTypeInt:
		return v.Int == other.Int
	case ConfigTypeBool:
		return v.Bool == other.Bool
	case ConfigTypeString:
		return v.Text == other.Text
	default:
		return false
	}
}

// MarshalJSON renders the value as the JSON scalar it is.
func (v ConfigValue) MarshalJSON() ([]byte, error) {
	if v.Null {
		return []byte("null"), nil
	}
	switch v.Type {
	case ConfigTypeInt:
		return json.Marshal(v.Int)
	case ConfigTypeBool:
		return json.Marshal(v.Bool)
	case ConfigTypeString:
		return json.Marshal(v.Text)
	default:
		return nil, fmt.Errorf("config value: unknown type %q", v.Type)
	}
}

// AuditString renders the value for the audit trail's metadata, which is a
// map of strings.
//
// Only ever called for non-sensitive values: the trail records what a setting
// became, and no sensitive setting is writable, so nothing this produces can
// carry a credential.
func (v ConfigValue) AuditString() string {
	if v.Null {
		return "null"
	}
	switch v.Type {
	case ConfigTypeInt:
		return strconv.FormatInt(v.Int, 10)
	case ConfigTypeBool:
		return strconv.FormatBool(v.Bool)
	case ConfigTypeString:
		return v.Text
	default:
		return ""
	}
}

// ConfigDefinition is everything the platform knows about one setting.
//
// It is the whole authorization and validation decision for that setting in
// one reviewable place: reading the catalog answers "who may change this, to
// what, and what happens when they do" without following a call chain.
type ConfigDefinition struct {
	Key         ConfigKey
	Label       string
	Description string
	Category    ConfigCategory
	// OwnerService is the service that reads and enforces the value. It is not
	// necessarily admin-service, and for a read-only setting it is usually not.
	OwnerService string

	Class  ConfigClass
	Source ConfigSource
	Apply  ConfigApply

	Type ConfigValueType
	// Unit names what the number counts. A limit whose unit the screen has to
	// guess is how "60" becomes seconds on one side and minutes on the other.
	Unit string
	// Min and Max are the range the Admin API accepts, inclusive, for an int.
	// They are deliberately narrower than the column CHECK: the CHECK is the
	// backstop that keeps a bug out of the database, this is the range an
	// administrator is offered.
	Min, Max int64
	// Nullable allows an explicit null, which for these settings always means
	// "the rule does not apply" rather than zero.
	Nullable bool
	Default  ConfigValue

	// Editable is the single answer to "can this be changed from the console".
	// False carries a ReadOnlyReason the console shows verbatim, so an operator
	// learns why rather than finding a disabled field.
	Editable       bool
	ReadOnlyReason string

	// Sensitive settings never leave this service as a value. The API reports
	// only whether one is configured.
	Sensitive bool

	// Document and Column are the backing store, and exist only for editable
	// settings. Column is a compile-time constant substituted into a statement;
	// no request ever names a column.
	Document ConfigDocument
	Column   string

	// ManageCapability is the capability an ordinary change requires. Reading
	// the catalog requires CapabilityConfigRead for everything.
	ManageCapability Capability
	// Dangerous reports whether a *resulting* value weakens the platform. It
	// takes the new value rather than the transition so it describes a state:
	// the same answer whether the value was raised, lowered or restored by a
	// rollback. Nil means no value of this setting is dangerous.
	Dangerous func(ConfigValue) bool
	// DangerNote is what the console shows when Dangerous says yes.
	DangerNote string

	// EnvVar is the variable admin-service reads to observe a setting it does
	// not own. Empty when the value has no environment representation, or when
	// no pod of this service receives it — in which case the API reports the
	// setting as not observable rather than reporting a default as if it were
	// the deployed value.
	EnvVar string
}

// DangerousValue reports whether applying this value needs the elevated
// capability. A definition without a predicate is never dangerous.
func (d ConfigDefinition) DangerousValue(value ConfigValue) bool {
	if d.Dangerous == nil {
		return false
	}
	return d.Dangerous(value)
}

// RequiredCapability is the capability a change to this value demands.
//
// A dangerous value requires CapabilitySuperuser and not merely
// CapabilityConfigManage: weakening authentication is a change to who can
// reach the platform, and the capability model already reserves that scope for
// the grant that confers all authority.
func (d ConfigDefinition) RequiredCapability(value ConfigValue) Capability {
	if d.DangerousValue(value) {
		return CapabilitySuperuser
	}
	return d.ManageCapability
}

// Rollbackable reports whether a recorded change to this setting can be undone
// by writing the previous value back.
//
// It is true for exactly the settings that are editable and store a scalar in
// the database: the previous value is recorded, restoring it is the same
// operation as any other change, and nothing external moved in the meantime.
// It is false for everything else rather than "not implemented yet" — a
// rollback the platform cannot perform must not be offered.
func (d ConfigDefinition) Rollbackable() bool {
	return d.Editable && !d.Sensitive && d.Source == ConfigSourceDatabase
}

// Validate checks one value against this definition.
//
// The definitive validation. The console repeats some of it for immediate
// feedback, and that copy is a convenience: a request that skips the console
// entirely meets exactly these rules.
func (d ConfigDefinition) Validate(value ConfigValue) error {
	if value.Type != d.Type {
		return fmt.Errorf("%w: %s expects %s", ErrInvalidInput, d.Key, d.Type)
	}
	if value.Null {
		if !d.Nullable {
			return fmt.Errorf("%w: %s cannot be null", ErrInvalidInput, d.Key)
		}
		return nil
	}
	if d.Type == ConfigTypeInt && (value.Int < d.Min || value.Int > d.Max) {
		return fmt.Errorf("%w: %s must be between %d and %d", ErrInvalidInput, d.Key, d.Min, d.Max)
	}
	return nil
}

// ParseConfigValue reads one JSON scalar as this definition's type.
//
// Coercion is refused everywhere it would be convenient. A string is not a
// number, a float is not an integer, and an absent field is not a value: each
// of those is a caller that computed the wrong thing, and accepting it would
// store a policy nobody chose. The integer rule is the same one the policy
// endpoints already apply — a base-10 literal parsed into int64, so exponent
// notation, decimals and values too large to represent are refused instead of
// wrapping.
func ParseConfigValue(definition ConfigDefinition, raw json.RawMessage) (ConfigValue, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ConfigValue{}, fmt.Errorf("%w: %s has no value", ErrInvalidInput, definition.Key)
	}
	if string(trimmed) != "null" {
		return parseConfigScalar(definition, trimmed)
	}
	// An explicit null is a rule of the definition rather than of the type: it
	// means "this rule does not apply", and a setting that has no such state
	// must not be able to reach one.
	if !definition.Nullable {
		return ConfigValue{}, fmt.Errorf("%w: %s cannot be null", ErrInvalidInput, definition.Key)
	}
	return NullValue(definition.Type), nil
}

// parseConfigScalar reads a non-null JSON scalar as the declared type.
func parseConfigScalar(definition ConfigDefinition, trimmed []byte) (ConfigValue, error) {
	switch definition.Type {
	case ConfigTypeInt:
		parsed, err := strconv.ParseInt(string(trimmed), 10, 64)
		if err != nil {
			return ConfigValue{}, fmt.Errorf("%w: %s must be an integer", ErrInvalidInput, definition.Key)
		}
		return IntValue(parsed), nil
	case ConfigTypeBool:
		switch string(trimmed) {
		case "true":
			return BoolValue(true), nil
		case "false":
			return BoolValue(false), nil
		}
		return ConfigValue{}, fmt.Errorf("%w: %s must be a boolean", ErrInvalidInput, definition.Key)
	case ConfigTypeString:
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return ConfigValue{}, fmt.Errorf("%w: %s must be text", ErrInvalidInput, definition.Key)
		}
		return TextValue(text), nil
	default:
		return ConfigValue{}, fmt.Errorf("%w: %s has no readable type", ErrInvalidInput, definition.Key)
	}
}

// DecodeStoredConfigValue reads a value back out of the change history.
//
// It infers the type from the JSON scalar instead of asking the registry,
// because history outlives definitions: a setting that was removed still has
// rows describing what it once was, and an operator reading the trail is
// entitled to see them. The stored type is constrained by a column CHECK to
// number, boolean or null, so this cannot widen into "whatever was written" —
// a string is not storable and is refused here as well.
func DecodeStoredConfigValue(raw []byte) (ConfigValue, error) {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case len(trimmed) == 0:
		return ConfigValue{}, fmt.Errorf("%w: empty stored value", ErrInvalidInput)
	case string(trimmed) == "null":
		return NullValue(ConfigTypeInt), nil
	case string(trimmed) == "true":
		return BoolValue(true), nil
	case string(trimmed) == "false":
		return BoolValue(false), nil
	}
	parsed, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil {
		return ConfigValue{}, fmt.Errorf("%w: unreadable stored value", ErrInvalidInput)
	}
	return IntValue(parsed), nil
}

// RetypeStoredConfigValue restores the declared type of a decoded null.
//
// JSON null carries no type, so a decoded null has to be told which setting it
// belonged to before it can be validated for a rollback. Only null is
// adjusted: a real type mismatch between the history and today's registry is
// left in place so validation refuses it, rather than being papered over into
// a value that would then be written back.
func RetypeStoredConfigValue(key ConfigKey, value ConfigValue) ConfigValue {
	definition, ok := LookupConfig(key)
	if !ok || !value.Null {
		return value
	}
	return NullValue(definition.Type)
}

// ConfigChange is one field's transition, as a diff entry and as a history row.
type ConfigChange struct {
	Key  ConfigKey
	From ConfigValue
	To   ConfigValue
}

// ConfigPrecondition is a value the document must still hold for a change to be
// allowed to apply.
//
// It is what makes a rollback safe. The revision protects against an edit made
// since the form was loaded; a precondition protects against something else
// entirely: that the version being reverted is still the one in force. Reverting
// "10 -> 20" after somebody has since moved the value to 30 would silently
// discard their change, and no revision check catches that, because the console
// loaded *after* they wrote.
//
// The store asserts these in the same statement that performs the write, so
// there is no window between checking and writing.
type ConfigPrecondition struct {
	Key   ConfigKey
	Value ConfigValue
}

// ConfigApplyOutcome is what a committed write produced.
//
// State is built from the same statement that performed the write rather than
// read back afterwards. That is deliberate: a second read could fail — or could
// observe a state somebody else has already moved on from — and neither may be
// allowed to contradict a commit that has already happened.
type ConfigApplyOutcome struct {
	Version ConfigVersion
	State   ConfigDocumentState
}

// ConfigApplyInput is one attempt to write a validated change set.
//
// Every field is server-derived: the changes have been checked against the
// registry, the actor comes from the authenticated principal, and the
// correlation id is the request id this service minted. Nothing here was
// chosen by the caller except the values themselves and the stated reason.
type ConfigApplyInput struct {
	ExpectedRevision int
	Changes          []ConfigChange
	ActorUserID      string
	CorrelationID    string
	Reason           string
	// RevertsRevision is the revision this change restores, or zero. It is
	// recorded rather than inferred so a rollback stays identifiable in the
	// history after the values have moved on again.
	RevertsRevision int
	// Preconditions are the values the document must still hold. Empty means
	// the change asserts only the revision.
	Preconditions []ConfigPrecondition
	// Authorization is re-proved inside the writing transaction, under locks
	// the revocation paths contend for.
	//
	// Not a formality: the capability the middleware checked was true when the
	// request arrived, and this write commits later. A role revoked in between
	// must make it fail, and only a check that shares the transaction can
	// promise that. Zero value authorizes nothing.
	Authorization MutationAuthorization
}

// ConfigDocumentState is a document as it is stored right now.
type ConfigDocumentState struct {
	Document ConfigDocument
	Revision int
	Values   map[ConfigKey]ConfigValue
}

// ConfigVersion is one applied change, as the history recorded it.
type ConfigVersion struct {
	ID              int64
	Document        ConfigDocument
	Revision        int
	AppliedAt       time.Time
	ActorUserID     string
	ActorEmail      string
	CorrelationID   string
	Reason          string
	RevertsRevision int
	Changes         []ConfigChange
}

// ErrConfigUnknownKey is the refusal a key outside the registry earns.
//
// It wraps ErrInvalidInput so the HTTP layer answers 400 without a second
// mapping, and it is its own error so a test can assert that the registry, and
// not a type check further along, is what refused.
var ErrConfigUnknownKey = fmt.Errorf("%w: unknown configuration key", ErrInvalidInput)

// ErrConfigNotEditable is the refusal a read-only or sensitive key earns.
var ErrConfigNotEditable = fmt.Errorf("%w: configuration is not editable", ErrInvalidInput)

// DiffConfig computes the changes a requested set of values would make.
//
// Values equal to what is stored are dropped rather than recorded: a form that
// submits every field it rendered must not produce a version claiming twelve
// changes when the operator made one. A request whose fields are all unchanged
// produces an empty diff, which callers treat as nothing to apply.
//
// The order follows the registry, not the request, so two administrators
// looking at the same change see the same diff.
func DiffConfig(current map[ConfigKey]ConfigValue, requested map[ConfigKey]ConfigValue) []ConfigChange {
	changes := make([]ConfigChange, 0, len(requested))
	for _, definition := range ConfigCatalog() {
		desired, ok := requested[definition.Key]
		if !ok {
			continue
		}
		existing, known := current[definition.Key]
		if known && existing.Equal(desired) {
			continue
		}
		changes = append(changes, ConfigChange{Key: definition.Key, From: existing, To: desired})
	}
	return changes
}

// ReverseConfigChanges turns a recorded version into the change set that
// restores it.
//
// Every field must still be rollbackable and the old value must still be
// acceptable under today's registry: bounds tighten over time, and restoring a
// value the platform would now refuse to accept would use the history as a way
// around validation.
func ReverseConfigChanges(version ConfigVersion) ([]ConfigChange, error) {
	if len(version.Changes) == 0 {
		return nil, fmt.Errorf("%w: version records no change", ErrInvalidInput)
	}
	reversed := make([]ConfigChange, 0, len(version.Changes))
	for _, change := range version.Changes {
		definition, ok := LookupConfig(change.Key)
		if !ok {
			return nil, fmt.Errorf("%w: %s no longer exists", ErrConflict, change.Key)
		}
		if !definition.Rollbackable() {
			return nil, fmt.Errorf("%w: %s is no longer reversible", ErrConflict, change.Key)
		}
		if err := definition.Validate(change.From); err != nil {
			return nil, fmt.Errorf("%w: %s cannot be restored", ErrConflict, change.Key)
		}
		reversed = append(reversed, ConfigChange{Key: change.Key, From: change.To, To: change.From})
	}
	return reversed, nil
}

// ConfigRollbackPreconditions are the values a version's changes must still
// hold for that version to be reversible.
//
// One per field the version touched, whether or not restoring it would change
// anything today: a field somebody has already put back by hand is still
// evidence that this version is no longer the one in force.
func ConfigRollbackPreconditions(version ConfigVersion) []ConfigPrecondition {
	preconditions := make([]ConfigPrecondition, 0, len(version.Changes))
	for _, change := range version.Changes {
		preconditions = append(preconditions, ConfigPrecondition{Key: change.Key, Value: change.To})
	}
	return preconditions
}

// ConfigPreconditionBaseline is the state a precondition-carrying change is
// described against.
//
// For a rollback that baseline is the version being undone, not the values the
// document happens to hold. The two are the same thing while the version is
// still in force; when it is not, describing the change against the current
// state would present "30 -> 10" as the operation on offer, when what was asked
// for is "undo the version that set 20". The diff must name the version's own
// transition, and `superseded` is what says it can no longer be performed.
func ConfigPreconditionBaseline(preconditions []ConfigPrecondition) map[ConfigKey]ConfigValue {
	baseline := make(map[ConfigKey]ConfigValue, len(preconditions))
	for _, precondition := range preconditions {
		baseline[precondition.Key] = precondition.Value
	}
	return baseline
}

// ConfigPreconditionsHold reports whether every precondition matches the state
// given.
//
// The authoritative check is the one the store performs inside the write. This
// one exists so a change set that turns out to be empty is still refused rather
// than answered as "nothing to do" — the write is never reached in that case,
// so nothing else would notice that the version had been superseded.
func ConfigPreconditionsHold(preconditions []ConfigPrecondition, values map[ConfigKey]ConfigValue) bool {
	for _, precondition := range preconditions {
		if !values[precondition.Key].Equal(precondition.Value) {
			return false
		}
	}
	return true
}

// ConfigDocumentOf resolves the document a set of changed keys belongs to, and
// refuses a set that spans more than one.
//
// One document per request is what makes the revision check meaningful: a
// request touching two documents would need two revisions and could apply one
// half while losing a race on the other.
func ConfigDocumentOf(keys []ConfigKey) (ConfigDocument, error) {
	var document ConfigDocument
	for _, key := range keys {
		definition, ok := LookupConfig(key)
		if !ok {
			return "", ErrConfigUnknownKey
		}
		if !definition.Editable {
			return "", ErrConfigNotEditable
		}
		if document == "" {
			document = definition.Document
			continue
		}
		if document != definition.Document {
			return "", fmt.Errorf("%w: a change may not span configuration documents", ErrInvalidInput)
		}
	}
	if document == "" {
		return "", fmt.Errorf("%w: no configuration was named", ErrInvalidInput)
	}
	return document, nil
}

// ValidConfigDocument reports whether the platform defines this document.
func ValidConfigDocument(document ConfigDocument) bool {
	return document == ConfigDocumentAuthPolicy
}

// maxConfigReasonLength mirrors the column CHECK. The reason is operator prose
// that lands in the history and in the audit trail; it is bounded here so an
// oversized one is a 400 rather than a database error.
const maxConfigReasonLength = 500

// NormalizeConfigReason trims and bounds the operator's stated reason.
func NormalizeConfigReason(reason string) (string, error) {
	trimmed := strings.TrimSpace(reason)
	if len(trimmed) > maxConfigReasonLength {
		return "", fmt.Errorf("%w: reason is too long", ErrInvalidInput)
	}
	return trimmed, nil
}

// AuditConfigResource is the canonical resource key of a configuration change.
func AuditConfigResource(document ConfigDocument) string {
	return AuditResourceConfigPrefix + string(document)
}

const (
	// AuditResourceConfigPrefix namespaces configuration events in the trail,
	// the same way AuditResourceUserPrefix namespaces user events.
	AuditResourceConfigPrefix = "admin.config:"

	AuditActionConfigUpdate   = "admin.config.update"
	AuditActionConfigRollback = "admin.config.rollback"
)

// errConfigCatalog is returned by ValidateConfigCatalog, which runs in a test
// rather than at boot: the catalog is a compile-time literal, so a violation is
// a source defect and must fail the build, not the deployment.
var errConfigCatalog = errors.New("configuration catalog")

// ConfigVersionRollbackable reports whether a recorded version can be undone.
//
// It is the same question ReverseConfigChanges answers, asked without wanting
// the result: the console needs to know whether to offer the action, and the
// answer must come from the one function that decides it rather than from a
// second rule that could disagree.
func ConfigVersionRollbackable(version ConfigVersion) bool {
	_, err := ReverseConfigChanges(version)
	return err == nil
}
