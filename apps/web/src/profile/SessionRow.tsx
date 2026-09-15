/**
 * SessionRow — pure display of one session (issues #672 §4.3, #859).
 *
 * Laid out as the prototype's device row (sessoes.html): icon, a one-line
 * "browser · platform" identity, then metadata. Only data the API really has
 * is shown — no location line, because there is no source of truth for it.
 *
 * The current session gets a badge, never a revoke control: the button is
 * entirely absent (not disabled) for it — that's what app logout is for.
 */

import "./SessionRow.css";
import type { Session } from "./sessionsApi";
import { describeUserAgent } from "./userAgentLabel";

/**
 * Material Symbols ligature for the platform label describeUserAgent returned.
 * An unidentified platform gets the neutral "devices", never a guessed desktop.
 */
function deviceIcon(platform: string): string {
  if (platform === "Android" || platform === "iOS") return "smartphone";
  if (platform === "iPadOS") return "tablet";
  return platform ? "computer" : "devices";
}

export default function SessionRow({
  session,
  onRevoke,
}: {
  session: Session;
  onRevoke: (id: string) => void;
}) {
  const { browser, platform } = describeUserAgent(session.userAgent);
  return (
    <li className="session-row" data-testid="session-row">
      <span
        className={`session-row__icon${session.current ? " session-row__icon--current" : ""}`}
        aria-hidden="true"
      >
        <span className="material-symbols-outlined">{deviceIcon(platform)}</span>
      </span>
      <div className="session-row__info">
        <p className="session-row__name">
          <span>{browser}</span>
          {platform && (
            <>
              <span aria-hidden="true"> · </span>
              <span>{platform}</span>
            </>
          )}
          {session.current && <span className="session-row__current-badge">Sessão atual</span>}
        </p>
        <p className="session-row__meta">
          {session.current ? (
            <span>Ativa agora</span>
          ) : (
            <span>
              Último acesso em{" "}
              <time dateTime={session.lastSeenAt}>
                {new Date(session.lastSeenAt).toLocaleString("pt-BR")}
              </time>
            </span>
          )}
          {session.ipAddress && (
            <>
              <span className="session-row__dot" aria-hidden="true">
                ·
              </span>
              <span>
                IP <span>{session.ipAddress}</span> (aproximado)
              </span>
            </>
          )}
        </p>
      </div>
      {!session.current && (
        <button type="button" className="session-row__revoke" onClick={() => onRevoke(session.id)}>
          Revogar sessão
        </button>
      )}
    </li>
  );
}
