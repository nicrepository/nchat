import { useEffect, useState } from "react";

import { listAuditEvents, type AuditEvent } from "../api/adminApi";
import { AdminApiError } from "../api/client";

type LoadState = "loading" | "ready" | "forbidden" | "error";

/**
 * The administrative audit trail.
 *
 * This is the console's proof that capability enforcement is real rather than
 * decorative: an administrator without `admin.audit.read` never sees the entry
 * in the sidebar, and if they reach the page anyway the API answers 403 and the
 * page says so.
 */
export default function AuditPage() {
  const [state, setState] = useState<LoadState>("loading");
  const [events, setEvents] = useState<AuditEvent[]>([]);

  useEffect(() => {
    let cancelled = false;
    listAuditEvents(50)
      .then((loaded) => {
        if (cancelled) return;
        setEvents(loaded);
        setState("ready");
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        setState(error instanceof AdminApiError && error.status === 403 ? "forbidden" : "error");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <section aria-labelledby="admin-audit-title">
      <h1 id="admin-audit-title">Auditoria</h1>

      {state === "loading" && <p role="status">Carregando eventos…</p>}
      {state === "forbidden" && (
        <p role="alert">Você não tem permissão para consultar a auditoria.</p>
      )}
      {state === "error" && <p role="alert">Não foi possível carregar os eventos.</p>}

      {state === "ready" &&
        (events.length === 0 ? (
          <p>Nenhum evento administrativo registrado ainda.</p>
        ) : (
          <div className="admin-table-scroll">
            <table className="admin-table">
              <caption className="admin-visually-hidden">
                Eventos administrativos mais recentes
              </caption>
              <thead>
                <tr>
                  <th scope="col">Quando</th>
                  <th scope="col">Ator</th>
                  <th scope="col">Ação</th>
                  <th scope="col">Recurso</th>
                  <th scope="col">Resultado</th>
                </tr>
              </thead>
              <tbody>
                {events.map((event) => (
                  <tr key={event.id}>
                    <td>{new Date(event.occurred_at).toLocaleString("pt-BR")}</td>
                    <td>{event.actor_email || "—"}</td>
                    <td>
                      <code>{event.action}</code>
                    </td>
                    <td>{event.resource || "—"}</td>
                    <td>
                      {/* The result is spelled out, not signalled by colour alone. */}
                      <span className={`admin-result admin-result--${event.result}`}>
                        {event.result}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
    </section>
  );
}
