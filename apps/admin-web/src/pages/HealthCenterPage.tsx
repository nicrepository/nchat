import { useCallback, useMemo, useState } from "react";
import { useSearchParams } from "react-router";

import {
  listHealthChecks,
  refreshHealthChecks,
  type HealthSnapshot,
  type HealthState,
  type ServiceHealth,
} from "../api/observabilityApi";
import HealthStateBadge from "../components/HealthStateBadge";
import QueryStates from "../components/QueryStates";
import {
  categoryLabel,
  formatAge,
  formatLatency,
  presentState,
  sortServices,
  HEALTH_STATE_ORDER,
  type HealthSortKey,
} from "../lib/healthStatus";
import { classify, useAdminQuery } from "../lib/useAdminQuery";
import { useAutoRefresh, useNow } from "../lib/useAutoRefresh";
import { useLatestResult } from "../lib/useLatestResult";
import { formatDateTime } from "../lib/units";

/**
 * The Health Center.
 *
 * Every row is one dependency the *server* declares. This page never names a
 * host, a port or an endpoint, and there is no input on it that could carry
 * one: the refresh sends nothing at all, and the state filter and the sort are
 * applied here in the browser over a payload of a dozen rows.
 *
 * The one thing the page must never do is make an unchecked dependency look
 * checked, so `disabled` and `unknown` are rendered as distinctly as
 * `unavailable`, and a row with no measured round trip shows an em dash rather
 * than 0 ms.
 */
export default function HealthCenterPage() {
  const [searchParams] = useSearchParams();
  const load = useCallback((signal: AbortSignal) => listHealthChecks(signal), []);
  const query = useAdminQuery(load);

  // A newer snapshot supersedes the loaded one until the next full load. It
  // lives here rather than inside the table for two reasons: a reload must not
  // leave the page showing a collection the query no longer knows about, and
  // the periodic refresh and the manual button both write it — so the rule
  // that orders them has to sit above both. useLatestResult is that rule: a
  // response is applied only if no newer refresh has started since, whichever
  // of the two produced it and whatever order they land in.
  const { value: refreshed, run: applyLatest, discard } = useLatestResult<HealthSnapshot>();
  const snapshot = refreshed ?? query.data;

  const { reload: reloadQuery } = query;
  const reload = useCallback(() => {
    discard();
    reloadQuery();
  }, [discard, reloadQuery]);

  // The periodic refresh swaps the snapshot in place instead of going through
  // the query's reload. Going through it would drop the page back to its
  // loading state every minute, replacing a table an operator is reading with
  // a skeleton — and a failed background refresh would blank it entirely.
  // Failures here are simply not applied: the last good collection stays on
  // screen, with its own timestamp saying how old it is.
  const refreshQuietly = useCallback(() => {
    void applyLatest(listHealthChecks).catch(() => undefined);
  }, [applyLatest]);
  useAutoRefresh(refreshQuietly);

  // The manual button fetches from the forcing endpoint; which endpoint is the
  // page's decision, and the button only owns its own feedback.
  const refreshNow = useCallback(() => applyLatest(refreshHealthChecks), [applyLatest]);

  return (
    <section aria-labelledby="admin-health-title">
      <h1 id="admin-health-title">Health Center</h1>
      <p className="admin-lead">
        Estado real de cada dependência da plataforma, verificado no servidor. Configurada não
        significa saudável, e desconhecido não significa saudável.
      </p>

      <QueryStates
        status={query.status}
        message={query.message}
        empty="Nenhuma dependência declarada."
        isEmpty={snapshot !== null && snapshot.services.length === 0}
        onRetry={reload}
        skeletonRows={6}
      />

      {query.status === "ready" && snapshot !== null && (
        <HealthTable
          snapshot={snapshot}
          onRefresh={refreshNow}
          initialService={searchParams.get("service")}
        />
      )}

      <StateLegend />
    </section>
  );
}

interface HealthTableProps {
  snapshot: HealthSnapshot;
  /** Forces a collection. Resolves once the page has decided what to do with it. */
  onRefresh: () => Promise<void>;
  /** The row to open on arrival, when the dashboard linked to one. */
  initialService: string | null;
}

