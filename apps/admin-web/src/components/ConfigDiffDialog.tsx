import { useEffect, useRef, useState } from "react";

import type { ConfigPlan } from "../api/configApi";
import { confirmBlocked } from "../lib/configFields";
import { ConfigDiffList, ConfigPlanAlerts } from "./ConfigDiff";

interface ConfigDiffDialogProps {
  title: string;
  plan: ConfigPlan;
  confirmLabel: string;
  pending: boolean;
  /** A message from a failed attempt, kept visible so the dialog can be retried. */
  failure: string | null;
  onConfirm: (reason: string) => void;
  onCancel: () => void;
}

/**
 * The review step: what will change, what it affects, and what it costs.
 *
 * The diff is the server's, not the form's. It is produced by the same
 * pipeline that would perform the write, against the state that exists right
 * now, so what an operator confirms is what the server would do — a diff
 * computed here from the values the form happens to hold could disagree with
 * both.
 *
 * A dialog in the accessibility sense as well as the visual one: labelled and
 * described by its own content, focus moved in on open and returned on close,
 * Escape cancels. Same contract as ConfirmDialog, with the two things a
 * configuration change needs and a confirmation does not: a typed diff and a
 * reason.
 */
export default function ConfigDiffDialog({
  title,
  plan,
  confirmLabel,
  pending,
  failure,
  onConfirm,
  onCancel,
}: ConfigDiffDialogProps) {
  const confirmRef = useRef<HTMLButtonElement>(null);
  const opener = useRef<Element | null>(null);
  const [reason, setReason] = useState("");

  useEffect(() => {
    opener.current = document.activeElement;
    confirmRef.current?.focus();
    return () => {
      if (opener.current instanceof HTMLElement) opener.current.focus();
    };
  }, []);

  return (
    <div className="admin-dialog-backdrop">
      <div
        className="admin-dialog admin-dialog--wide"
        role="dialog"
        aria-modal="true"
        aria-labelledby="config-diff-title"
        aria-describedby="config-diff-body"
        onKeyDown={(event) => {
          if (event.key === "Escape" && !pending) onCancel();
        }}
      >
        <h2 id="config-diff-title">{title}</h2>
        <div id="config-diff-body">
          <ConfigDiffList changes={plan.changes} />

          {plan.affectedServices.length > 0 && (
            <p className="admin-table__muted">
              Serviços afetados: {plan.affectedServices.join(", ")}.
            </p>
          )}

          <ConfigPlanAlerts plan={plan} />

          {plan.reasonRequired && (
            <label className="admin-field" htmlFor="config-reason">
              <span>Motivo (obrigatório para alterações sensíveis)</span>
              <textarea
                id="config-reason"
                value={reason}
                disabled={pending}
                onChange={(event) => setReason(event.target.value)}
              />
            </label>
          )}

          {failure !== null && (
            <p role="alert" className="admin-alert" data-testid="config-apply-error">
              {failure}
            </p>
          )}
        </div>
        <div className="admin-dialog__actions">
          <button
            type="button"
            className="admin-button admin-button--ghost"
            onClick={onCancel}
            disabled={pending}
          >
            Cancelar
          </button>
          <button
            type="button"
            className="admin-button admin-button--danger"
            ref={confirmRef}
            onClick={() => onConfirm(reason.trim())}
            disabled={confirmBlocked(plan, pending, reason)}
          >
            {pending ? "Aplicando…" : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
