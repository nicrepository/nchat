import { useEffect, useId, useState } from "react";

import "./AdminAntiSpamPage.css";
import AdminShell from "./AdminShell";
import {
  type AntiSpamPolicy,
  fetchAntiSpamPolicy,
  fetchCurrentWorkspaceId,
  updateAntiSpamPolicy,
} from "./adminAntiSpamApi";
import { validateLimit } from "./antiSpamForm";

/**
 * RF-19 (issue #419): the workspace anti-spam limit.
 *
 * The bounds shown and validated against are the ones the server returned,
 * never numbers restated in the frontend.
 */

type PageState =
  | { kind: "loading" }
  | { kind: "error" }
  | { kind: "ready"; policy: AntiSpamPolicy };

type Feedback = { kind: "error" | "success"; text: string } | null;

export default function AdminAntiSpamPage() {
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
      .then(fetchAntiSpamPolicy)
      .then((policy) => {
        if (cancelled) return;
        setState({ kind: "ready", policy });
        setValue(String(policy.messagesPerMinute));
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

    const validationError = validateLimit(value, state.policy.min, state.policy.max);
    if (validationError) {
      setFeedback({ kind: "error", text: validationError });
      return;
    }

    setSaving(true);
    setFeedback(null);
    try {
      const updated = await updateAntiSpamPolicy(state.policy.workspaceId, Number(value.trim()));
      setState({ kind: "ready", policy: updated });
      setValue(String(updated.messagesPerMinute));
      setFeedback({ kind: "success", text: "Limite atualizado." });
    } catch {
      // The server's message is not surfaced verbatim: it may carry detail that
      // does not belong in the UI.
      setFeedback({ kind: "error", text: "Não foi possível salvar o limite. Tente novamente." });
    } finally {
      setSaving(false);
    }
  }

  const invalid = feedback?.kind === "error";

  return (
    <AdminShell activeTab="anti-spam">
      <div className="admin-antispam__page-head">
        <h1 className="admin-antispam__title">Anti-spam</h1>
        <p className="admin-antispam__subtitle">
          Limite quantas mensagens cada usuário pode enviar no workspace.
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
            Não foi possível carregar a configuração de anti-spam.
          </p>
        )}

        {state.kind === "ready" && (
          <form onSubmit={handleSubmit} noValidate>
            <div className="admin-antispam__field">
              <label className="admin-antispam__label" htmlFor={inputId}>
                Mensagens por usuário por minuto
              </label>
              <input
                id={inputId}
                className="admin-antispam__input"
                type="number"
                inputMode="numeric"
                min={state.policy.min}
                max={state.policy.max}
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
                Entre {state.policy.min} e {state.policy.max} mensagens por minuto. Vale para canais
                e mensagens diretas somados, por usuário.
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