function HealthTable({ snapshot, onRefresh, initialService }: HealthTableProps) {
  const [stateFilter, setStateFilter] = useState<HealthState | "all">("all");
  const [sortKey, setSortKey] = useState<HealthSortKey>("attention");
  const [expanded, setExpanded] = useState<string | null>(initialService);

  const rows = useMemo(
    () =>
      sortServices(
        snapshot.services.filter(
          (service) => stateFilter === "all" || service.state === stateFilter,
        ),
        sortKey,
      ),
    [snapshot.services, stateFilter, sortKey],
  );

  // One reference instant for every row, seeded from the collection so the
  // first paint is correct without reading the clock during render.
  const now = useNow(Date.parse(snapshot.collectedAt));

  return (
    <>
      <RefreshBar snapshot={snapshot} onRefresh={onRefresh} />
      <Controls
        stateFilter={stateFilter}
        onStateFilter={setStateFilter}
        sortKey={sortKey}
        onSortKey={setSortKey}
        services={snapshot.services}
      />
      {rows.length === 0 ? (
        <p className="admin-empty">Nenhuma dependência neste estado.</p>
      ) : (
        <ServiceTable rows={rows} now={now} expanded={expanded} onExpand={setExpanded} />
      )}
    </>
  );
}

interface ServiceTableProps {
  rows: ServiceHealth[];
  now: number;
  expanded: string | null;
  onExpand: (id: string | null) => void;
}

