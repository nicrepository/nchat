package domain_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// The registry is a compile-time literal, so every invariant it must hold is a
// property of the source and belongs in the build rather than in a boot check.
func TestConfigCatalog_HoldsItsInvariants(t *testing.T) {
	if err := domain.ValidateConfigCatalog(); err != nil {
		t.Fatalf("configuration catalog is invalid: %v", err)
	}
}

// The one invariant that keeps this API from ever writing outside the database:
// no Kubernetes object, no file, no outbound request, and therefore no SSRF
// surface in a validator.
func TestConfigCatalog_OnlyDatabaseBackedSettingsAreEditable(t *testing.T) {
	editable := 0
	for _, definition := range domain.ConfigCatalog() {
		if !definition.Editable {
			continue
		}
		editable++
		if definition.Source != domain.ConfigSourceDatabase {
			t.Fatalf("%s is editable from source %q; the Admin API may only write the database",
				definition.Key, definition.Source)
		}
		if definition.Sensitive {
			t.Fatalf("%s is editable and sensitive; there is no secret backend to write", definition.Key)
		}
	}
	if editable == 0 {
		t.Fatal("expected at least one editable setting; a registry with none makes the whole surface read-only")
	}
}

// A credential must be reportable as configured and never as a value. The
// registry is what guarantees that: a sensitive definition is never editable,
// so it can never appear in a change set, a diff or the history.
func TestConfigCatalog_CredentialsAreReadOnlyAndPointAtTheRunbook(t *testing.T) {
	credentials := 0
	for _, definition := range domain.ConfigCatalog() {
		if !definition.Sensitive {
			continue
		}
		credentials++
		if definition.Editable || definition.Rollbackable() {
			t.Fatalf("%s must not be writable", definition.Key)
		}
		if !strings.Contains(definition.ReadOnlyReason, "sealed-secrets-rotation") {
			t.Fatalf("%s must name the rotation runbook, got %q", definition.Key, definition.ReadOnlyReason)
		}
	}
	if credentials == 0 {
		t.Fatal("expected the registry to inventory the platform credentials")
	}
}

// Class B is declared and unused. If a definition ever claims it, the secret
// write path has to be designed first, and this is the test that forces that
// conversation instead of letting one slip in behind a convenient flag.
func TestConfigCatalog_HasNoRuntimeSecretClass(t *testing.T) {
	for _, definition := range domain.ConfigCatalog() {
		if definition.Class == domain.ConfigClassRuntimeSecret {
			t.Fatalf("%s claims class B; the platform has no secret backend the Admin API can write", definition.Key)
		}
	}
}

func TestLookupConfig_RefusesAKeyThePlatformDoesNotDeclare(t *testing.T) {
	for _, key := range []domain.ConfigKey{
		"", "auth.password.min_length ", "AUTH.PASSWORD.MIN_LENGTH",
		"auth.password.min_lengthx", "../../etc/passwd", "min_password_length",
	} {
		if _, ok := domain.LookupConfig(key); ok {
			t.Fatalf("expected %q to be unknown", key)
		}
	}
	if _, ok := domain.LookupConfig(domain.ConfigKeyPasswordMinLength); !ok {
		t.Fatal("expected the declared key to resolve")
	}
}

// The column is the only part of a statement that is substituted rather than
// bound, so it must never be anything but a plain identifier from this literal.
func TestConfigCatalog_ColumnsAreSubstitutableIdentifiers(t *testing.T) {
	for _, definition := range domain.EditableConfigDefinitions(domain.ConfigDocumentAuthPolicy) {
		for _, forbidden := range []string{" ", ";", "'", "\"", "-", "(", ")", "*"} {
			if strings.Contains(definition.Column, forbidden) {
				t.Fatalf("%s has column %q containing %q", definition.Key, definition.Column, forbidden)
			}
		}
	}
}

