package domain

import (
	"fmt"
	"regexp"
	"sync"
)

// The configuration registry (issue #580).
//
// This is the whole set of configuration the Admin API knows about. It is a
// literal and not a query: the platform decides what exists, so a key that is
// not written below does not exist as far as this service is concerned, and
// there is no request that can add one.
//
// The narrative version of this table, including the settings admin-service
// cannot observe at all, is docs/security/config-inventory.md.
//
// Three groups, and the difference between them is what the platform can
// honestly do:
//
//   - class A, stored in auth.auth_policy_settings. auth-service reads these on
//     the request that enforces them, so persisting is applying. These are the
//     only settings this API writes.
//   - class C, read at boot from the Kustomize ConfigMap. admin-service mounts
//     the same ConfigMap with envFrom, so it reports what its own pod observes
//     and says plainly that changing it is a rollout.
//   - class D, infrastructure and credentials. Reported as observed or as
//     "configured", never written, never returned as a value when sensitive.
//
// There is no class B: NChat has no secret store the Admin API can write.
// Credentials arrive as environment variables from Sealed Secrets and are
// rotated through docs/runbooks/sealed-secrets-rotation.md, which is a Git
// change and a rollout. Offering a "replace secret" field here would either
// write a value nothing reads or push a Kubernetes credential towards a
// process that must not hold one, so the console offers neither.

// Keys of the settings stored in auth.auth_policy_settings.
const (
	ConfigKeyPasswordMinLength        ConfigKey = "auth.password.min_length"
	ConfigKeyPasswordRequireUppercase ConfigKey = "auth.password.require_uppercase"
	ConfigKeyPasswordRequireLowercase ConfigKey = "auth.password.require_lowercase"
	ConfigKeyPasswordRequireNumber    ConfigKey = "auth.password.require_number"
	ConfigKeyPasswordRequireSymbol    ConfigKey = "auth.password.require_symbol"
	ConfigKeyPasswordExpirationDays   ConfigKey = "auth.password.expiration_days"
	ConfigKeyLoginFailedLimit         ConfigKey = "auth.login.failed_attempt_limit"
	ConfigKeyLoginFailedWindow        ConfigKey = "auth.login.failed_attempt_window_minutes"
	ConfigKeyLoginLockoutMinutes      ConfigKey = "auth.login.lockout_minutes"
	ConfigKeySessionIdleMinutes       ConfigKey = "auth.session.idle_timeout_minutes"
	ConfigKeyDeviceMaxPerUser         ConfigKey = "auth.device.max_per_user"
	ConfigKeyPasswordResetTTLMinutes  ConfigKey = "auth.password_reset.ttl_minutes"
	ConfigKeyInviteTTLHours           ConfigKey = "auth.invite.ttl_hours"
)

