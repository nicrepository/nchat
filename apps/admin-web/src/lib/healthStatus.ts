/**
 * Presentation rules for the Health Center and the dashboard (issue #581).
 *
 * These are pure functions, deliberately kept out of the components, because
 * they are the part that has to be *correct* rather than merely rendered: the
 * five states must stay distinguishable without colour, an unknown must never
 * read as healthy, and a latency that was never measured must not be printed
 * as zero.
 */

import type {
  HealthState,
  MetricUnit,
  MetricWindow,
  PlatformMetric,
} from "../api/observabilityApi";
import { formatBytes } from "./units";

/**
 * How each state is described in words.
 *
 * `mark` is the non-colour carrier of the same information. Every status in
 * this console is text as well as colour, and here it is text *and* a shape:
 * an operator with a monochrome screen, a colour vision deficiency, or a
 * screenshot pasted into a ticket reads the same thing everyone else does.
 */
export interface HealthStatePresentation {
  label: string;
  mark: string;
  /** The suffix of the CSS modifier class. Styling only; never a decision. */
  tone: "ok" | "warn" | "danger" | "muted" | "neutral";
  /** One sentence saying what this state means, for the legend. */
  meaning: string;
}

const PRESENTATION: Record<HealthState, HealthStatePresentation> = {
  healthy: {
    label: "Saudável",
    mark: "●",
    tone: "ok",
    meaning: "A verificação foi executada e a dependência respondeu corretamente.",
  },
  degraded: {
    label: "Degradado",
    mark: "▲",
    tone: "warn",
    meaning: "A dependência respondeu, mas algo na resposta exige atenção.",
  },
  unavailable: {
    label: "Indisponível",
    mark: "■",
    tone: "danger",
    meaning: "A verificação foi executada e a dependência não respondeu.",
  },
  disabled: {
    label: "Desabilitado",
    mark: "○",
    tone: "muted",
    meaning: "Integração desligada na configuração deste ambiente. Não é uma falha.",
  },
  unknown: {
    label: "Desconhecido",
    mark: "?",
    tone: "neutral",
    meaning: "Nenhuma verificação foi executada, então a plataforma não sabe. Não é saudável.",
  },
};

export function presentState(state: HealthState): HealthStatePresentation {
  return PRESENTATION[state] ?? PRESENTATION.unknown;
}

/** Every state, in the order the legend and the filter list them. */
export const HEALTH_STATE_ORDER: readonly HealthState[] = [
  "unavailable",
  "degraded",
  "unknown",
  "healthy",
  "disabled",
] as const;

/**
 * How much attention each state demands.
 *
 * The same ordering the server sorts by, restated here because the table can
 * be re-sorted in the browser and must not fall back to a different idea of
 * "worst first" when it is.
 */
const ATTENTION: Record<HealthState, number> = {
  unavailable: 4,
  degraded: 3,
  unknown: 2,
  healthy: 1,
  disabled: 0,
};

export function attentionRank(state: HealthState): number {
  return ATTENTION[state] ?? ATTENTION.unknown;
}

/** The sanitized failure categories, in the operator's language. */
const CATEGORY_LABELS: Record<string, string> = {
  connection_timeout: "Tempo limite de conexão",
  authentication_failed: "Falha de autenticação",
  tls_error: "Erro de TLS",
  dependency_unavailable: "Dependência indisponível",
  invalid_configuration: "Configuração inválida",
  capacity_warning: "Aviso de capacidade",
  not_observable: "Não observável deste serviço",
  protocol_error: "Resposta não interpretável",
};

/**
 * Names a failure category.
 *
 * An unrecognised code is shown as it arrived rather than dropped: the server
 * only ever sends values from a closed set, so seeing a raw one means the two
 * ends disagree, and hiding that would leave an operator with a blank cell and
 * no way to tell why.
 */
export function categoryLabel(category: string): string {
  if (category === "") return "";
  return CATEGORY_LABELS[category] ?? category;
}

/** How each metric's time window is described next to its number. */
const WINDOW_LABELS: Record<MetricWindow, string> = {
  instant: "agora",
  last_24h: "últimas 24 h",
  cumulative: "total",
};

export function windowLabel(window: MetricWindow): string {
  return WINDOW_LABELS[window] ?? "";
}

/**
 * Renders a latency.
 *
 * Null is an em dash and never "0 ms": a check that did not run has no round
 * trip, and printing a number for it would be inventing a measurement.
 */
export function formatLatency(latencyMS: number | null): string {
  if (latencyMS === null) return "—";
  if (latencyMS >= 1000) return `${(latencyMS / 1000).toFixed(1)} s`;
  return `${Math.round(latencyMS)} ms`;
}

/**
 * Renders how long ago a check ran, relative to a reference instant.
 *
 * The reference is a parameter rather than `Date.now()` so the value is
 * deterministic in a test and so every row on one render agrees about "now" —
 * a table where the first row says 5 s and the last says 6 s is describing one
 * collection as if it were several.
 */
export function formatAge(checkedAt: string, now: number): string {
  const timestamp = Date.parse(checkedAt);
  if (Number.isNaN(timestamp)) return "—";
  const seconds = Math.max(0, Math.round((now - timestamp) / 1000));
  if (seconds < 10) return "agora";
  if (seconds < 60) return `há ${seconds} s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `há ${minutes} min`;
  return `há ${Math.floor(minutes / 60)} h`;
}

/**
 * Renders a metric's value.
 *
 * "Indisponível" and "0" are different answers and this is the one place that
 * decides which one the operator sees.
 */
export function formatMetric(metric: PlatformMetric): string {
  if (!metric.available || metric.value === null) return "Indisponível";
  return formatMetricValue(metric.value, metric.unit);
}

function formatMetricValue(value: number, unit: MetricUnit): string {
  if (unit === "bytes") return formatBytes(value);
  return value.toLocaleString("pt-BR");
}

/** How the Health Center table may be ordered. */
export type HealthSortKey = "attention" | "name" | "latency";

export interface SortableService {
  displayName: string;
  state: HealthState;
  latencyMS: number | null;
}

/**
 * Orders the table.
 *
 * `attention` is the default and is what the page opens on, so the problem is
 * the first thing read. Under latency, a row with no measurement sorts last
 * rather than as zero — otherwise every disabled integration would crowd the
 * top of a column about speed.
 */
export function sortServices<T extends SortableService>(services: T[], key: HealthSortKey): T[] {
  const sorted = [...services];
  sorted.sort((left, right) => compareServices(left, right, key));
  return sorted;
}

function compareServices(
  left: SortableService,
  right: SortableService,
  key: HealthSortKey,
): number {
  if (key === "name") return left.displayName.localeCompare(right.displayName, "pt-BR");
  if (key === "latency") return compareLatency(left.latencyMS, right.latencyMS);
  const byAttention = attentionRank(right.state) - attentionRank(left.state);
  return byAttention !== 0
    ? byAttention
    : left.displayName.localeCompare(right.displayName, "pt-BR");
}

function compareLatency(left: number | null, right: number | null): number {
  if (left === null && right === null) return 0;
  if (left === null) return 1;
  if (right === null) return -1;
  return right - left;
}