func TestParseConfigValue_RefusesCoercion(t *testing.T) {
	minLength, _ := domain.LookupConfig(domain.ConfigKeyPasswordMinLength)
	expiration, _ := domain.LookupConfig(domain.ConfigKeyPasswordExpirationDays)
	requireSymbol, _ := domain.LookupConfig(domain.ConfigKeyPasswordRequireSymbol)

	refused := []struct {
		name       string
		definition domain.ConfigDefinition
		raw        string
	}{
		{"decimal", minLength, "12.5"},
		{"exponent", minLength, "1e2"},
		{"quoted number", minLength, `"12"`},
		{"boolean for an integer", minLength, "true"},
		{"object", minLength, `{"value":12}`},
		{"array", minLength, "[12]"},
		{"absent", minLength, ""},
		{"null where null is not allowed", minLength, "null"},
		{"quoted boolean", requireSymbol, `"true"`},
		{"number for a boolean", requireSymbol, "1"},
		{"overflow", expiration, "99999999999999999999"},
	}
	for _, testCase := range refused {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := domain.ParseConfigValue(testCase.definition, json.RawMessage(testCase.raw)); err == nil {
				t.Fatalf("expected %q to be refused", testCase.raw)
			}
		})
	}

	value, err := domain.ParseConfigValue(expiration, json.RawMessage("null"))
	if err != nil {
		t.Fatalf("null must be accepted where the definition is nullable: %v", err)
	}
	if !value.Null || value.Type != domain.ConfigTypeInt {
		t.Fatalf("expected a typed null, got %+v", value)
	}
}

// Null is not zero. "Passwords do not expire" and "passwords expire in zero
// days" are different policies and only one of them is a policy.
func TestConfigValue_NullIsNotZero(t *testing.T) {
	null := domain.NullValue(domain.ConfigTypeInt)
	zero := domain.IntValue(0)

	if null.Equal(zero) || zero.Equal(null) {
		t.Fatal("a typed null must not equal zero")
	}
	encoded, err := json.Marshal(null)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != "null" {
		t.Fatalf("expected null on the wire, got %s", encoded)
	}
	if null.AuditString() != "null" {
		t.Fatalf("expected the trail to record null, got %q", null.AuditString())
	}
}

func TestConfigValue_DifferentTypesAreNeverEqual(t *testing.T) {
	if domain.IntValue(1).Equal(domain.BoolValue(true)) {
		t.Fatal("an integer must not equal a boolean")
	}
	if domain.TextValue("1").Equal(domain.IntValue(1)) {
		t.Fatal("text must not equal a number")
	}
}

func TestConfigDefinition_ValidateEnforcesTheAdministrativeRange(t *testing.T) {
	definition, _ := domain.LookupConfig(domain.ConfigKeyLoginFailedLimit)

	for _, value := range []int64{definition.Min - 1, definition.Max + 1, 0, -1} {
		if err := definition.Validate(domain.IntValue(value)); err == nil {
			t.Fatalf("expected %d to be refused", value)
		}
	}
	for _, value := range []int64{definition.Min, definition.Max} {
		if err := definition.Validate(domain.IntValue(value)); err != nil {
			t.Fatalf("expected %d to be accepted: %v", value, err)
		}
	}
	if err := definition.Validate(domain.BoolValue(true)); err == nil {
		t.Fatal("expected a type mismatch to be refused")
	}
}

