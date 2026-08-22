import type { ConfigSetting } from "../api/configApi";
import { applyLabel } from "../lib/configFields";
import type { ConfigDrafts } from "../lib/useConfigDrafts";
import ConfigField from "./ConfigField";

interface ConfigPolicyFormProps {
  /** The editable settings of the document, in registry order. */
  settings: ConfigSetting[];
  drafts: ConfigDrafts;
  revision: number;
  canManage: boolean;
  applying: boolean;
  /** Opens the review. It never writes: the server computes the diff first. */
  onReview: () => void;
}

/**
 * The one part of the configuration the console can actually write.
 *
 * Submitting does not save. It asks the server what the drafts would change
 * against the state that exists now, and the answer is what the operator
 * confirms — which is why the button says "revisar" and not "salvar".
 *
 * Hiding the buttons for an operator without the manage capability is
 * navigation, not authorization: the API refuses the same request either way,
 * and the notice says which permission is missing rather than leaving a dead
 * form on screen.
 */
export default function ConfigPolicyForm({
  settings,
  drafts,
  revision,
  canManage,
  applying,
  onReview,
}: ConfigPolicyFormProps) {
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        onReview();
      }}
    >
      <h2>Política de autenticação</h2>
      <p className="admin-table__muted">
        Revisão atual: {revision}. {applyLabel("runtime")}.
      </p>
      <ul className="admin-config-list">
        {settings.map((setting) => (
          <ConfigField
            key={setting.key}
            setting={setting}
            draft={drafts.drafts[setting.key] ?? ""}
            editable={canManage}
            disabled={applying}
            onChange={(value) => drafts.edit(setting.key, value)}
          />
        ))}
      </ul>

      {canManage ? (
        <ConfigFormActions drafts={drafts} applying={applying} />
      ) : (
        <p className="admin-notice">
          Somente leitura: falta a permissão <code>admin.config.manage</code>.
        </p>
      )}
    </form>
  );
}

function ConfigFormActions({ drafts, applying }: { drafts: ConfigDrafts; applying: boolean }) {
  const busy = applying || !drafts.dirty;
  return (
    <div className="admin-actions">
      <button type="submit" className="admin-button" disabled={busy || drafts.invalid}>
        Revisar alterações
      </button>
      <button
        type="button"
        className="admin-button admin-button--ghost"
        disabled={busy}
        onClick={drafts.discard}
      >
        Descartar
      </button>
      {drafts.dirty && (
        <span className="admin-table__muted" data-testid="config-dirty">
          Há alterações não salvas.
        </span>
      )}
    </div>
  );
}
