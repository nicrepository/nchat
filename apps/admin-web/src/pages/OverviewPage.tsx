import { useCallback } from "react";
import { Link } from "react-router";

import {
  getOverview,
  type DashboardSummary,
  type HealthAlert,
  type PlatformMetric,
} from "../api/observabilityApi";
import HealthStateBadge from "../components/HealthStateBadge";
import QueryStates from "../components/QueryStates";
import { formatMetric, presentState, windowLabel, HEALTH_STATE_ORDER } from "../lib/healthStatus";
import { useAdminQuery } from "../lib/useAdminQuery";
import { useAutoRefresh } from "../lib/useAutoRefresh";
import { useLatestResult } from "../lib/useLatestResult";
import { formatDateTime } from "../lib/units";
import { useAdminSession } from "../session/useAdminSession";

/**
 * The console's landing page.
 *
 * It answers one question, in this order: is the platform healthy, is there
 * anything to act on right now, and how much is it carrying. Everything below
 * the fold is context; everything above it is a decision.
 *
 * Two rules shape what is here. It is one request, not one per card — the
 * server composes the summary — and nothing on it is decorative: there is no
 * chart, because a sparkline of a number nobody can act on is a worse use of
 * the space than the number itself.
 */
export default function OverviewPage() {
  const { bootstrap, can } = useAdminSession();
  if (bootstrap === null) return null;

  return (
    <section aria-labelledby="admin-overview-title">
      <h1 id="admin-overview-title">Visão geral</h1>
      <p className="admin-lead">
        Sessão administrativa ativa para {bootstrap.identity.display_name} em{" "}
        {bootstrap.environment}.
      </p>

      {can("admin.infrastructure.read") ? (
        <OperationalDashboard />
      ) : (
        <p className="admin-notice">
          Esta sessão não tem a permissão <code>admin.infrastructure.read</code>, então o painel
          operacional não é exibido. As demais seções seguem disponíveis conforme suas permissões.
        </p>
      )}

      <SessionDetails
        idleExpiresAt={bootstrap.session.idle_expires_at}
        absoluteExpiresAt={bootstrap.session.absolute_expires_at}
        capabilities={bootstrap.capabilities}
      />
    </section>
  );
}

/**
 * The operational half of the page.
 *
 * It is a component rather than a branch inside the page so the request lives
 * and dies with the section: a session without the capability never mounts it
 * and therefore never asks. That is a courtesy and not a control — the endpoint
 * refuses the same request whether or not this component rendered — but asking
 * for something already known to be refused would put a 403 in the audit trail
 * every time an operator opens their own landing page.
 */
function OperationalDashboard() {
  const load = useCallback((signal: AbortSignal) => getOverview(signal), []);
  const query = useAdminQuery(load);

  // The periodic refresh swaps the summary in place rather than going through
  // the query's reload, which would drop the dashboard back to its skeleton
  // every minute. A background refresh that fails is simply not applied: the
  // last good summary stays on screen with its own "coleta de" timestamp,
  // which is what says how old it is.
  //
  // useLatestResult is what keeps that timestamp moving forward only. Two
  // refreshes can overlap — the interval fires again while one is still in
  // flight, or a returning tab triggers one on top of it — and nothing about
  // HTTP guarantees they land in the order they started.
  const { value: refreshed, run: applyLatest, discard } = useLatestResult<DashboardSummary>();
  const refreshQuietly = useCallback(() => {
    void applyLatest(getOverview).catch(() => undefined);
  }, [applyLatest]);
  // Moderate rather than polling: the server serves a cached collection for far
  // less than this interval, so a dashboard left open stays current without
  // turning every open tab into load on the integrations.
  useAutoRefresh(refreshQuietly);

  const { reload: reloadQuery } = query;
  const retry = useCallback(() => {
    discard();
    reloadQuery();
  }, [discard, reloadQuery]);

  const summary = refreshed ?? query.data;

  return (
    <>
      <QueryStates
        status={query.status}
        message={query.message}
        empty="Nenhum dado operacional disponível."
        isEmpty={false}
        onRetry={retry}
        skeletonRows={4}
      />
      {query.status === "ready" && summary !== null && <DashboardBody summary={summary} />}
    </>
  );
}

/** The three sections of the dashboard, in the order they answer questions. */
function DashboardBody({ summary }: { summary: DashboardSummary }) {
  return (
    <>
      <PlatformState summary={summary} />
      <Alerts alerts={summary.alerts} />
      <Metrics metrics={summary.metrics} available={summary.metricsAvailable} />
    </>
  );
}

/**
 * The headline: one state, the count of services behind it, and when it was
 * measured.
 *
 * `aria-live="polite"` is on the state alone. It is the one thing on the page
 * whose change an operator must not miss, and confining the live region to it
 * is what keeps a periodic refresh from reading the whole dashboard aloud
 * every time a number moves.
 */
