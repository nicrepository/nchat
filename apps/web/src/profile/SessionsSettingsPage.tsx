/**
 * SessionsSettingsPage — `/profile/sessions` (issue #672 §4.3).
 *
 * Owns its own list fetch, loading/error/retry state, and the two confirm
 * dialogs — independent of the Profile/Notifications/Security tabs, per the
 * issue's "falha de uma seção não derruba as demais" requirement: a
 * `sessionsApi` failure here shows a retry button, it never throws past this
 * page's boundary.
 */

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";

import "./SessionsSettingsPage.css";
import { getSessionGeneration, onAuthChange } from "../lib/authSession";
import { listSessions, revokeAllOtherSessions, revokeSession, type Session } from "./sessionsApi";
import RevokeSessionDialog from "./RevokeSessionDialog";
import SessionRow from "./SessionRow";

type LoadState =
  | { status: "loading"; generation: number }
  | { status: "error"; generation: number }
  | { status: "ready"; generation: number; sessions: Session[] };
type ConfirmState =
  | { target: "single"; sessionId: string; generation: number }
  | { target: "others"; generation: number }
  | null;

export default function SessionsSettingsPage() {
  const sessionGeneration = useSyncExternalStore(onAuthChange, getSessionGeneration);
  const [state, setState] = useState<LoadState>({
    status: "loading",
    generation: sessionGeneration,
  });
  const [confirm, setConfirm] = useState<ConfirmState>(null);
  const mountedRef = useRef(true);
  const loadControllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const load = useCallback(() => {
    const generation = sessionGeneration;
    loadControllerRef.current?.abort();
    const controller = new AbortController();
    loadControllerRef.current = controller;
    listSessions(controller.signal)
      .then((sessions) => {
        if (
          controller.signal.aborted ||
          !mountedRef.current ||
          getSessionGeneration() !== generation
        )
          return;
        setState({ status: "ready", generation, sessions });
      })
      .catch(() => {
        if (
          controller.signal.aborted ||
          !mountedRef.current ||
          getSessionGeneration() !== generation
        )
          return;
        setState({ status: "error", generation });
      });
  }, [sessionGeneration]);

  useEffect(() => {
    load();
    return () => loadControllerRef.current?.abort();
  }, [load]);

  function retry() {
    setState({ status: "loading", generation: sessionGeneration });
    load();
  }

  async function handleConfirm() {
    if (!confirm || confirm.generation !== getSessionGeneration()) return;
    const generation = confirm.generation;
    if (confirm.target === "single") {
      await revokeSession(confirm.sessionId);
    } else {
      await revokeAllOtherSessions();
    }
    if (mountedRef.current && getSessionGeneration() === generation) {
      setState({ status: "loading", generation });
      load();
    }
  }

  const visibleState: LoadState =
    state.generation === sessionGeneration
      ? state
      : { status: "loading", generation: sessionGeneration };

  if (visibleState.status === "loading") {
    return (
      <div className="sessions-settings" role="status" aria-label="Carregando sessões">
        <span className="sessions-settings__loading" />
      </div>
    );
  }

  if (visibleState.status === "error") {
    return (
      <div className="sessions-settings sessions-settings__error">
        <p>Não foi possível carregar suas sessões.</p>
        <button type="button" onClick={retry}>
          Tentar novamente
        </button>
      </div>
    );
  }

  const { sessions } = visibleState;
  const hasOtherSessions = sessions.some((s) => !s.current);
  const visibleConfirm = confirm?.generation === sessionGeneration ? confirm : null;

  return (
    <div className="sessions-settings">
      <header className="sessions-settings__header">
        <h2 className="sessions-settings__title">Sessões</h2>
        {hasOtherSessions && (
          <button
            type="button"
            className="sessions-settings__revoke-all"
            onClick={() => setConfirm({ target: "others", generation: getSessionGeneration() })}
          >
            Revogar todas as outras
          </button>
        )}
      </header>
      <p className="sessions-settings__scope">
        Estas são sessões do NChat. Revogá-las encerra o acesso ao NChat, mas não encerra a sessão
        no provedor de identidade.
      </p>
      <ul className="sessions-settings__list">
        {sessions.map((session) => (
          <SessionRow
            key={session.id}
            session={session}
            onRevoke={(id) =>
              setConfirm({ target: "single", sessionId: id, generation: getSessionGeneration() })
            }
          />
        ))}
      </ul>
      {visibleConfirm && (
        <RevokeSessionDialog
          target={visibleConfirm.target}
          onClose={() => setConfirm(null)}
          onConfirm={handleConfirm}
        />
      )}
    </div>
  );
}