// authPolicyDefinitions are the class A settings.
//
// Every Min/Max below is the range the Admin API offers, and it is narrower
// than the column CHECK on purpose: the CHECK exists so a bug cannot store
// nonsense, this exists so an administrator is not offered a value that would
// be technically storable and operationally indefensible. Nothing is clamped or
// rounded — a value outside the range is refused.
func authPolicyDefinitions() []ConfigDefinition {
	policy := func(definition ConfigDefinition) ConfigDefinition {
		definition.Category = ConfigCategoryAuthentication
		definition.OwnerService = "auth-service"
		definition.Class = ConfigClassRuntime
		definition.Source = ConfigSourceDatabase
		definition.Apply = ConfigApplyRuntime
		definition.Editable = true
		definition.Document = ConfigDocumentAuthPolicy
		definition.ManageCapability = CapabilityConfigManage
		return definition
	}
	weakened := func(value ConfigValue) bool { return value.Type == ConfigTypeBool && !value.Bool }

	return []ConfigDefinition{
		policy(ConfigDefinition{
			Key:         ConfigKeyPasswordMinLength,
			Label:       "Tamanho mínimo da senha",
			Description: "Número mínimo de caracteres exigido ao definir ou alterar uma senha local.",
			Column:      "min_password_length",
			Type:        ConfigTypeInt,
			Unit:        "caracteres",
			Min:         8,
			Max:         128,
			Default:     IntValue(12),
			Dangerous:   func(v ConfigValue) bool { return v.Int < 12 },
			DangerNote:  "Abaixo de 12 caracteres a política fica mais fraca que o padrão da plataforma.",
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyPasswordRequireUppercase,
			Label:       "Exigir letra maiúscula",
			Description: "Rejeita senhas sem ao menos uma letra maiúscula.",
			Column:      "require_uppercase",
			Type:        ConfigTypeBool,
			Default:     BoolValue(true),
			Dangerous:   weakened,
			DangerNote:  "Desativar um requisito de complexidade enfraquece a autenticação local.",
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyPasswordRequireLowercase,
			Label:       "Exigir letra minúscula",
			Description: "Rejeita senhas sem ao menos uma letra minúscula.",
			Column:      "require_lowercase",
			Type:        ConfigTypeBool,
			Default:     BoolValue(true),
			Dangerous:   weakened,
			DangerNote:  "Desativar um requisito de complexidade enfraquece a autenticação local.",
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyPasswordRequireNumber,
			Label:       "Exigir número",
			Description: "Rejeita senhas sem ao menos um dígito.",
			Column:      "require_number",
			Type:        ConfigTypeBool,
			Default:     BoolValue(true),
			Dangerous:   weakened,
			DangerNote:  "Desativar um requisito de complexidade enfraquece a autenticação local.",
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyPasswordRequireSymbol,
			Label:       "Exigir símbolo",
			Description: "Rejeita senhas sem ao menos um caractere não alfanumérico.",
			Column:      "require_symbol",
			Type:        ConfigTypeBool,
			Default:     BoolValue(true),
			Dangerous:   weakened,
			DangerNote:  "Desativar um requisito de complexidade enfraquece a autenticação local.",
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyPasswordExpirationDays,
			Label:       "Expiração de senha",
			Description: "Dias até uma senha local expirar. Vazio significa que senhas não expiram — que é o padrão da plataforma, e não o valor zero.",
			Column:      "password_expiration_days",
			Type:        ConfigTypeInt,
			Unit:        "dias",
			Min:         1,
			Max:         3650,
			Nullable:    true,
			Default:     NullValue(ConfigTypeInt),
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyLoginFailedLimit,
			Label:       "Tentativas de login até o bloqueio",
			Description: "Falhas consecutivas dentro da janela antes do bloqueio temporário da conta.",
			Column:      "failed_login_limit",
			Type:        ConfigTypeInt,
			Unit:        "tentativas",
			Min:         3,
			Max:         20,
			Default:     IntValue(5),
			Dangerous:   func(v ConfigValue) bool { return v.Int > 10 },
			DangerNote:  "Acima de 10 tentativas o bloqueio deixa de conter ataque de força bruta com senhas comuns.",
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyLoginFailedWindow,
			Label:       "Janela de contagem de falhas",
			Description: "Período em que as falhas de login são contadas para o bloqueio.",
			Column:      "failed_login_window_minutes",
			Type:        ConfigTypeInt,
			Unit:        "minutos",
			Min:         1,
			Max:         1440,
			Default:     IntValue(15),
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyLoginLockoutMinutes,
			Label:       "Duração do bloqueio",
			Description: "Tempo que a conta permanece bloqueada após atingir o limite de falhas.",
			Column:      "failed_login_lockout_minutes",
			Type:        ConfigTypeInt,
			Unit:        "minutos",
			Min:         1,
			Max:         1440,
			Default:     IntValue(15),
			Dangerous:   func(v ConfigValue) bool { return v.Int < 5 },
			DangerNote:  "Bloqueio menor que 5 minutos torna a tentativa automatizada barata novamente.",
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeySessionIdleMinutes,
			Label:       "Inatividade da sessão de chat",
			Description: "Tempo sem uso após o qual a sessão do chat expira. Não afeta a sessão administrativa, que é mais curta e definida por variável de ambiente.",
			Column:      "session_idle_timeout_minutes",
			Type:        ConfigTypeInt,
			Unit:        "minutos",
			Min:         5,
			Max:         1440,
			Default:     IntValue(60),
			Dangerous:   func(v ConfigValue) bool { return v.Int > 240 },
			DangerNote:  "Sessões ociosas por mais de 4 horas ampliam a janela de uso de um dispositivo abandonado.",
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyDeviceMaxPerUser,
			Label:       "Dispositivos simultâneos por usuário",
			Description: "Quantidade máxima de sessões de dispositivo ativas por usuário.",
			Column:      "max_devices_per_user",
			Type:        ConfigTypeInt,
			Unit:        "dispositivos",
			Min:         1,
			Max:         50,
			Default:     IntValue(5),
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyPasswordResetTTLMinutes,
			Label:       "Tempo de vida do link de redefinição de senha",
			Description: "Tempo de vida do link enviado por e-mail para redefinir senha.",
			Column:      "password_reset_token_ttl_minutes",
			Type:        ConfigTypeInt,
			Unit:        "minutos",
			Min:         5,
			Max:         1440,
			Default:     IntValue(60),
			Dangerous:   func(v ConfigValue) bool { return v.Int > 240 },
			DangerNote:  "Um link de redefinição válido por mais de 4 horas fica exposto por muito tempo na caixa de entrada.",
		}),
		policy(ConfigDefinition{
			Key:         ConfigKeyInviteTTLHours,
			Label:       "Tempo de vida do convite",
			Description: "Tempo de vida do convite enviado a um novo usuário.",
			Column:      "invite_token_ttl_hours",
			Type:        ConfigTypeInt,
			Unit:        "horas",
			Min:         1,
			Max:         720,
			Default:     IntValue(72),
			Dangerous:   func(v ConfigValue) bool { return v.Int > 168 },
			DangerNote:  "Convites válidos por mais de uma semana permanecem utilizáveis muito depois de o contexto mudar.",
		}),
	}
}

