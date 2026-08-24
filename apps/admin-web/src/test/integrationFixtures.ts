/**
 * Payloads the integrations specs share.
 *
 * They are written as the API sends them — snake_case, `configured` without
 * `value` for a credential — so a spec that passes proves the parser accepts
 * the real contract rather than a shape invented for the test.
 */

export const COLLECTED_AT = "2026-08-23T11:00:00.000Z";

export function settingPayload(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    key: "oidc.enabled",
    label: "Single sign-on habilitado",
    description: "Com `false`, todos os endpoints OIDC respondem 404.",
    category: "integrations",
    owner_service: "auth-service",
    class: "C",
    source: "gitops",
    apply: "rollout",
    type: "string",
    nullable: false,
    editable: false,
    read_only_reason: "Definido no ConfigMap versionado em Git.",
    sensitive: false,
    rollbackable: false,
    env_var: "OIDC_ENABLED",
    observable: true,
    value: "true",
    advanced: false,
    ...overrides,
  };
}

export const CREDENTIAL_SETTING = settingPayload({
  key: "secret.oidc_client_secret",
  label: "OIDC — client secret",
  description: "Credencial do client usada na troca de código por token.",
  category: "credentials",
  class: "D",
  source: "sealed_secret",
  apply: "external",
  sensitive: true,
  env_var: "OIDC_CLIENT_SECRET",
  read_only_reason: "Credencial em Sealed Secret; a rotação segue o runbook.",
  configured: true,
  advanced: true,
  value: undefined,
});
// The API omits the field entirely for a credential; JSON.stringify drops an
// explicit undefined, and the spec relies on that rather than on a null.

export const OIDC_INTEGRATION = {
  id: "oidc",
  display_name: "Keycloak / OIDC",
  summary: "Single sign-on da plataforma. Indisponível, resta apenas o login local.",
  category: "identity",
  runbook_path: "docs/runbooks/task-auth-oidc-keycloak.md",
  health_service_id: "oidc",
  state: "degraded",
  enabled: true,
  observable: true,
  latency_ms: 120,
  checked_at: COLLECTED_AT,
  error_category: "invalid_configuration",
  detail: "O provedor respondeu com um issuer diferente do configurado.",
  version: "",
  diagnosable: true,
  stages: ["resolve", "connect", "tls", "discovery", "issuer", "jwks", "credential"],
  settings_visible: true,
  settings: [settingPayload(), CREDENTIAL_SETTING],
  actions: [],
};

export const SMTP_INTEGRATION = {
  id: "smtp",
  display_name: "SMTP",
  summary: "Relay de e-mail. Sem ele convites ficam na fila sem sair.",
  category: "messaging",
  runbook_path: "docs/runbooks/task-smtp-bruteforce-login-audit.md",
  health_service_id: "smtp",
  state: "disabled",
  enabled: false,
  observable: true,
  checked_at: COLLECTED_AT,
  error_category: "",
  detail: "",
  version: "",
  diagnosable: true,
  stages: ["resolve", "connect", "tls", "credential", "ready"],
  settings_visible: true,
  settings: [],
  actions: [
    {
      id: "smtp.test_email",
      label: "Enviar e-mail de teste",
      description: "Entrega uma mensagem fixa para o endereço da sua própria conta.",
      capability: "admin.integrations.manage",
    },
  ],
};

export const TURN_INTEGRATION = {
  id: "turn",
  display_name: "TURN / coturn",
  summary: "Relay de mídia para redes restritivas.",
  category: "realtime",
  runbook_path: "docs/runbooks/task-livekit-coturn-dev.md",
  health_service_id: "turn",
  state: "unknown",
  enabled: true,
  observable: false,
  checked_at: COLLECTED_AT,
  error_category: "not_observable",
  detail: "",
  version: "",
  diagnosable: false,
  diagnostic_unsupported:
    "Nenhuma variável de ambiente da plataforma nomeia o servidor TURN, então não há alvo que este serviço possa verificar sem inventá-lo.",
  stages: [],
  settings_visible: true,
  settings: [],
  actions: [],
};

export function integrationsPayload(
  integrations: unknown[] = [OIDC_INTEGRATION, SMTP_INTEGRATION, TURN_INTEGRATION],
): unknown {
  return { data: { collected_at: COLLECTED_AT, integrations } };
}

export function reportPayload(overrides: Record<string, unknown> = {}): unknown {
  return {
    data: {
      report: {
        integration: "oidc",
        started_at: "2026-08-23T11:05:00.000Z",
        status: "failed",
        summary: "Ao menos uma etapa falhou. As etapas seguintes não foram executadas.",
        steps: [
          { stage: "resolve", status: "passed", detail: "O nome resolve.", latency_ms: 3 },
          { stage: "connect", status: "passed", detail: "Conexão aceita.", latency_ms: 8 },
          {
            stage: "tls",
            status: "failed",
            category: "tls_error",
            detail: "Não foi possível estabelecer TLS com a dependência.",
            latency_ms: 41,
          },
          { stage: "credential", status: "skipped", detail: "Não executada." },
        ],
        ...overrides,
      },
    },
  };
}

/**
 * The configuration catalogue, as `GET /config` sends it.
 *
 * It carries one OIDC setting and one unrelated authentication-policy setting,
 * so a spec that follows the deep link can prove both halves: the OIDC field
 * arrives, and the search actually filtered rather than rendering everything.
 */
export function configCatalogPayload(): unknown {
  return {
    data: {
      documents: [{ key: "auth.policy", revision: 3 }],
      settings: [
        {
          key: "auth.password.min_length",
          label: "Tamanho mínimo da senha",
          description: "Número mínimo de caracteres exigido.",
          category: "authentication",
          owner_service: "auth-service",
          class: "A",
          source: "database",
          apply: "runtime",
          type: "int",
          unit: "caracteres",
          min: 8,
          max: 128,
          nullable: false,
          default: 12,
          editable: true,
          sensitive: false,
          document: "auth.policy",
          manage_capability: "admin.config.manage",
          rollbackable: true,
          observable: true,
          value: 12,
        },
        settingPayload(),
        CREDENTIAL_SETTING,
      ],
    },
  };
}
