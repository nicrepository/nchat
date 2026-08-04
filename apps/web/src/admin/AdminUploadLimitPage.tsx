import { useEffect, useId, useState } from "react";

// The anti-spam stylesheet is reused rather than copied: this page is the same
// card, label, number field, hint and save button, and a second file of
// identical rules would only be one more place for them to drift.
import "./AdminAntiSpamPage.css";
import AdminShell from "./AdminShell";
import { fetchCurrentWorkspaceId } from "./adminAntiSpamApi";
import {
  type UploadLimitPolicy,
  fetchUploadLimitPolicy,
  updateUploadLimitPolicy,
} from "./adminUploadLimitApi";
import {
  bytesToMiB,
  isEditablePolicy,
  mibToBytes,
  validateUploadLimitMiB,
} from "./uploadLimitForm";

/**
 * RF-32 (issue #458): the workspace attachment size limit.
 *
 * The field is in MiB because that is the unit the policy is defined in; the
 * API speaks bytes, and the conversion is an exact division that happens at
 * this edge only. The bounds shown and validated against are the ones the
 * server returned, never numbers restated in the frontend.
 *
 * Nothing here rounds. A stored value that is not a whole number of MiB cannot
 * be represented in the field, so the page enters "unrepresentable" and refuses
 * to save rather than letting an ordinary submit overwrite a limit the
 * administrator never edited.
 */

type PageState =
  | { kind: "loading" }
  | { kind: "error" }
  | { kind: "ready"; policy: UploadLimitPolicy }
  | { kind: "unrepresentable"; policy: UploadLimitPolicy };

type Feedback = { kind: "error" | "success"; text: string } | null;

export default function AdminUploadLimitPage() {
  const [state, setState] = useState<PageState>({ kind: "loading" });
  const [value, setValue] = useState("");
  const [feedback, setFeedback] = useState<Feedback>(null);
  const [saving, setSaving] = useState(false);
  const inputId = useId();
  const helpId = useId();
  const feedbackId = useId();

  useEffect(() => {
    let cancelled = false;

    fetchCurrentWorkspaceId()
      .then(fetchUploadLimitPolicy)
      .then((policy) => {
        if (cancelled) return;
        if (!isEditablePolicy(policy.maxUploadBytes)) {
          // The original value is kept in state so the operator can see exactly
          // what is stored, and the form is not rendered at all — there is no
          // submit path that could round it away.
          setState({ kind: "unrepresentable", policy });
          return;
        }
        setState({ kind: "ready", policy });
        setValue(String(bytesToMiB(policy.maxUploadBytes)));
      })
      .catch(() => {
        if (!cancelled) setState({ kind: "error" });
      });

    return () => {
      cancelled = true;
    };
  }, []);

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    if (state.kind !== "ready" || saving) return;

    const validationError = validateUploadLimitMiB(value, state.policy.min, state.policy.max);
    if (validationError) {
      setFeedback({ kind: "error", text: validationError });
      return;
    }

    setSaving(true);
    setFeedback(null);
    try {
      const updated = await updateUploadLimitPolicy(
        state.policy.workspaceId,
        mibToBytes(Number(value.trim())),
      );
      if (!isEditablePolicy(updated.maxUploadBytes)) {
        setState({ kind: "unrepresentable", policy: updated });
        return;
      }
      setState({ kind: "ready", policy: updated });
      setValue(String(bytesToMiB(updated.maxUploadBytes)));
      setFeedback({ kind: "success", text: "Limite atualizado." });
    } catch {
      // The server's message is not surfaced verbatim: it may carry detail that
      // does not belong in the UI.
      setFeedback({ kind: "error", text: "Não foi possível salvar o limite. Tente novamente." });
    } finally {
      // Restored on every path, so a failed save leaves the form usable again.
      setSaving(false);
    }
  }

  const invalid = feedback?.kind === "error";

  return (
    <AdminShell activeTab="upload-limit">
      <div className="admin-antispam__page-head">
        <h1 className="admin-antispam__title">Limite de upload</h1>
        <p className="admin-antispam__subtitle">
          Defina o tamanho máximo de cada arquivo enviado no workspace.
        </p>
      </div>

      <div className="admin-antispam__card">
        {state.kind === "loading" && (
          <div aria-busy="true" aria-live="polite">
            <span className="admin-antispam__label">Carregando configuração…</span>
            <div className="admin-antispam__skeleton" />
          </div>
        )}

        {state.kind === "error" && (
          <p className="admin-antispam__message admin-antispam__message--error" role="alert">
            Não foi possível carregar o limite de upload.
          </p>
        )}

        {state.kind === "unrepresentable" && (
          <p className="admin-antispam__message admin-antispam__message--error" role="alert">
            Configuração inválida: o limite atual é de {state.policy.maxUploadBytes} bytes, que não
            é um número inteiro de MiB. Corrija o valor no banco antes de editá-lo aqui.
          </p>
        )}

        {state.kind === "ready" && (
          <form onSubmit={handleSubmit} noValidate>
            <div className="admin-antispam__field">
              <label className="admin-antispam__label" htmlFor={inputId}>
                Limite máximo por arquivo (MiB)
              </label>
              <input
                id={inputId}
                className="admin-antispam__input"
                type="number"
                inputMode="numeric"
                min={bytesToMiB(state.policy.min)}
                max={bytesToMiB(state.policy.max)}
                step={1}
                value={value}
                onChange={(e) => {
                  setValue(e.target.value);
                  setFeedback(null);
                }}
                disabled={saving}
                aria-describedby={feedback ? `${helpId} ${feedbackId}` : helpId}
                aria-invalid={invalid || undefined}
              />
              <p className="admin-antispam__hint" id={helpId}>
                O limite deve ser um número inteiro entre {bytesToMiB(state.policy.min)} e{" "}
                {bytesToMiB(state.policy.max)} MiB. Vale para anexos de canais e de mensagens
                diretas.
              </p>
            </div>

            <div className="admin-antispam__actions">
              <button className="admin-antispam__submit" type="submit" disabled={saving}>
                {saving ? "Salvando…" : "Salvar"}
              </button>
              {feedback && (
                <p
                  id={feedbackId}
                  className={`admin-antispam__message admin-antispam__message--${feedback.kind}`}
                  role={feedback.kind === "error" ? "alert" : "status"}
                >
                  {feedback.text}
                </p>
              )}
            </div>
          </form>
        )}
      </div>
    </AdminShell>
  );
}