// deploymentDefinitions are the class C settings: read at boot from the shared
// Kustomize ConfigMap.
//
// admin-service mounts nchat-config with envFrom exactly like every other
// service, so the value reported here is the one this pod booted with. That is
// the honest claim and the console states it: Git is the source of truth, and a
// change to it reaches the platform when the workloads are rolled out.
func deploymentDefinitions() []ConfigDefinition {
	deployment := func(definition ConfigDefinition) ConfigDefinition {
		definition.Class = ConfigClassRollout
		definition.Source = ConfigSourceGitOps
		definition.Apply = ConfigApplyRollout
		definition.Editable = false
		definition.ReadOnlyReason = "Definido no ConfigMap versionado em Git; alterar exige commit e rollout."
		return definition
	}
	text := func(key ConfigKey, env, label, description, owner string, category ConfigCategory) ConfigDefinition {
		return deployment(ConfigDefinition{
			Key: key, EnvVar: env, Label: label, Description: description,
			OwnerService: owner, Category: category, Type: ConfigTypeString,
		})
	}
	flag := func(key ConfigKey, env, label, description, owner string, category ConfigCategory) ConfigDefinition {
		return deployment(ConfigDefinition{
			Key: key, EnvVar: env, Label: label, Description: description,
			OwnerService: owner, Category: category, Type: ConfigTypeString,
		})
	}

	return []ConfigDefinition{
		text("platform.environment", "APP_ENV", "Ambiente",
			"Rótulo do ambiente. É também o que decide o aviso exibido no topo do console.",
			"todos os serviços", ConfigCategoryPlatform),
		text("platform.log_level", "LOG_LEVEL", "Nível de log",
			"Verbosidade do log estruturado dos serviços.",
			"todos os serviços", ConfigCategoryPlatform),
		text("auth.jwt.issuer", "AUTH_JWT_ISSUER", "Emissor do JWT",
			"Valor de `iss` exigido em todo access token. Precisa ser idêntico em todos os serviços.",
			"auth-service", ConfigCategoryAuthentication),
		text("auth.jwt.audience", "AUTH_JWT_AUDIENCE", "Audiência do JWT",
			"Valor de `aud` exigido em todo access token.",
			"auth-service", ConfigCategoryAuthentication),
		text("auth.trusted_proxy_cidrs", "AUTH_TRUSTED_PROXY_CIDRS", "Proxies confiáveis",
			"CIDRs cujo cabeçalho de encaminhamento é aceito para derivar o endereço do cliente.",
			"auth-service", ConfigCategoryAuthentication),
		flag("oidc.enabled", "OIDC_ENABLED", "Single sign-on habilitado",
			"Com `false`, todos os endpoints OIDC respondem 404 e só resta o login local.",
			"auth-service", ConfigCategoryIntegrations),
		text("oidc.provider_name", "OIDC_PROVIDER_NAME", "Nome do provedor OIDC",
			"Identificador do provedor usado nas rotas de single sign-on.",
			"auth-service", ConfigCategoryIntegrations),
		text("oidc.scopes", "OIDC_SCOPES", "Escopos OIDC",
			"Escopos solicitados na autorização.",
			"auth-service", ConfigCategoryIntegrations),
		flag("oidc.auto_provision_enabled", "OIDC_AUTO_PROVISION_ENABLED", "Provisionamento automático",
			"Cria a conta local no primeiro login federado quando habilitado.",
			"auth-service", ConfigCategoryIntegrations),
		text("oidc.allowed_email_domains", "OIDC_ALLOWED_EMAIL_DOMAINS", "Domínios de e-mail aceitos",
			"Restringe o provisionamento automático aos domínios listados. Vazio não restringe.",
			"auth-service", ConfigCategoryIntegrations),
		text("oidc.admin_acr_values", "OIDC_ADMIN_ACR_VALUES", "ACR exigido no console",
			"Contexto de autenticação exigido no single sign-on do console. Vazio não exige nenhum.",
			"auth-service", ConfigCategoryAuthentication),
		flag("calls.livekit.enabled", "LIVEKIT_ENABLED", "Chamadas habilitadas",
			"Com `false`, a emissão de token do LiveKit é recusada.",
			"media-service", ConfigCategoryIntegrations),
		text("calls.livekit.token_ttl_seconds", "LIVEKIT_TTL_SECONDS", "Tempo de vida do token de chamada",
			"Tempo de vida do token emitido para entrar em uma sala.",
			"media-service", ConfigCategoryIntegrations),
		flag("email.smtp.worker_enabled", "SMTP_WORKER_ENABLED", "Envio de e-mail habilitado",
			"Com `false`, a fila de e-mail acumula e nada é entregue.",
			"notification-service", ConfigCategoryIntegrations),
		text("admin.allowed_origins", "ADMIN_ALLOWED_ORIGINS", "Origens aceitas pelo console",
			"Allowlist de origem da Admin API. Vazio significa somente a própria origem.",
			"admin-service", ConfigCategoryPlatform),
		text("admin.session.idle_minutes", "ADMIN_SESSION_IDLE_MINUTES", "Inatividade da sessão administrativa",
			"Tempo sem uso após o qual a sessão do console expira. É deliberadamente menor que a do chat.",
			"admin-service", ConfigCategoryPlatform),
		text("admin.session.absolute_minutes", "ADMIN_SESSION_ABSOLUTE_MINUTES", "Duração máxima da sessão administrativa",
			"Prazo absoluto da sessão do console, independentemente de uso.",
			"admin-service", ConfigCategoryPlatform),
	}
}

