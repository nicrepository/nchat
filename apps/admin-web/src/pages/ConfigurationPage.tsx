import { useCallback, useMemo, useState } from "react";
import { useSearchParams } from "react-router";

import {
  listConfigVersions,
  loadConfiguration,
  type ConfigCatalog,
  type ConfigSetting,
  type ConfigVersion,
} from "../api/configApi";
import ConfigDiffDialog from "../components/ConfigDiffDialog";
import ConfigField from "../components/ConfigField";
import ConfigPolicyForm from "../components/ConfigPolicyForm";
import ConfigSearch from "../components/ConfigSearch";
import QueryStates from "../components/QueryStates";
import {
  AUTH_POLICY_DOCUMENT,
  categoryLabel,
  changedValues,
  formatConfigValue,
  groupByCategory,
} from "../lib/configFields";
import { searchSettings } from "../lib/configSearch";
import { useAdminQuery } from "../lib/useAdminQuery";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import { useConfigDrafts } from "../lib/useConfigDrafts";
import { useConfigReview, type ConfigReviewKind } from "../lib/useConfigReview";
import { useAdminSession } from "../session/useAdminSession";

const VERSION_PAGE_SIZE = 25;

/**
 * Configuration & Secrets Management (issue #580).
 *
 * The screen shows the whole configuration inventory and lets an operator
 * change the part of it the platform can genuinely apply: the authentication
 * policy, which auth-service reads from the database on the request that
 * enforces it. Everything else is shown with its class, its source of truth and
 * the reason it is not editable here — a value read at boot from a Git-managed
 * ConfigMap, or a credential that rotates through Sealed Secrets.
 *
 * The edit path is deliberately three steps and not one. The form collects a
 * draft, the server computes what that draft would change against the state
 * that exists *now*, and only then is anything written, under the revision the
 * form was loaded at. That is what turns two administrators saving at once into
 * one write and one visible conflict instead of a silent overwrite.
 *
 * Nothing here is a security boundary. Every control this page hides is a
 * control the API refuses anyway.
 */
export default function ConfigurationPage() {
  const { can } = useAdminSession();
  const [searchParams] = useSearchParams();
  const canManage = can("admin.config.manage");

  const loadCatalog = useCallback((signal: AbortSignal) => loadConfiguration(signal), []);
  const catalog = useAdminQuery(loadCatalog);

  const loadVersions = useCallback(
    (signal: AbortSignal) => listConfigVersions(AUTH_POLICY_DOCUMENT, VERSION_PAGE_SIZE, signal),
    [],
  );
  const versions = useAdminQuery(loadVersions);

  const { settings, revision } = useMemo(() => readCatalog(catalog.data), [catalog.data]);

  // The term seeds from the URL so a card on the integrations page can link
  // straight to the settings it owns, and it is debounced so typing a name is
  // one filter pass rather than one per keystroke.
  const [term, setTerm] = useState(searchParams.get("q") ?? "");
  const settled = useDebouncedValue(term);
  const visible = useMemo(() => searchSettings(settings, settled), [settings, settled]);

  const drafts = useConfigDrafts(catalog.data, settings);
  const flow = useConfigReview({
    revision,
    onApplied: () => {
      catalog.reload();
      versions.reload();
    },
  });

  return (
    <section aria-labelledby="admin-config-title">
      <h1 id="admin-config-title">Configurações</h1>
      <p className="admin-lead">
        Inventário completo das configurações da plataforma. Só é editável aqui o que o NChat
        consegue realmente aplicar sem rollout: a política de autenticação, lida do banco pelo
        auth-service a cada requisição. O restante aparece com sua classe, sua fonte de verdade e o
        motivo de não ser editável.
      </p>

      {/* Outside the loading gate on purpose. A successful apply reloads the
          whole catalogue — the revision every other field is edited against has
          moved, so the form must be rebuilt from the server rather than patched
          in place — and the operator's confirmation has to survive that reload
          instead of disappearing behind a skeleton. */}
      {flow.feedback !== null && (
        <p role="status" className="admin-notice" data-testid="config-feedback">
          {flow.feedback}
        </p>
      )}
      {/* Only while no dialog is open: the dialog carries its own failure, and
          two copies of one message read as two problems. */}
      {flow.failure !== null && flow.review === null && (
        <p role="alert" className="admin-alert" data-testid="config-failure">
          {flow.failure}
        </p>
      )}

      <QueryStates
        status={catalog.status}
        message={catalog.message}
        empty="Nenhuma configuração declarada."
        isEmpty={catalog.data !== null && settings.length === 0}
        onRetry={catalog.reload}
        skeletonRows={6}
      />

      {catalog.status === "ready" && settings.length > 0 && (
        <>
          <ConfigSearch
            term={term}
            onTerm={setTerm}
            matches={visible.length}
            total={settings.length}
          />

          <ConfigPolicyForm
            settings={visible.filter((setting) => setting.editable)}
            drafts={drafts}
            revision={revision}
            canManage={canManage}
            applying={flow.busy}
            // The change set is computed over *every* setting, not the filtered
            // view. A field edited before the search was typed is still an edit,
            // and dropping it because a filter hid it would discard work
            // silently. The diff dialog is what shows the operator everything
            // they are about to apply.
            onReview={() => flow.open(changedValues(settings, drafts.drafts))}
          />

          <ReadOnlySections
            settings={visible.filter((setting) => !setting.editable)}
            searching={settled.trim() !== ""}
          />

          <VersionHistory
            versions={versions}
            canManage={canManage}
            busy={flow.busy}
            // The version's identity and nothing else. Which values a rollback
            // restores — and whether it can still be performed — is derived by
            // the server from the recorded version; rebuilding it from the
            // history rendered here would make presentation data the authority
            // for an administrative mutation.
            onRollback={(version) => flow.openRollback(version.id)}
          />
        </>
      )}

      {flow.review !== null && (
        <ConfigDiffDialog
          title={reviewTitle(flow.review.request.kind)}
          plan={flow.review.plan}
          confirmLabel={reviewConfirmLabel(flow.review.request.kind)}
          pending={flow.applying}
          failure={flow.failure}
          onConfirm={flow.confirm}
          onCancel={flow.cancel}
        />
      )}
    </section>
  );
}