// Danger is a property of the resulting value, so the same answer must come
// back whether the value was reached by an edit or by a rollback.
func TestConfigDefinition_DangerousValueRequiresSuperuser(t *testing.T) {
	minLength, _ := domain.LookupConfig(domain.ConfigKeyPasswordMinLength)
	requireNumber, _ := domain.LookupConfig(domain.ConfigKeyPasswordRequireNumber)
	devices, _ := domain.LookupConfig(domain.ConfigKeyDeviceMaxPerUser)

	if !minLength.DangerousValue(domain.IntValue(8)) {
		t.Fatal("shortening the minimum password length below the platform default is dangerous")
	}
	if minLength.DangerousValue(domain.IntValue(16)) {
		t.Fatal("strengthening the policy is not dangerous")
	}
	if minLength.RequiredCapability(domain.IntValue(8)) != domain.CapabilitySuperuser {
		t.Fatal("a dangerous value must demand admin.superuser")
	}
	if minLength.RequiredCapability(domain.IntValue(16)) != domain.CapabilityConfigManage {
		t.Fatal("an ordinary value must demand admin.config.manage")
	}
	if !requireNumber.DangerousValue(domain.BoolValue(false)) {
		t.Fatal("disabling a complexity requirement is dangerous")
	}
	if requireNumber.DangerousValue(domain.BoolValue(true)) {
		t.Fatal("enabling a complexity requirement is not dangerous")
	}
	if devices.DangerousValue(domain.IntValue(devices.Max)) {
		t.Fatal("a setting with no danger predicate is never dangerous")
	}
}

func TestDiffConfig_DropsUnchangedFieldsAndKeepsRegistryOrder(t *testing.T) {
	current := map[domain.ConfigKey]domain.ConfigValue{
		domain.ConfigKeyPasswordMinLength:     domain.IntValue(12),
		domain.ConfigKeyPasswordRequireNumber: domain.BoolValue(true),
		domain.ConfigKeyLoginFailedLimit:      domain.IntValue(5),
	}
	requested := map[domain.ConfigKey]domain.ConfigValue{
		domain.ConfigKeyLoginFailedLimit:      domain.IntValue(6),
		domain.ConfigKeyPasswordRequireNumber: domain.BoolValue(true),
		domain.ConfigKeyPasswordMinLength:     domain.IntValue(14),
	}

	changes := domain.DiffConfig(current, requested)

	if len(changes) != 2 {
		t.Fatalf("expected the unchanged field to be dropped, got %+v", changes)
	}
	// Registry order, not request order: two administrators looking at the same
	// change must read the same diff.
	if changes[0].Key != domain.ConfigKeyPasswordMinLength || changes[1].Key != domain.ConfigKeyLoginFailedLimit {
		t.Fatalf("expected registry order, got %s then %s", changes[0].Key, changes[1].Key)
	}
	if changes[0].From.Int != 12 || changes[0].To.Int != 14 {
		t.Fatalf("expected 12 -> 14, got %d -> %d", changes[0].From.Int, changes[0].To.Int)
	}
}

func TestDiffConfig_ResubmittedFormProducesNothing(t *testing.T) {
	current := map[domain.ConfigKey]domain.ConfigValue{
		domain.ConfigKeyPasswordMinLength: domain.IntValue(12),
	}
	if changes := domain.DiffConfig(current, current); len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
}

func TestConfigDocumentOf_RefusesUnknownAndReadOnlyKeys(t *testing.T) {
	if _, err := domain.ConfigDocumentOf([]domain.ConfigKey{"auth.password.min_lengthx"}); !errors.Is(err, domain.ErrConfigUnknownKey) {
		t.Fatalf("expected an unknown key to be refused as unknown, got %v", err)
	}
	if _, err := domain.ConfigDocumentOf([]domain.ConfigKey{"secret.smtp_password"}); !errors.Is(err, domain.ErrConfigNotEditable) {
		t.Fatalf("expected a credential to be refused as not editable, got %v", err)
	}
	if _, err := domain.ConfigDocumentOf([]domain.ConfigKey{"oidc.enabled"}); !errors.Is(err, domain.ErrConfigNotEditable) {
		t.Fatalf("expected a deployment setting to be refused as not editable, got %v", err)
	}
	if _, err := domain.ConfigDocumentOf(nil); err == nil {
		t.Fatal("expected an empty change set to be refused")
	}
	document, err := domain.ConfigDocumentOf([]domain.ConfigKey{
		domain.ConfigKeyPasswordMinLength, domain.ConfigKeyLoginFailedLimit,
	})
	if err != nil || document != domain.ConfigDocumentAuthPolicy {
		t.Fatalf("expected the auth policy document, got %q (%v)", document, err)
	}
}

