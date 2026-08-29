/**
 * SessionRow — pure display of one session (issue #672 §4.3).
 *
 * The current session gets a badge, never a revoke control: the button is
 * entirely absent (not disabled) for it — that's what app logout is for.
 */

import "./SessionRow.css";
import type { Session } from "./sessionsApi";

export default function SessionRow({
  session,
  onRevoke,
}: {
  session: Session;
  onRevoke: (id: string) => void;
}) {
  return (
    <li className="session-row" data-testid="session-row">
      <div className="session-row__info">
        <p className="session-row__agent">{session.userAgent || "Dispositivo desconhecido"}</p>
        <p className="session-row__meta">
          {session.ipAddress && (
            <span>
              <span>{session.ipAddress}</span> (aproximado)
            </span>
          )}
          <span>
            {session.current
              ? "Ativa agora"
              : `Última atividade em ${new Date(session.lastSeenAt).toLocaleString("pt-BR")}`}
          </span>
        </p>
      </div>
      {session.current ? (
        <span className="session-row__current-badge">Sessão atual</span>
      ) : (
        <button type="button" className="session-row__revoke" onClick={() => onRevoke(session.id)}>
          Revogar sessão
        </button>
      )}
    </li>
  );
}
