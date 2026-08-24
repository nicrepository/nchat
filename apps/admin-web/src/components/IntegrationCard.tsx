import { Link } from "react-router";

import type { Integration } from "../api/integrationsApi";
import { formatLatency } from "../lib/healthStatus";
import { canDiagnose, passiveSummary, splitSettings, stageLabel } from "../lib/integrationView";
import { formatDateTime } from "../lib/units";
import type { DiagnosticRun } from "../lib/useDiagnosticRun";
import ConfigField from "./ConfigField";
import DiagnosticSteps from "./DiagnosticSteps";
import HealthStateBadge from "./HealthStateBadge";

export interface IntegrationCardProps {
  integration: Integration;
  can: (capability: string) => boolean;
  run: DiagnosticRun;
  onDiagnose: () => void;
  /**
   * Opens the confirmation for an explicit action.
   *
   * It takes nothing because SMTP's test message is the only action the
   * registry declares, and a parameter naming which one would be flexibility
   * nobody uses. A second action is one field away, and adding it then is
   * cheaper than carrying an unread argument until it arrives.
   */
  onAction: () => void;
  /** True when this card was deep-linked to and should render expanded. */
  expanded: boolean;
  onToggle: () => void;
}

/**
 * One integration, in the four sections every integration gets.
 *
 * Status, configuration, diagnostic, history — the same order and the same
 * shape for all of them, so an operator who has read one card can read the
 * others. The alternative, a page of environment variables grouped by prefix,
 * is what this issue exists to replace.
 *
 * Nothing on this card is a security boundary. The diagnostic button is hidden
 * without the capability because offering a control that returns 403 is bad
 * user experience, not because hiding it protects anything: the API re-decides
 * on every request.
 */
export default function IntegrationCard({
  integration,
  can,
  run,
  onDiagnose,
  onAction,
  expanded,
  onToggle,
}: IntegrationCardProps) {
  const headingID = `integration-${integration.id}`;
  const bodyID = `${headingID}-body`;
  return (
    <article className="admin-panel" data-testid={`integration-${integration.id}`}>
      <header className="admin-integration__header">
        <h2 id={headingID}>{integration.displayName}</h2>
        <HealthStateBadge state={integration.state} />
        <button
          type="button"
          className="admin-button admin-button--quiet"
          aria-expanded={expanded}
          aria-controls={bodyID}
          onClick={onToggle}
        >
          {expanded ? "Recolher" : "Abrir"}
        </button>
      </header>
      <p className="admin-integration__impact">{integration.summary}</p>

      {expanded && (
        <div id={bodyID}>
          <StatusSection integration={integration} />
          <ConfigurationSection integration={integration} />
          <DiagnosticSection
            integration={integration}
            can={can}
            run={run}
            onDiagnose={onDiagnose}
            onAction={onAction}
            headingID={headingID}
          />
          <HistorySection />
        </div>
      )}
    </article>
  );
}

function StatusSection({ integration }: { integration: Integration }) {
  return (
    <section className="admin-integration__section">
      <h3>Status</h3>
      <p>{passiveSummary(integration)}</p>
      <dl className="admin-definitions">
        <dt>Habilitada na configuração</dt>
        <dd>{integration.enabled ? "Sim" : "Não"}</dd>
        <dt>Observável deste serviço</dt>
        <dd>{integration.observable ? "Sim" : "Não"}</dd>
        <dt>Latência da última coleta</dt>
        <dd>{formatLatency(integration.latencyMS)}</dd>
        <dt>Última coleta</dt>
        <dd>{formatDateTime(integration.checkedAt)}</dd>
        {integration.version !== "" && (
          <>
            <dt>Versão</dt>
            <dd>{integration.version}</dd>
          </>
        )}
      </dl>
      <p className="admin-table__muted">
        Estado passivo, da mesma coleta do <Link to="/health">Health Center</Link>. Abrir esta
        página não contata nenhuma dependência.
      </p>
    </section>
  );
}

/**
 * The integration's settings, exactly as the configuration surface of issue
 * #580 declares them.
 *
 * Read-only, every one of them, and that is not a limitation of this screen: no
 * integration setting is class A. They come from a Git-managed ConfigMap or
 * from a Sealed Secret, so changing one is a commit and a rollout or the
 * rotation runbook, and each field says which. A credential shows whether it is
 * configured and never what it is, because no endpoint returns one.
 */