func TestReverseConfigChanges_SwapsTheTransition(t *testing.T) {
	version := domain.ConfigVersion{
		Document: domain.ConfigDocumentAuthPolicy,
		Revision: 4,
		Changes: []domain.ConfigChange{
			{Key: domain.ConfigKeyPasswordMinLength, From: domain.IntValue(12), To: domain.IntValue(20)},
		},
	}

	reversed, err := domain.ReverseConfigChanges(version)
	if err != nil {
		t.Fatalf("expected a reversible version: %v", err)
	}
	if reversed[0].From.Int != 20 || reversed[0].To.Int != 12 {
		t.Fatalf("expected 20 -> 12, got %d -> %d", reversed[0].From.Int, reversed[0].To.Int)
	}
	if !domain.ConfigVersionRollbackable(version) {
		t.Fatal("expected the version to report itself rollbackable")
	}
}

// The registry may tighten over time. Restoring a value it would now refuse
// would make the history a way around validation, so it is refused instead.
func TestReverseConfigChanges_RefusesAValueTodaysRegistryWouldNotAccept(t *testing.T) {
	definition, _ := domain.LookupConfig(domain.ConfigKeyPasswordMinLength)
	version := domain.ConfigVersion{
		Document: domain.ConfigDocumentAuthPolicy,
		Changes: []domain.ConfigChange{
			{Key: domain.ConfigKeyPasswordMinLength, From: domain.IntValue(definition.Min - 1), To: domain.IntValue(12)},
		},
	}

	if _, err := domain.ReverseConfigChanges(version); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if domain.ConfigVersionRollbackable(version) {
		t.Fatal("an unrestorable version must not be offered for rollback")
	}
}

func TestReverseConfigChanges_RefusesAKeyThatNoLongerExists(t *testing.T) {
	version := domain.ConfigVersion{
		Document: domain.ConfigDocumentAuthPolicy,
		Changes:  []domain.ConfigChange{{Key: "auth.removed.setting", From: domain.IntValue(1), To: domain.IntValue(2)}},
	}

	if _, err := domain.ReverseConfigChanges(version); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
}

func TestReverseConfigChanges_RefusesAnEmptyVersion(t *testing.T) {
	version := domain.ConfigVersion{Document: domain.ConfigDocumentAuthPolicy, AppliedAt: time.Now()}
	if _, err := domain.ReverseConfigChanges(version); err == nil {
		t.Fatal("expected a version with no change to be unrollbackable")
	}
}

// The history column CHECK constrains stored values to number, boolean and
// null. This is the Go half of that rule: a string cannot be decoded back out,
// so a credential written by some future mistake could not be read either.
func TestDecodeStoredConfigValue_ReadsOnlyScalarsTheHistoryCanHold(t *testing.T) {
	cases := map[string]domain.ConfigValue{
		"12":    domain.IntValue(12),
		"true":  domain.BoolValue(true),
		"false": domain.BoolValue(false),
		"null":  domain.NullValue(domain.ConfigTypeInt),
	}
	for raw, expected := range cases {
		value, err := domain.DecodeStoredConfigValue([]byte(raw))
		if err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		if !value.Equal(expected) {
			t.Fatalf("decode %q: expected %+v, got %+v", raw, expected, value)
		}
	}
	for _, raw := range []string{`"hunter2"`, `{"a":1}`, `[1]`, "", "12.5"} {
		if _, err := domain.DecodeStoredConfigValue([]byte(raw)); err == nil {
			t.Fatalf("expected %q to be unreadable from the history", raw)
		}
	}
}