// infrastructureDefinitions are the class D settings that are not credentials:
// endpoints and topology controlled outside the application.
func infrastructureDefinitions() []ConfigDefinition {
	infra := func(key ConfigKey, env, label, description, owner string) ConfigDefinition {
		return ConfigDefinition{
			Key: key, EnvVar: env, Label: label, Description: description,
			OwnerService: owner, Category: ConfigCategoryInfrastructure,
			Class: ConfigClassInfrastructure, Source: ConfigSourceGitOps,
			Apply: ConfigApplyExternal, Editable: false, Type: ConfigTypeString,
			ReadOnlyReason: "Infraestrutura controlada fora da aplicação; o console apenas observa.",
		}
	}
	return []ConfigDefinition{
		infra("infra.postgres.host", "POSTGRES_HOST", "PostgreSQL — host",
			"Endereço do banco no cluster.", "plataforma"),
		infra("infra.postgres.port", "POSTGRES_PORT", "PostgreSQL — porta",
			"Porta do banco no cluster.", "plataforma"),
		infra("infra.postgres.database", "POSTGRES_DB", "PostgreSQL — base",
			"Nome da base de dados.", "plataforma"),
		infra("infra.valkey.host", "VALKEY_HOST", "Valkey — host",
			"Endereço do Valkey usado por cache e broadcast de WebSocket.", "chat-service"),
		infra("infra.storage.filer_url", "SEAWEEDFS_FILER_URL", "SeaweedFS — filer",
			"Endpoint do filer usado pelo armazenamento de anexos.", "file-service"),
		infra("infra.storage.s3_endpoint", "SEAWEEDFS_S3_ENDPOINT", "SeaweedFS — S3",
			"Endpoint S3 do armazenamento de anexos.", "file-service"),
	}
}

