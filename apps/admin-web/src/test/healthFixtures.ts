/**
 * Payload fixtures for the observability screens.
 *
 * They are written in the wire shape — snake_case, `latency_ms` absent rather
 * than null — so the specs exercise the same parsing a real response goes
 * through. A fixture written in the parsed shape would test the components
 * against a contract nothing produces.
 */

export const COLLECTED_AT = "2026-08-22T12:00:00.000Z";

export const POSTGRES_HEALTHY = {
  id: "postgres",
  display_name: "PostgreSQL",
  category: "data",
  impact: "Banco de dados da plataforma.",
  state: "healthy",
  enabled: true,
  observable: true,
  critical: true,
  latency_ms: 12,
  checked_at: COLLECTED_AT,
  config_key: "infra.postgres.host",
  runbook_path: "docs/runbooks/task-14-health-checks.md",
};

export const LIVEKIT_UNAVAILABLE = {
  id: "livekit",
  display_name: "LiveKit",
  category: "realtime",
  impact: "Servidor de mídia das chamadas.",
  state: "unavailable",
  enabled: true,
  observable: true,
  critical: false,
  latency_ms: 3001,
  checked_at: COLLECTED_AT,
  error_category: "connection_timeout",
  detail: "A dependência não respondeu dentro do tempo limite do check.",
  config_key: "calls.livekit.enabled",
  runbook_path: "docs/runbooks/task-livekit-coturn-dev.md",
};

export const SMTP_DISABLED = {
  id: "smtp",
  display_name: "SMTP",
  category: "messaging",
  impact: "Relay de e-mail.",
  state: "disabled",
  enabled: false,
  observable: true,
  critical: false,
  checked_at: COLLECTED_AT,
  config_key: "email.smtp.worker_enabled",
  runbook_path: "docs/runbooks/task-smtp-bruteforce-login-audit.md",
};

export const TURN_UNKNOWN = {
  id: "turn",
  display_name: "TURN / coturn",
  category: "realtime",
  impact: "Relay de mídia para redes restritivas.",
  state: "unknown",
  enabled: true,
  observable: false,
  critical: false,
  checked_at: COLLECTED_AT,
  error_category: "not_observable",
  detail: "Este pod não recebe a configuração que nomeia o endpoint desta integração.",
  runbook_path: "docs/runbooks/task-livekit-coturn-dev.md",
};

export const CLAMAV_DEGRADED = {
  id: "clamav",
  display_name: "ClamAV",
  category: "content",
  impact: "Antimalware dos anexos.",
  state: "degraded",
  enabled: true,
  observable: true,
  critical: false,
  latency_ms: 640,
  checked_at: COLLECTED_AT,
  error_category: "capacity_warning",
  detail: "A dependência respondeu, mas acima do tempo esperado.",
  version: "ClamAV 1.4.1",
  runbook_path: "docs/runbooks/file-service-envelope-encryption.md",
};

export function healthSnapshotPayload(services: unknown[] = defaultServices()) {
  return {
    data: {
      collected_at: COLLECTED_AT,
      overall: "degraded",
      services,
    },
  };
}

export function defaultServices() {
  return [LIVEKIT_UNAVAILABLE, CLAMAV_DEGRADED, TURN_UNKNOWN, POSTGRES_HEALTHY, SMTP_DISABLED];
}

export function metricPayload(overrides: Record<string, unknown> = {}) {
  return {
    key: "users.active_now",
    label: "Usuários ativos agora",
    definition: "Contas distintas com ao menos uma sessão de chat viva.",
    window: "instant",
    unit: "count",
    value: 3,
    available: true,
    ...overrides,
  };
}

export function overviewPayload(overrides: Record<string, unknown> = {}) {
  return {
    data: {
      summary: {
        collected_at: COLLECTED_AT,
        overall: "degraded",
        state_counts: { healthy: 1, degraded: 1, unavailable: 1, disabled: 1, unknown: 1 },
        metrics: [
          metricPayload(),
          metricPayload({
            key: "messages.last_24h",
            label: "Mensagens em 24 h",
            window: "last_24h",
            value: 431,
          }),
          metricPayload({
            key: "storage.stored_bytes",
            label: "Armazenamento utilizado",
            window: "cumulative",
            unit: "bytes",
            value: 2 * 1024 * 1024 * 1024,
          }),
        ],
        metrics_available: true,
        alerts: [
          {
            service_id: "livekit",
            severity: "warning",
            title: "LiveKit indisponível",
            impact: "Servidor de mídia das chamadas.",
            action: "Verifique se a dependência está de pé.",
            since: COLLECTED_AT,
            runbook_path: "docs/runbooks/task-livekit-coturn-dev.md",
            config_key: "calls.livekit.enabled",
          },
        ],
        ...overrides,
      },
    },
  };
}