function PlatformState({ summary }: { summary: DashboardSummary }) {
  const presentation = presentState(summary.overall);
  return (
    <section aria-labelledby="admin-platform-state" className="admin-panel">
      <h2 id="admin-platform-state">Estado da plataforma</h2>
      <p className="admin-platform-state" role="status" aria-live="polite">
        <HealthStateBadge state={summary.overall} />
        <span className="admin-platform-state__meaning">{presentation.meaning}</span>
      </p>
      <ul className="admin-state-counts">
        {HEALTH_STATE_ORDER.map((state) => (
          <li key={state} className="admin-state-counts__item">
            <span className="admin-state-counts__value">{summary.stateCounts[state]}</span>
            <span className="admin-state-counts__label">{presentState(state).label}</span>
          </li>
        ))}
      </ul>
      <p className="admin-table__muted">
        Coleta de {formatDateTime(summary.collectedAt)}.{" "}
        <Link to="/health">Abrir o Health Center</Link>
      </p>
    </section>
  );
}

/**
 * What needs attention now.
 *
 * Empty is a real and good answer, and it is stated rather than left as blank
 * space: "nothing to act on" and "this section failed to load" must not look
 * the same.
 */
function Alerts({ alerts }: { alerts: HealthAlert[] }) {
  return (
    <section aria-labelledby="admin-alerts-title" className="admin-panel">
      <h2 id="admin-alerts-title">Requer atenção</h2>
      {alerts.length === 0 ? (
        <p className="admin-empty">Nenhuma condição acionável no momento.</p>
      ) : (
        <ul className="admin-alert-list">
          {alerts.map((alert) => (
            <AlertItem key={alert.serviceId} alert={alert} />
          ))}
        </ul>
      )}
    </section>
  );
}

function AlertItem({ alert }: { alert: HealthAlert }) {
  const severity = alert.severity === "critical" ? "Crítico" : "Atenção";
  return (
    <li className={`admin-alert-item admin-alert-item--${alert.severity}`}>
      <h3 className="admin-alert-item__title">
        <span className="admin-alert-item__severity">{severity}</span> {alert.title}
      </h3>
      <p className="admin-alert-item__impact">{alert.impact}</p>
      <p className="admin-alert-item__action">{alert.action}</p>
      <p className="admin-table__muted">
        Observado em {formatDateTime(alert.since)} ·{" "}
        <Link to={`/health?service=${encodeURIComponent(alert.serviceId)}`}>Ver diagnóstico</Link>
        {alert.configKey !== "" && (
          <>
            {" · "}
            <Link to="/configuration">Ver configuração</Link>
          </>
        )}
      </p>
    </li>
  );
}

/**
 * The volume the platform is carrying.
 *
 * Every card states its own window, because "431 mensagens" without one is a
 * number nobody can act on. A card whose aggregate did not run says
 * "Indisponível" instead of a zero.
 */
function Metrics({ metrics, available }: { metrics: PlatformMetric[]; available: boolean }) {
  return (
    <section aria-labelledby="admin-metrics-title" className="admin-panel">
      <h2 id="admin-metrics-title">Volume</h2>
      {!available && (
        <p role="alert" className="admin-warning">
          Os indicadores não puderam ser calculados nesta coleta. Os valores abaixo estão marcados
          como indisponíveis em vez de serem exibidos como zero.
        </p>
      )}
      <ul className="admin-metric-grid">
        {metrics.map((metric) => (
          <MetricCard key={metric.key} metric={metric} />
        ))}
      </ul>
    </section>
  );
}

function MetricCard({ metric }: { metric: PlatformMetric }) {
  return (
    <li className="admin-metric" data-testid={`metric-${metric.key}`}>
      <span className="admin-metric__label">{metric.label}</span>
      <span
        className={`admin-metric__value${metric.available ? "" : " admin-metric__value--absent"}`}
      >
        {formatMetric(metric)}
      </span>
      <span className="admin-metric__window">{windowLabel(metric.window)}</span>
      {/* The definition is always present rather than behind a tooltip: it is
          what makes the number verifiable, and a tooltip is unreachable by
          keyboard and invisible on a printed screenshot. */}
      <span className="admin-metric__definition">{metric.definition}</span>
    </li>
  );
}

interface SessionDetailsProps {
  idleExpiresAt: string;
  absoluteExpiresAt: string;
  capabilities: string[];
}

/**
 * What this session is. It stays on the landing page — it was the whole of it
 * before the dashboard existed — but below the operational content, because it
 * answers a question nobody asks in an incident.
 */
function SessionDetails({ idleExpiresAt, absoluteExpiresAt, capabilities }: SessionDetailsProps) {
  return (
    <section aria-labelledby="admin-session-title" className="admin-panel">
      <h2 id="admin-session-title">Esta sessão</h2>
      <dl className="admin-definitions">
        <dt>Expira por inatividade em</dt>
        <dd>{formatDateTime(idleExpiresAt)}</dd>
        <dt>Expira definitivamente em</dt>
        <dd>{formatDateTime(absoluteExpiresAt)}</dd>
      </dl>
      <h3>Permissões efetivas</h3>
      {capabilities.length === 0 ? (
        <p>Nenhuma permissão administrativa atribuída.</p>
      ) : (
        <ul className="admin-capability-list">
          {capabilities.map((capability) => (
            <li key={capability}>
              <code>{capability}</code>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