// credentialDefinitions are the class D credentials.
//
// Two properties hold for every entry and are what make this list safe to
// serve at all:
//
//   - none is editable. There is no secret backend the Admin API can write, so
//     rotation is the Sealed Secrets runbook, and the console links to it
//     instead of offering a field;
//   - none carries a value anywhere. The service reads only whether the
//     variable is non-empty, and the value is never assigned to a field that
//     could be marshalled.
//
// EnvVar empty means no admin-service pod receives the variable — the Secret is
// scoped to another workload on purpose. Those are reported as not observable
// rather than as "not configured", because the two are different facts and
// guessing the wrong one would send an operator to fix something that is fine.
func credentialDefinitions() []ConfigDefinition {
	credential := func(key ConfigKey, env, label, description, owner string) ConfigDefinition {
		return ConfigDefinition{
			Key: key, EnvVar: env, Label: label, Description: description,
			OwnerService: owner, Category: ConfigCategoryCredentials,
			Class: ConfigClassInfrastructure, Source: ConfigSourceSealedSecret,
			Apply: ConfigApplyExternal, Editable: false, Sensitive: true,
			Type:           ConfigTypeString,
			ReadOnlyReason: "Credencial em Sealed Secret; a rotação segue docs/runbooks/sealed-secrets-rotation.md.",
		}
	}
	return []ConfigDefinition{
		credential("secret.database_url", "DATABASE_URL", "Conexão com o PostgreSQL",
			"String de conexão da aplicação, com credencial embutida.", "plataforma"),
		credential("secret.valkey_url", "VALKEY_URL", "Conexão com o Valkey",
			"String de conexão do Valkey, que pode embutir credencial.", "chat-service"),
		credential("secret.auth_jwt_hmac", "AUTH_JWT_HMAC_SECRET", "Chave de assinatura do JWT",
			"Segredo HMAC que assina e valida todo access token da plataforma.", "auth-service"),
		credential("secret.auth_email_outbox_key", "AUTH_EMAIL_OUTBOX_ENCRYPTION_KEY", "Chave da fila de e-mail",
			"Chave que protege os tokens de redefinição e convite na fila de envio.", "auth-service"),
		credential("secret.oidc_issuer_url", "OIDC_ISSUER_URL", "OIDC — issuer",
			"URL do provedor de identidade. Distribuída pelo mesmo Secret das credenciais OIDC.", "auth-service"),
		credential("secret.oidc_client_id", "OIDC_CLIENT_ID", "OIDC — client id",
			"Identificador do client registrado no provedor.", "auth-service"),
		credential("secret.oidc_client_secret", "OIDC_CLIENT_SECRET", "OIDC — client secret",
			"Credencial do client usada na troca de código por token.", "auth-service"),
		credential("secret.oidc_redirect_url", "OIDC_REDIRECT_URL", "OIDC — redirect do chat",
			"Callback registrado no provedor para o host do chat.", "auth-service"),
		credential("secret.oidc_admin_redirect_url", "OIDC_ADMIN_REDIRECT_URL", "OIDC — redirect do console",
			"Callback registrado no provedor para o host do console. Vazio desativa o single sign-on nele.", "auth-service"),
		credential("secret.smtp_password", "SMTP_PASSWORD", "SMTP — senha",
			"Credencial de envio do relay de e-mail.", "notification-service"),
		credential("secret.livekit_api_key", "LIVEKIT_API_KEY", "LiveKit — API key",
			"Identificador da credencial usada para emitir tokens de sala.", "media-service"),
		credential("secret.livekit_api_secret", "LIVEKIT_API_SECRET", "LiveKit — API secret",
			"Segredo usado para assinar tokens de sala.", "media-service"),
		// Scoped Secrets: deliberately not mounted by admin-service, so this
		// pod cannot observe them at all.
		credential("secret.file_encryption_master_key", "", "Chave mestra de anexos",
			"Chave que protege o conteúdo dos anexos. Montada somente pelo file-service; perdê-la é perda de dados irreversível.", "file-service"),
		credential("secret.link_safety_api_token", "", "Link Scan — credencial Cloudflare",
			"Credencial de verificação de links. Montada somente por chat-service e file-service.", "chat-service, file-service"),
	}
}

