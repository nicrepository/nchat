import type { ConfigDiffEntry, ConfigPlan } from "../api/configApi";
import { applyLabel, formatConfigValue } from "../lib/configFields";

/**
 * The two halves of a review, as the server computed them: what would change,
 * and why it might not be allowed to.
 *
 * They live beside the dialog rather than inside it because the dialog's own
 * job is the modal contract — focus, Escape, the confirm button — and reading
 * that contract should not mean scrolling past the rendering of every field in
 * a change set.
 */

/** One field's transition, with the impact the registry attaches to it. */
function ConfigDiffRow({ change }: { change: ConfigDiffEntry }) {
  return (
    <li className="admin-diff__entry">
      <p className="admin-diff__label">
        {change.label} <code className="admin-table__muted">{change.key}</code>
      </p>
      <p className="admin-diff__transition">
        <span className="admin-diff__from">− {formatConfigValue(change.from, change.unit)}</span>
        <span className="admin-diff__to">+ {formatConfigValue(change.to, change.unit)}</span>
      </p>
      <p className="admin-table__muted">
        {change.ownerService} · {applyLabel(change.apply)}
      </p>
      {change.dangerous && (
        <p className="admin-warning" data-testid={`config-danger-${change.key}`}>
          <strong>Atenção:</strong> {change.dangerNote}
        </p>
      )}
    </li>
  );
}

/**
 * The change set, or the statement that there is not one.
 *
 * An empty diff is a real answer and is shown as one: it means the values
 * submitted are already the stored values, which is what a resubmitted form
 * produces.
 */
export function ConfigDiffList({ changes }: { changes: ConfigDiffEntry[] }) {
  if (changes.length === 0) {
    return <p className="admin-empty">Nada a alterar: os valores enviados já são os atuais.</p>;
  }
  return (
    <ul className="admin-diff" data-testid="config-diff">
      {changes.map((change) => (
        <ConfigDiffRow key={change.key} change={change} />
      ))}
    </ul>
  );
}

/**
 * Everything standing between this plan and being applied.
 *
 * Each is a different fact and gets its own message: a value the registry
 * refused, a version that has been superseded, a document that moved under the
 * operator, and a capability they do not hold. Collapsing them into one "não
 * foi possível" would leave the person reading it with no idea which to act on
 * — reload, pick a newer version, or ask someone else.
 *
 * They are independent conditions rather than a chain of branches, because
 * more than one can be true at once and the operator is owed all of them.
 */
export function ConfigPlanAlerts({ plan }: { plan: ConfigPlan }) {
  return (
    <>
      {plan.errors.length > 0 && (
        <ul className="admin-alert" role="alert" data-testid="config-plan-errors">
          {plan.errors.map((failure) => (
            <li key={failure.key}>
              {failure.key}: {failure.message}
            </li>
          ))}
        </ul>
      )}

      {plan.superseded && (
        <p role="alert" className="admin-alert" data-testid="config-superseded">
          Esta versão não pode mais ser revertida: a configuração foi alterada depois dela.
          Recarregue o estado atual e reverta a partir de uma versão mais recente.
        </p>
      )}

      {plan.stale && (
        <p role="alert" className="admin-alert" data-testid="config-conflict">
          Outra pessoa alterou esta configuração enquanto o formulário estava aberto (revisão atual:{" "}
          {plan.revision}). Recarregue para revisar a diferença contra o estado atual.
        </p>
      )}

      {!plan.authorized && (
        <p role="alert" className="admin-alert" data-testid="config-unauthorized">
          Esta alteração exige a permissão <code>{plan.requiredCapability}</code>.
        </p>
      )}
    </>
  );
}