func TestRetypeStoredConfigValue_OnlyAdjustsNull(t *testing.T) {
	retyped := domain.RetypeStoredConfigValue(domain.ConfigKeyPasswordExpirationDays, domain.NullValue(domain.ConfigTypeString))
	if retyped.Type != domain.ConfigTypeInt || !retyped.Null {
		t.Fatalf("expected the declared type on a null, got %+v", retyped)
	}
	mismatch := domain.RetypeStoredConfigValue(domain.ConfigKeyPasswordMinLength, domain.BoolValue(true))
	if mismatch.Type != domain.ConfigTypeBool {
		t.Fatal("a real type mismatch must be preserved so validation refuses it")
	}
}

func TestNormalizeConfigReason_TrimsAndBounds(t *testing.T) {
	reason, err := domain.NormalizeConfigReason("  incidente 42  ")
	if err != nil || reason != "incidente 42" {
		t.Fatalf("expected a trimmed reason, got %q (%v)", reason, err)
	}
	if _, err := domain.NormalizeConfigReason(strings.Repeat("a", 501)); err == nil {
		t.Fatal("expected an oversized reason to be refused")
	}
}

// The catalog guards, exercised against definitions that break them. A checker
// that has only ever seen a valid catalog is a checker whose failure modes
// nobody has read.
func TestValidateConfigDefinitions_RefusesEveryInvariantViolation(t *testing.T) {
	valid := func() domain.ConfigDefinition {
		definition, _ := domain.LookupConfig(domain.ConfigKeyDeviceMaxPerUser)
		return definition
	}
	readOnly := func() domain.ConfigDefinition {
		definition, _ := domain.LookupConfig("secret.smtp_password")
		return definition
	}
	mutate := func(apply func(*domain.ConfigDefinition)) []domain.ConfigDefinition {
		definition := valid()
		apply(&definition)
		return []domain.ConfigDefinition{definition}
	}

	cases := map[string][]domain.ConfigDefinition{
		"duplicate key":            {valid(), valid()},
		"malformed key":            mutate(func(d *domain.ConfigDefinition) { d.Key = "Auth.Bad Key" }),
		"no label":                 mutate(func(d *domain.ConfigDefinition) { d.Label = "" }),
		"no description":           mutate(func(d *domain.ConfigDefinition) { d.Description = "" }),
		"no owner":                 mutate(func(d *domain.ConfigDefinition) { d.OwnerService = "" }),
		"no category":              mutate(func(d *domain.ConfigDefinition) { d.Category = "" }),
		"class B":                  mutate(func(d *domain.ConfigDefinition) { d.Class = domain.ConfigClassRuntimeSecret }),
		"editable and sensitive":   mutate(func(d *domain.ConfigDefinition) { d.Sensitive = true }),
		"editable outside the db":  mutate(func(d *domain.ConfigDefinition) { d.Source = domain.ConfigSourceGitOps }),
		"editable but not runtime": mutate(func(d *domain.ConfigDefinition) { d.Apply = domain.ConfigApplyRollout }),
		"unknown document":         mutate(func(d *domain.ConfigDefinition) { d.Document = "auth.unknown" }),
		"malformed column":         mutate(func(d *domain.ConfigDefinition) { d.Column = "max; DROP TABLE" }),
		"unknown capability":       mutate(func(d *domain.ConfigDefinition) { d.ManageCapability = "admin.invented" }),
		"empty range":              mutate(func(d *domain.ConfigDefinition) { d.Min, d.Max = 10, 10 }),
		"invalid default":          mutate(func(d *domain.ConfigDefinition) { d.Default = domain.IntValue(0) }),
		"silent danger": mutate(func(d *domain.ConfigDefinition) {
			d.Dangerous = func(domain.ConfigValue) bool { return true }
			d.DangerNote = ""
		}),
	}
	for name, definitions := range cases {
		t.Run(name, func(t *testing.T) {
			if err := domain.ValidateConfigDefinitions(definitions); err == nil {
				t.Fatal("expected the catalog guard to refuse")
			}
		})
	}

	readOnlyCases := map[string]func(*domain.ConfigDefinition){
		"read-only with no reason":    func(d *domain.ConfigDefinition) { d.ReadOnlyReason = "" },
		"read-only with a capability": func(d *domain.ConfigDefinition) { d.ManageCapability = domain.CapabilityConfigManage },
		"read-only naming a column":   func(d *domain.ConfigDefinition) { d.Column = "min_password_length" },
		"read-only naming a document": func(d *domain.ConfigDefinition) { d.Document = domain.ConfigDocumentAuthPolicy },
		"read-only claiming rollback": func(d *domain.ConfigDefinition) { d.Sensitive = false; d.Editable = true },
	}
	for name, apply := range readOnlyCases {
		t.Run(name, func(t *testing.T) {
			definition := readOnly()
			apply(&definition)
			if err := domain.ValidateConfigDefinitions([]domain.ConfigDefinition{definition}); err == nil {
				t.Fatal("expected the catalog guard to refuse")
			}
		})
	}
}