// catalog is assembled once. The order is the order the console renders and
// the order a diff is reported in, so it is stable rather than map iteration.
var (
	catalogOnce  sync.Once
	catalogItems []ConfigDefinition
	catalogIndex map[ConfigKey]ConfigDefinition
)

func buildCatalog() {
	catalogItems = make([]ConfigDefinition, 0, 48)
	catalogItems = append(catalogItems, authPolicyDefinitions()...)
	catalogItems = append(catalogItems, deploymentDefinitions()...)
	catalogItems = append(catalogItems, infrastructureDefinitions()...)
	catalogItems = append(catalogItems, credentialDefinitions()...)
	catalogIndex = make(map[ConfigKey]ConfigDefinition, len(catalogItems))
	for _, definition := range catalogItems {
		catalogIndex[definition.Key] = definition
	}
}

// ConfigCatalog returns every definition the platform declares, in a stable
// order.
func ConfigCatalog() []ConfigDefinition {
	catalogOnce.Do(buildCatalog)
	return catalogItems
}

// LookupConfig resolves a key against the registry.
//
// This is the fail-closed boundary of the whole surface: a key the platform
// does not declare is not found, and every caller treats not found as a
// refusal. There is no fallback that treats an unknown key as a string.
func LookupConfig(key ConfigKey) (ConfigDefinition, bool) {
	catalogOnce.Do(buildCatalog)
	definition, ok := catalogIndex[key]
	return definition, ok
}

// EditableConfigDefinitions returns the settings of one document, in catalog
// order.
func EditableConfigDefinitions(document ConfigDocument) []ConfigDefinition {
	definitions := make([]ConfigDefinition, 0, 16)
	for _, definition := range ConfigCatalog() {
		if definition.Editable && definition.Document == document {
			definitions = append(definitions, definition)
		}
	}
	return definitions
}