function ServiceTable({ rows, now, expanded, onExpand }: ServiceTableProps) {
  return (
    <div className="admin-table-scroll">
      <table className="admin-table">
        <caption className="admin-visually-hidden">
          Dependências da plataforma, com estado, latência e momento da última verificação.
        </caption>
        <thead>
          <tr>
            <th scope="col">Serviço</th>
            <th scope="col">Estado</th>
            <th scope="col">Latência</th>
            <th scope="col">Última checagem</th>
            <th scope="col">Detalhes</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((service) => (
            <ServiceRow
              key={service.id}
              service={service}
              now={now}
              expanded={expanded === service.id}
              onToggle={() => onExpand(expanded === service.id ? null : service.id)}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * The manual refresh, and the timestamp of whatever is currently on screen.
 *
 * The button is disabled while a refresh is in flight, which is the client
 * half of not turning it into an amplifier; the server half — coalescing and a
 * minimum interval — is what actually bounds it, because a disabled button
 * protects nothing against a second tab.
 *
 * It reports a failure only when the request that failed is still the newest.
 * A manual refresh that the periodic one has already overtaken says nothing:
 * an error banner about a superseded request describes a question the page has
 * stopped asking.
 */
function RefreshBar({
  snapshot,
  onRefresh,
}: {
  snapshot: HealthSnapshot;
  onRefresh: () => Promise<void>;
}) {
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState("");

  const refresh = () => {
    if (refreshing) return;
    setRefreshing(true);
    setError("");
    onRefresh()
      .catch((cause: unknown) => setError(classify(cause).message))
      .finally(() => setRefreshing(false));
  };

  return (
    <div className="admin-refresh-bar">
      <p className="admin-table__muted">Última coleta: {formatDateTime(snapshot.collectedAt)}</p>
      <button type="button" className="admin-button" onClick={refresh} disabled={refreshing}>
        {refreshing ? "Atualizando…" : "Atualizar agora"}
      </button>
      {error !== "" && (
        <p role="alert" className="admin-alert">
          {error}
        </p>
      )}
    </div>
  );
}

interface ControlsProps {
  stateFilter: HealthState | "all";
  onStateFilter: (state: HealthState | "all") => void;
  sortKey: HealthSortKey;
  onSortKey: (key: HealthSortKey) => void;
  services: ServiceHealth[];
}

/**
 * Filtering and ordering.
 *
 * Both are real form controls with real labels rather than clickable table
 * headers: the console is operated by keyboard often, and a `<select>` is
 * reachable, announced and understood without any of the ARIA a sortable
 * header grid would need.
 */
function Controls({ stateFilter, onStateFilter, sortKey, onSortKey, services }: ControlsProps) {
  const countFor = (state: HealthState) => services.filter((row) => row.state === state).length;
  return (
    <div className="admin-health-controls">
      <label className="admin-field" htmlFor="health-state-filter">
        <span>Filtrar por estado</span>
        <select
          id="health-state-filter"
          value={stateFilter}
          onChange={(event) => onStateFilter(event.target.value as HealthState | "all")}
        >
          <option value="all">Todos ({services.length})</option>
          {HEALTH_STATE_ORDER.map((state) => (
            <option key={state} value={state}>
              {presentState(state).label} ({countFor(state)})
            </option>
          ))}
        </select>
      </label>

      <label className="admin-field" htmlFor="health-sort">
        <span>Ordenar por</span>
        <select
          id="health-sort"
          value={sortKey}
          onChange={(event) => onSortKey(event.target.value as HealthSortKey)}
        >
          <option value="attention">Prioridade de atenção</option>
          <option value="name">Serviço</option>
          <option value="latency">Latência</option>
        </select>
      </label>
    </div>
  );
}

interface ServiceRowProps {
  service: ServiceHealth;
  now: number;
  expanded: boolean;
  onToggle: () => void;
}

function ServiceRow({ service, now, expanded, onToggle }: ServiceRowProps) {
  const detailID = `health-detail-${service.id}`;
  return (
    <>
      <tr data-testid={`health-row-${service.id}`}>
        <th scope="row" className="admin-table__person">
          <span className="admin-table__name">{service.displayName}</span>
          <span className="admin-table__muted">
            {service.critical ? "Dependência crítica" : "Dependência de funcionalidade"}
          </span>
        </th>
        <td>
          <HealthStateBadge state={service.state} />
        </td>
        <td>{formatLatency(service.latencyMS)}</td>
        <td>
          {/* The relative age is what an operator reads; the absolute instant
              is what they quote in a ticket. Both, because neither alone is
              enough. */}
          <span>{formatAge(service.checkedAt, now)}</span>
          <span className="admin-table__muted">{formatDateTime(service.checkedAt)}</span>
        </td>
        <td>
          <button
            type="button"
            className="admin-button admin-button--quiet"
            aria-expanded={expanded}
            aria-controls={detailID}
            onClick={onToggle}
          >
            {expanded ? "Ocultar" : "Detalhes"}
          </button>
        </td>
      </tr>
      {expanded && <ServiceDetail service={service} detailID={detailID} />}
    </>
  );
}

/**
 * One dependency's diagnosis.
 *
 * Functional language first — what this means for users, what to do — with the
 * technical identifiers last and visually secondary. Nothing here is an
 * endpoint: the documentation reference is a static repository path the server
 * declared, and the only server-produced text is a sanitized category and a
 * version that passed through a character allowlist.
 */
function ServiceDetail({ service, detailID }: { service: ServiceHealth; detailID: string }) {
  return (
    <tr id={detailID} className="admin-health-detail">
      <td colSpan={5}>
        <p className="admin-health-detail__impact">{service.impact}</p>
        {service.detail !== "" && (
          <p className="admin-health-detail__diagnosis">{service.detail}</p>
        )}
        <ServiceQualifier enabled={service.enabled} observable={service.observable} />
        <dl className="admin-definitions admin-health-detail__technical">
          <dt>Categoria do erro</dt>
          <dd>{categoryLabel(service.errorCategory) || "—"}</dd>
          <dt>Habilitada na configuração</dt>
          <dd>{service.enabled ? "Sim" : "Não"}</dd>
          <dt>Observável deste serviço</dt>
          <dd>{service.observable ? "Sim" : "Não"}</dd>
          {service.version !== "" && (
            <>
              <dt>Versão</dt>
              <dd>{service.version}</dd>
            </>
          )}
          <dt>Documentação</dt>
          <dd>
            <code>{service.runbookPath}</code>
          </dd>
        </dl>
      </td>
    </tr>
  );
}

/**
 * The sentence that explains why nothing was checked.
 *
 * Switched off and invisible-from-here are different facts and lead to
 * different actions, so they get different sentences rather than a shared
 * "não verificado".
 */
function ServiceQualifier({ enabled, observable }: { enabled: boolean; observable: boolean }) {
  if (!enabled) {
    return (
      <p className="admin-notice">
        Integração desligada na configuração deste ambiente. Não é uma falha, e nenhuma verificação
        foi executada.
      </p>
    );
  }
  if (!observable) {
    return (
      <p className="admin-notice">
        O serviço administrativo não recebe a configuração que nomeia o endpoint desta integração,
        então nada foi verificado. Este é o estado desconhecido — não é saudável.
      </p>
    );
  }
  return null;
}

/**
 * What each state means.
 *
 * It is on the page rather than in a tooltip because the distinction between
 * disabled, unknown and unavailable is the thing this page exists to keep
 * straight, and a legend nobody can find is a legend that does not exist.
 */
function StateLegend() {
  return (
    <section aria-labelledby="admin-health-legend" className="admin-panel">
      <h2 id="admin-health-legend">O que cada estado significa</h2>
      <dl className="admin-definitions">
        {HEALTH_STATE_ORDER.map((state) => (
          <div key={state} className="admin-legend-entry">
            <dt>
              <HealthStateBadge state={state} />
            </dt>
            <dd>{presentState(state).meaning}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
