import { useCallback, useState } from "react";
import { useSearchParams } from "react-router";

import {
  diagnoseIntegration,
  loadIntegrations,
  sendSMTPTestEmail,
  type Integration,
} from "../api/integrationsApi";
import ConfirmDialog from "../components/ConfirmDialog";
import IntegrationCard from "../components/IntegrationCard";
import QueryStates from "../components/QueryStates";
import { useAdminQuery } from "../lib/useAdminQuery";
import { useDiagnosticRun, type DiagnosticRun } from "../lib/useDiagnosticRun";
import { formatDateTime } from "../lib/units";
import { useAdminSession } from "../session/useAdminSession";

/**
 * Integrations (issue #582).
 *
 * Every integration NChat has, in one shape: status, configuration, diagnostic,
 * history. The status is passive — the same collection the Health Center made —
 * so opening this page contacts nothing. The diagnostic is the one thing here
 * that reaches the network, and it happens only when an operator presses a
 * button.
 *
 * The page never names a destination. There is no URL field, no host field and
 * no "test this endpoint" input: a request identifies an integration and the
 * server decides what that means from its own environment, which is what keeps
 * the console from being a proxy.
 */
export default function IntegrationsPage() {
  const { can } = useAdminSession();
  const [searchParams] = useSearchParams();
  const load = useCallback((signal: AbortSignal) => loadIntegrations(signal), []);
  const query = useAdminQuery(load);

  const [expanded, setExpanded] = useState<string | null>(searchParams.get("integration"));
  const run = useDiagnosticRun();
  const [pendingAction, setPendingAction] = useState<Integration | null>(null);

  const integrations = query.data?.integrations ?? [];

  // Switching cards clears the previous result rather than leaving it under a
  // different integration's heading, which would read as that one's diagnosis.
  const toggle = useCallback(
    (id: string) => {
      setExpanded((current) => (current === id ? null : id));
      run.reset();
    },
    [run],
  );

  return (
    <section aria-labelledby="admin-integrations-title">
      <h1 id="admin-integrations-title">Integrações</h1>
      <p className="admin-lead">
        Estado, configuração e diagnóstico de cada integração da plataforma. O estado vem da mesma
        coleta do Health Center e abrir esta página não contata nada; um diagnóstico só é executado
        quando você pede.
      </p>

      <QueryStates
        status={query.status}
        message={query.message}
        empty="Nenhuma integração declarada."
        isEmpty={query.data !== null && integrations.length === 0}
        onRetry={query.reload}
        skeletonRows={5}
      />

      {query.status === "ready" && query.data !== null && integrations.length > 0 && (
        <>
          <p className="admin-table__muted">
            Última coleta: {formatDateTime(query.data.collectedAt)}
          </p>
          {integrations.map((integration) => (
            <IntegrationCard
              key={integration.id}
              integration={integration}
              can={can}
              run={runFor(run, expanded === integration.id)}
              onDiagnose={() => run.start((signal) => diagnoseIntegration(integration.id, signal))}
              onAction={() => setPendingAction(integration)}
              expanded={expanded === integration.id}
              onToggle={() => toggle(integration.id)}
            />
          ))}
        </>
      )}

      {pendingAction !== null && (
        <TestEmailDialog
          pending={run.running}
          onConfirm={() => {
            setPendingAction(null);
            run.start((signal) => sendSMTPTestEmail(signal));
          }}
          onCancel={() => setPendingAction(null)}
        />
      )}
    </section>
  );
}

/**
 * One run belongs to one card.
 *
 * The hook is shared because only one diagnostic can be open at a time, but a
 * collapsed card must not render another integration's result. Handing it an
 * empty run is how that stays true without a second piece of state to keep in
 * sync.
 */
const IDLE: DiagnosticRun = {
  report: null,
  running: false,
  failure: "",
  start: () => undefined,
  reset: () => undefined,
};

function runFor(run: DiagnosticRun, active: boolean): DiagnosticRun {
  return active ? run : IDLE;
}

/**
 * The confirmation before a message leaves the platform.
 *
 * The destination is stated rather than asked for: it is the address of the
 * signed-in administrative account, decided by the server from the session. An
 * operator confirming this cannot redirect it, and neither can anyone holding a
 * stolen session.
 */
function TestEmailDialog({
  pending,
  onConfirm,
  onCancel,
}: {
  pending: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <ConfirmDialog
      title="Enviar e-mail de teste"
      description={
        <>
          Uma mensagem fixa será entregue pelo relay SMTP configurado, para o endereço da sua
          própria conta administrativa. O destino não é editável.
        </>
      }
      impact="A mensagem sai da plataforma e consome a reputação do relay. A ação é auditada e limitada a um envio por minuto."
      confirmLabel="Enviar"
      pending={pending}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