var (
	configKeyPattern    = regexp.MustCompile(`^[a-z][a-z0-9_.]{2,79}$`)
	configColumnPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,62}$`)
)

// ValidateConfigCatalog checks the invariants the registry must hold.
//
// It is exported and called from a test rather than from boot because the
// catalog is a compile-time literal: a violation is a source defect that must
// fail the build, not a runtime condition to degrade around. The checks are the
// ones that would otherwise become a security bug quietly:
//
//   - a duplicate key would make one definition unreachable and the other
//     authoritative, depending on order;
//   - an editable definition that is not backed by the database would mean the
//     console writes somewhere this design never reviewed — a Kubernetes
//     resource, a remote endpoint, a file. Requiring ConfigSourceDatabase is
//     also what keeps a network-calling validator, and with it an SSRF surface,
//     out of this API by construction;
//   - an editable sensitive definition would be class B, which needs a secret
//     backend that does not exist;
//   - a definition with no manage capability, or one the platform does not
//     define, would be an endpoint nobody can review the authorization of;
//   - a column name that is not a plain identifier would reach a statement
//     built by substitution.
func ValidateConfigCatalog() error {
	return ValidateConfigDefinitions(ConfigCatalog())
}

// ValidateConfigDefinitions checks any set of definitions against the same
// rules.
//
// Split out from ValidateConfigCatalog so the guards can be exercised against
// definitions that break them. A checker that has only ever seen a valid
// catalog is a checker nobody knows the failure modes of, and these are the
// failure modes that would otherwise become security bugs quietly.
func ValidateConfigDefinitions(definitions []ConfigDefinition) error {
	seen := make(map[ConfigKey]struct{}, len(definitions))
	for _, definition := range definitions {
		if _, duplicate := seen[definition.Key]; duplicate {
			return fmt.Errorf("%w: duplicate key %s", errConfigCatalog, definition.Key)
		}
		seen[definition.Key] = struct{}{}
		if err := validateConfigIdentity(definition); err != nil {
			return err
		}
		if err := validateEditability(definition); err != nil {
			return err
		}
		if err := validateReadOnly(definition); err != nil {
			return err
		}
	}
	return nil
}

// validateConfigIdentity checks what every definition owes, editable or not:
// a well-formed key, the prose the console renders, an owner and a category,
// and a class the platform can actually honour.
func validateConfigIdentity(definition ConfigDefinition) error {
	key := definition.Key
	if !configKeyPattern.MatchString(string(key)) {
		return fmt.Errorf("%w: malformed key %s", errConfigCatalog, key)
	}
	if definition.Label == "" || definition.Description == "" {
		return fmt.Errorf("%w: %s has no label or description", errConfigCatalog, key)
	}
	if definition.OwnerService == "" || definition.Category == "" {
		return fmt.Errorf("%w: %s has no owner or category", errConfigCatalog, key)
	}
	if definition.Class == ConfigClassRuntimeSecret {
		return fmt.Errorf("%w: %s claims class B, which needs a secret backend the platform does not have", errConfigCatalog, key)
	}
	return nil
}

// validateEditability checks what a definition must be before this API is
// allowed to write it, in two halves: where the value lives, and what the value
// is allowed to be.
func validateEditability(definition ConfigDefinition) error {
	if !definition.Editable {
		return nil
	}
	if err := validateEditableSource(definition); err != nil {
		return err
	}
	return validateEditableValue(definition)
}

// validateEditableSource is the half that decides whether writing this setting
// is even in scope: it must be a non-secret row of a known document in this
// database, applied at runtime. Every one of those is a boundary the write path
// assumes and never re-checks.
func validateEditableSource(definition ConfigDefinition) error {
	key := definition.Key
	if definition.Sensitive {
		return fmt.Errorf("%w: %s is editable and sensitive", errConfigCatalog, key)
	}
	if definition.Source != ConfigSourceDatabase {
		return fmt.Errorf("%w: %s is editable but not stored in the database", errConfigCatalog, key)
	}
	if definition.Class != ConfigClassRuntime || definition.Apply != ConfigApplyRuntime {
		return fmt.Errorf("%w: %s is editable but not class A runtime", errConfigCatalog, key)
	}
	if !ValidConfigDocument(definition.Document) {
		return fmt.Errorf("%w: %s names no known document", errConfigCatalog, key)
	}
	if !configColumnPattern.MatchString(definition.Column) {
		return fmt.Errorf("%w: %s has a malformed column", errConfigCatalog, key)
	}
	return nil
}

// validateEditableValue is the half that decides what may be stored: the
// capability a change demands, the range the API offers, a default that is
// itself acceptable, and a stated reason for any value the definition calls
// dangerous.
func validateEditableValue(definition ConfigDefinition) error {
	key := definition.Key
	if !IsKnownCapability(definition.ManageCapability) {
		return fmt.Errorf("%w: %s names an unknown capability", errConfigCatalog, key)
	}
	if definition.Type == ConfigTypeInt && definition.Min >= definition.Max {
		return fmt.Errorf("%w: %s has an empty range", errConfigCatalog, key)
	}
	if err := definition.Validate(definition.Default); err != nil {
		return fmt.Errorf("%w: %s has an invalid default: %v", errConfigCatalog, key, err)
	}
	if definition.Dangerous != nil && definition.DangerNote == "" {
		return fmt.Errorf("%w: %s can be dangerous and says nothing about it", errConfigCatalog, key)
	}
	return nil
}

func validateReadOnly(definition ConfigDefinition) error {
	key := definition.Key
	if definition.Editable {
		return nil
	}
	if definition.ReadOnlyReason == "" {
		return fmt.Errorf("%w: %s is read-only and gives no reason", errConfigCatalog, key)
	}
	if definition.ManageCapability != "" || definition.Column != "" || definition.Document != "" {
		return fmt.Errorf("%w: %s is read-only but carries write metadata", errConfigCatalog, key)
	}
	if definition.Rollbackable() {
		return fmt.Errorf("%w: %s is read-only but reports itself rollbackable", errConfigCatalog, key)
	}
	return nil
}