func TestConfigValue_RendersEveryDeclaredType(t *testing.T) {
	cases := []struct {
		value domain.ConfigValue
		json  string
		audit string
	}{
		{domain.IntValue(12), "12", "12"},
		{domain.BoolValue(true), "true", "true"},
		{domain.BoolValue(false), "false", "false"},
		{domain.TextValue("staging"), `"staging"`, "staging"},
		{domain.NullValue(domain.ConfigTypeBool), "null", "null"},
	}
	for _, testCase := range cases {
		encoded, err := json.Marshal(testCase.value)
		if err != nil {
			t.Fatalf("marshal %+v: %v", testCase.value, err)
		}
		if string(encoded) != testCase.json {
			t.Fatalf("expected %s, got %s", testCase.json, encoded)
		}
		if got := testCase.value.AuditString(); got != testCase.audit {
			t.Fatalf("expected %q in the trail, got %q", testCase.audit, got)
		}
	}
}

// A value with no declared type is not a value. It must fail loudly rather than
// serialize as something plausible.
func TestConfigValue_UntypedValueIsRefused(t *testing.T) {
	var untyped domain.ConfigValue

	if _, err := json.Marshal(untyped); err == nil {
		t.Fatal("expected an untyped value to refuse to serialize")
	}
	if untyped.AuditString() != "" {
		t.Fatalf("expected nothing in the trail, got %q", untyped.AuditString())
	}
	if untyped.Equal(domain.ConfigValue{}) {
		t.Fatal("two untyped values are not a comparison anyone should trust")
	}
	definition, _ := domain.LookupConfig(domain.ConfigKeyDeviceMaxPerUser)
	definition.Type = "made-up"
	if _, err := domain.ParseConfigValue(definition, json.RawMessage("1")); err == nil {
		t.Fatal("expected a definition with no readable type to be refused")
	}
}

func TestParseConfigValue_ReadsTextForAStringDefinition(t *testing.T) {
	definition, _ := domain.LookupConfig("platform.environment")

	value, err := domain.ParseConfigValue(definition, json.RawMessage(`"staging"`))
	if err != nil {
		t.Fatalf("ParseConfigValue: %v", err)
	}
	if value.Type != domain.ConfigTypeString || value.Text != "staging" {
		t.Fatalf("expected the text, got %+v", value)
	}
	if _, err := domain.ParseConfigValue(definition, json.RawMessage("12")); err == nil {
		t.Fatal("expected a number to be refused for a text definition")
	}
	if err := definition.Validate(domain.TextValue("staging")); err != nil {
		t.Fatalf("expected a string value to validate: %v", err)
	}
}