/**
 * What the loaded catalogue means for this screen: every declared setting, and
 * the revision the editable ones are changed against.
 *
 * Nothing loaded is not an error and not an empty screen — it is the state
 * QueryStates is already describing — so it resolves to no settings and
 * revision zero, which no write can use.
 */
function readCatalog(catalog: ConfigCatalog | null): {
  settings: ConfigSetting[];
  revision: number;
} {
  if (catalog === null) {
    return { settings: [], revision: 0 };
  }
  const document = catalog.documents.find((entry) => entry.key === AUTH_POLICY_DOCUMENT);
  return { settings: catalog.settings, revision: document?.revision ?? 0 };
}

function reviewTitle(kind: ConfigReviewKind): string {
  return kind === "rollback" ? "Reverter configuração" : "Revisar alterações";
}

function reviewConfirmLabel(kind: ConfigReviewKind): string {
  return kind === "rollback" ? "Reverter" : "Aplicar";
}

/**
 * Everything the console cannot change, grouped by category and collapsed.
 *
 * Collapsed because it is reference material an operator consults, not a form
 * they fill in — and because leaving forty read-only rows expanded above the
 * history would bury both.
 */
function ReadOnlySections({
  settings,
  searching,
}: {
  settings: ConfigSetting[];
  /** Open every group while a term is active, so a match is never hidden. */
  searching: boolean;
}) {
  const groups = groupByCategory(settings);
  return (
    <>
      <h2>Configurações não editáveis pelo console</h2>
      <p className="admin-table__muted">
        Valores efetivos observados por este serviço. A fonte de verdade é o Git ou a
        infraestrutura, e alterá-los exige commit e rollout.
      </p>
      {groups.length === 0 && searching && (
        <p className="admin-empty">Nenhuma configuração corresponde à busca.</p>
      )}
      {groups.map(([category, entries]) => (
        <details key={category} className="admin-subsection" open={searching}>
          <summary>
            {categoryLabel(category)} ({entries.length})
          </summary>
          <ul className="admin-config-list">
            {entries.map((setting) => (
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
      ))}
    </>
  );
}

interface VersionHistoryProps {
  versions: ReturnType<typeof useAdminQuery<ConfigVersion[]>>;
  canManage: boolean;
  busy: boolean;
  onRollback: (version: ConfigVersion) => void;
}

/**
 * The change history, newest first.
 *
 * Append-only: a rollback appears as its own entry naming the revision it
 * reverted, so an apply/rollback sequence reads as three changes rather than as
 * a change that vanished.
 */
function VersionHistory({ versions, canManage, busy, onRollback }: VersionHistoryProps) {
  const entries = versions.data ?? [];
  return (
    <>
      <h2>Histórico</h2>
      <QueryStates
        status={versions.status}
        message={versions.message}
        empty="Nenhuma alteração registrada."
        isEmpty={versions.data !== null && entries.length === 0}
        onRetry={versions.reload}
      />
      {versions.status === "ready" && entries.length > 0 && (
        <ul className="admin-version-list" data-testid="config-versions">
          {entries.map((version) => (
            <VersionEntry
              key={version.id}
              version={version}
              canManage={canManage}
              busy={busy}
              onRollback={onRollback}
            />
          ))}
        </ul>
      )}
    </>
  );
}

interface VersionEntryProps {
  version: ConfigVersion;
  canManage: boolean;
  busy: boolean;
  onRollback: (version: ConfigVersion) => void;
}

/**
 * One recorded change: when, by whom, what moved, and whether it can be undone.
 *
 * `rollbackable` is the server's answer, not this component's guess. A version
 * whose value today's registry would refuse is not offered, and says so instead
 * of showing a button that would fail.
 */
function VersionEntry({ version, canManage, busy, onRollback }: VersionEntryProps) {
  return (
    <li className="admin-version">
      <p className="admin-version__header">
        <strong>Revisão {version.revision}</strong> ·{" "}
        {new Date(version.appliedAt).toLocaleString("pt-BR")} ·{" "}
        {version.actorEmail === "" ? "ator removido" : version.actorEmail}
        {version.revertsRevision > 0 && <> · reverteu a revisão {version.revertsRevision}</>}
      </p>
      {version.reason !== "" && <p className="admin-table__muted">{version.reason}</p>}
      <ul className="admin-version__changes">
        {version.changes.map((change) => (
          <li key={change.key}>
            {change.label}: {formatConfigValue(change.from, change.unit)} →{" "}
            {formatConfigValue(change.to, change.unit)}
          </li>
        ))}
      </ul>
      {canManage && version.rollbackable && (
        <button
          type="button"
          className="admin-button admin-button--ghost"
          disabled={busy}
          onClick={() => onRollback(version)}
        >
          Reverter
        </button>
      )}
      {!version.rollbackable && (
        <p className="admin-table__muted">Esta versão não pode ser revertida automaticamente.</p>
      )}
    </li>
  );
}