function ConfigurationSection({ integration }: { integration: Integration }) {
  if (!integration.settingsVisible) {
    return (
      <section className="admin-integration__section">
        <h3>Configuração</h3>
        <p className="admin-notice">
          O inventário de configuração exige a permissão <code>admin.config.read</code>, que esta
          conta não tem. O estado e o diagnóstico acima continuam disponíveis.
        </p>
      </section>
    );
  }
  const { common, advanced } = splitSettings(integration.settings);
  return (
    <section className="admin-integration__section">
      <h3>Configuração</h3>
      {common.length === 0 && advanced.length === 0 ? (
        <p className="admin-empty">Esta integração não expõe configuração pela Admin API.</p>
      ) : (
        <ul className="admin-config-list">
          {common.map((setting) => (
            <ConfigField
              key={setting.key}
              setting={setting}
              draft=""
              editable={false}
              disabled
              onChange={() => undefined}
            />
          ))}
        </ul>
      )}
      {advanced.length > 0 && (
        <details className="admin-subsection">
          <summary>Configuração avançada ({advanced.length})</summary>
          <ul className="admin-config-list">
            {advanced.map((setting) => (
              <ConfigField
                key={setting.key}
                setting={setting}
                draft=""
                editable={false}
                disabled
                onChange={() => undefined}
              />
            ))}
          </ul>
        </details>
      )}
      <p className="admin-table__muted">
        Inventário completo e histórico de alterações em{" "}
        {/* The identifier, never the display name. "Keycloak / OIDC" is
            presentation — it is translated, it carries a slash the search
            tokenises, and the word "Keycloak" appears in no configuration key
            — so linking on it lands the operator on an empty result. The id is
            the same slug the configuration keys are namespaced with
            (oidc.*, email.smtp.*, calls.livekit.*, infra.storage.*), which is
            what makes the deep link find them. */}
        <Link
          to={`/configuration?q=${encodeURIComponent(integration.id)}`}
          data-testid={`configure-${integration.id}`}
        >
          Configurações
        </Link>
        .
      </p>
    </section>
  );
}

interface DiagnosticSectionProps {
  integration: Integration;
  can: (capability: string) => boolean;
  run: DiagnosticRun;
  onDiagnose: () => void;
  /**
   * Opens the confirmation for an explicit action.
   *
   * It takes nothing because SMTP's test message is the only action the
   * registry declares, and a parameter naming which one would be flexibility
   * nobody uses. A second action is one field away, and adding it then is
   * cheaper than carrying an unread argument until it arrives.
   */
  onAction: () => void;
  headingID: string;
}

/**
 * The one control on this page that reaches the network.
 *
 * It never runs on its own. There is no effect that starts it, no refresh
 * interval and no retry: a diagnostic opens outbound connections and, for the
 * test message, hands mail to a relay, so it happens when an operator asks and
 * at no other time.
 */
function DiagnosticSection({
  integration,
  can,
  run,
  onDiagnose,
  onAction,
  headingID,
}: DiagnosticSectionProps) {
  const allowed = canDiagnose(integration, can);
  return (
    <section className="admin-integration__section">
      <h3>Teste e diagnóstico</h3>
      {!integration.diagnosable ? (
        <p className="admin-notice" data-testid={`diagnostic-unsupported-${integration.id}`}>
          {integration.diagnosticUnsupported}
        </p>
      ) : (
        <>
          <p className="admin-table__muted">
            Etapas verificadas: {integration.stages.map(stageLabel).join(" · ")}.
          </p>
          <div className="admin-integration__actions">
            <button
              type="button"
              className="admin-button"
              disabled={!allowed || run.running}
              onClick={onDiagnose}
              data-testid={`diagnose-${integration.id}`}
            >
              {run.running ? "Executando…" : "Executar diagnóstico"}
            </button>
            {integration.actions.map((action) => (
              <button
                key={action.id}
                type="button"
                className="admin-button admin-button--ghost"
                disabled={!can(action.capability) || run.running}
                onClick={onAction}
                data-testid={`action-${action.id}`}
              >
                {action.label}
              </button>
            ))}
          </div>
          {integration.actions.map((action) => (
            <p key={action.id} className="admin-field__help">
              {action.description}
            </p>
          ))}
          {!allowed && (
            <p className="admin-notice">
              Executar um diagnóstico exige a permissão <code>admin.integrations.manage</code>.
            </p>
          )}
          {run.failure !== "" && (
            <p role="alert" className="admin-alert">
              {run.failure}
            </p>
          )}
          {run.report !== null && <DiagnosticSteps report={run.report} labelledBy={headingID} />}
        </>
      )}
    </section>
  );
}

function HistorySection() {
  return (
    <section className="admin-integration__section">
      <h3>Histórico e auditoria</h3>
      <p className="admin-table__muted">
        Todo diagnóstico executado e toda mensagem de teste enviada geram um evento na{" "}
        <Link to="/audit">trilha de auditoria</Link>, com o ator, a integração e o resultado
        categorizado — nunca o alvo nem a resposta. As alterações de configuração têm histórico
        próprio em <Link to="/configuration">Configurações</Link>.
      </p>
    </section>
  );
}
