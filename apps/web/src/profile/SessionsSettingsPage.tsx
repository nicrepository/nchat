/**
 * SessionsSettingsPage — `/profile/sessions` (issue #672 §4.3).
 *
 * Owns its own list fetch, loading/error/retry state, and the two confirm
 * dialogs — independent of the Profile/Notifications/Security tabs, per the
 * issue's "falha de uma seção não derruba as demais" requirement: a
 * `sessionsApi` failure here shows a retry button, it never throws past this
 * page's boundary.
 */

import { useCallback, useEffect, useRef, useState } from "react";

import "./SessionsSettingsPage.css";
import { listSessions, revokeAllOtherSessions, revokeSession, type Session } from "./sessionsApi";
import RevokeSessionDialog from "./RevokeSessionDialog";
import SessionRow from "./SessionRow";

type LoadState = { status: "loading" } | { status: "error" } | { status: "ready"; sessions: Session[] };
type ConfirmState = { target: "single"; sessionId: string } | { target: "others" } | null;

export default function SessionsSettingsPage() {
  const [state, setState] = useState<LoadState>({ status: "loading" });
  const [confirm, setConfirm] = useState<ConfirmState>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const load = useCallback((signal?: AbortSignal) => {
    setState({ status: "loading" });
    listSessions(signal)
      .then((sessions) => {
        if (signal?.aborted || !mountedRef.current) return;
        setState({ status: "ready", sessions });
      })
      .catch(() => {
        if (signal?.aborted || !mountedRef.current) return;
        setState({ status: "error" });
      });
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    return () => controller.abort();
  }, [load]);

  async function handleConfirm() {
    if (!confirm) return;
    if (confirm.target === "single") {
      await revokeSession(confirm.sessionId);
    } else {
      await revokeAllOtherSessions();
    }
    if (mountedRef.current) load();
  }

  if (state.status === "loading") {
    return (
      <div className="sessions-settings" role="status" aria-label="Carregando sessões">
        <span className="sessions-settings__loading" />
      </div>
    );
  }

  if (state.status === "error") {
    return (
      <div className="sessions-settings sessions-settings__error">
        <p>Não foi possível carregar suas sessões.</p>
        <button type="button" onClick={() => load()}>
          Tentar novamente
        </button>
      </div>
    );
  }

  const { sessions } = state;
  const hasOtherSessions = sessions.some((s) => !s.current);

  return (
    <div className="sessions-settings">
      <header className="sessions-settings__header">
        <h1 className="sessions-settings__title">Sessões</h1>
        {hasOtherSessions && (
          <button
            type="button"
            className="sessions-settings__revoke-all"
            onClick={() => setConfirm({ target: "others" })}
          >
            Revogar todas as outras
          </button>
        )}
      </header>
      <ul className="sessions-settings__list">
        {sessions.map((session) => (
          <SessionRow
            key={session.id}
            session={session}
            onRevoke={(id) => setConfirm({ target: "single", sessionId: id })}
          />
        ))}
      </ul>
      {confirm && (
        <RevokeSessionDialog target={confirm.target} onClose={() => setConfirm(null)} onConfirm={handleConfirm} />
      )}
    </div>
  );
}