func TestAuditConfigResource_NamesTheDocument(t *testing.T) {
	resource := domain.AuditConfigResource(domain.ConfigDocumentAuthPolicy)

	if resource != "admin.config:auth.policy" {
		t.Fatalf("unexpected resource key: %q", resource)
	}
	if !strings.HasPrefix(resource, domain.AuditResourceConfigPrefix) {
		t.Fatal("the resource must stay inside the configuration namespace")
	}
}

// Every danger predicate is exercised at its own boundary, so a definition
// whose predicate never fires — or fires for everything — is caught here rather
// than by an operator discovering that a weakening needed no extra authority.
func TestConfigCatalog_EveryDangerPredicateFiresAtItsBoundary(t *testing.T) {
	boundaries := map[domain.ConfigKey]struct{ dangerous, safe domain.ConfigValue }{
		domain.ConfigKeyPasswordMinLength:        {domain.IntValue(11), domain.IntValue(12)},
		domain.ConfigKeyPasswordRequireUppercase: {domain.BoolValue(false), domain.BoolValue(true)},
		domain.ConfigKeyPasswordRequireLowercase: {domain.BoolValue(false), domain.BoolValue(true)},
		domain.ConfigKeyPasswordRequireNumber:    {domain.BoolValue(false), domain.BoolValue(true)},
		domain.ConfigKeyPasswordRequireSymbol:    {domain.BoolValue(false), domain.BoolValue(true)},
		domain.ConfigKeyLoginFailedLimit:         {domain.IntValue(11), domain.IntValue(10)},
		domain.ConfigKeyLoginLockoutMinutes:      {domain.IntValue(4), domain.IntValue(5)},
		domain.ConfigKeySessionIdleMinutes:       {domain.IntValue(241), domain.IntValue(240)},
		domain.ConfigKeyPasswordResetTTLMinutes:  {domain.IntValue(241), domain.IntValue(240)},
		domain.ConfigKeyInviteTTLHours:           {domain.IntValue(169), domain.IntValue(168)},
	}
	for key, boundary := range boundaries {
		t.Run(string(key), func(t *testing.T) {
			assertDangerBoundary(t, key, boundary.dangerous, boundary.safe)
		})
	}
}

// assertDangerBoundary checks both sides of one predicate: the value that
// weakens the platform demands admin.superuser, and the value one step away
// from it does not.
func assertDangerBoundary(t *testing.T, key domain.ConfigKey, dangerous, safe domain.ConfigValue) {
	t.Helper()
	definition, ok := domain.LookupConfig(key)
	if !ok {
		t.Fatalf("%s is not declared", key)
	}
	if !definition.DangerousValue(dangerous) {
		t.Fatalf("%s must treat %s as dangerous", key, dangerous.AuditString())
	}
	if definition.RequiredCapability(dangerous) != domain.CapabilitySuperuser {
		t.Fatalf("%s must demand admin.superuser at %s", key, dangerous.AuditString())
	}
	if definition.DangerousValue(safe) {
		t.Fatalf("%s must not treat %s as dangerous", key, safe.AuditString())
	}
	if definition.RequiredCapability(safe) != domain.CapabilityConfigManage {
		t.Fatalf("%s must need only the manage capability at %s", key, safe.AuditString())
	}
}

// The settings with no predicate: no value of them is dangerous, and this is
// what notices if one silently grows one without a boundary spec above.
func TestConfigCatalog_SettingsWithoutADangerPredicateAreNeverDangerous(t *testing.T) {
	for _, key := range []domain.ConfigKey{
		domain.ConfigKeyPasswordExpirationDays,
		domain.ConfigKeyLoginFailedWindow,
		domain.ConfigKeyDeviceMaxPerUser,
	} {
		definition, _ := domain.LookupConfig(key)
		if definition.Dangerous != nil {
			t.Fatalf("%s declares a danger predicate no boundary spec covers", key)
		}
		if definition.DangerousValue(domain.IntValue(definition.Max)) {
			t.Fatalf("%s must never be dangerous", key)
		}
	}
}
