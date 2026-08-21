import { useCallback, useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router";

import { listAuditEvents } from "../api/adminApi";
import { getUser } from "../api/managementApi";
import QueryStates from "../components/QueryStates";
import { formatDateTime } from "../lib/units";
import { useAdminQuery } from "../lib/useAdminQuery";
import { useAdminSession } from "../session/useAdminSession";

const PAGE_SIZE = 50;

/** A well-formed identifier, so a hand-edited URL cannot become a request. */
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * The administrative audit trail, platform-wide or narrowed to one person.
 *
 * This is the console's proof that capability enforcement is real rather than
 * decorative: an administrator without `admin.audit.read` never sees the entry
 * in the sidebar, and if they reach the page anyway the API answers 403 and the
 * page says so. Narrowing changes nothing about that — the filtered read
 * carries exactly the same guard, because being able to open somebody's record
 * is not being able to read the trail.
 *
 * The filter lives in the URL so it survives a refresh and can be handed to a
 * colleague. The identifier there is not a secret and authorizes nothing: the
 * API re-checks the session and the capability on every request, and the value
 * is validated before it is used.
 */
export default function AuditPage() {
  const [params, setParams] = useSearchParams();
  const { can } = useAdminSession();
  const raw = params.get("user") ?? "";
  const userID = UUID.test(raw) ? raw : "";

  const load = useCallback(
    (signal: AbortSignal) => listAuditEvents(PAGE_SIZE, userID || undefined, signal),
    [userID],
  );
  const query = useAdminQuery(load);
  const events = query.data ?? [];

  const subject = useSubjectName(userID, can("admin.users.read"));

  // A URL naming something that is not an identifier is a broken link, not a
  // filter. Dropping it keeps the page from asking the API a question it would
  // refuse.
  useEffect(() => {
    if (raw !== "" && userID === "") {
      setParams({}, { replace: true });
    }
  }, [raw, userID, setParams]);

  return (
    <section aria-labelledby="admin-audit-title">
      <h1 id="admin-audit-title">
        {userID === "" ? "Auditoria" : `Auditoria — ${subject ?? "usuário selecionado"}`}
      </h1>

      {userID !== "" && (
        <p className="admin-notice" data-testid="admin-audit-filter">
          Mostrando apenas eventos administrativos realizados sobre{" "}
          <strong>{subject ?? "esta conta"}</strong>. <Link to="/audit">Ver toda a auditoria</Link>
        </p>
      )}

      <QueryStates
        status={query.status}
        message={query.message}
        empty={
          userID === ""
            ? "Nenhum evento administrativo registrado ainda."
            : "Nenhum evento administrativo registrado para esta conta."
        }
        isEmpty={events.length === 0}
        onRetry={query.reload}
        skeletonRows={5}
      />

      {query.status === "ready" && events.length > 0 && (
        <div className="admin-table-scroll">
          <table className="admin-table">
            <caption className="admin-visually-hidden">
              {userID === ""
                ? "Eventos administrativos mais recentes"
                : "Eventos administrativos desta conta"}
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
                  <td>{formatDateTime(event.occurred_at)}</td>
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
      )}
    </section>
  );
}

/**
 * Resolves the filtered account's name, for the heading only.
 *
 * Deliberately independent of the trail: the events exist whether or not the
 * name can be read. An auditor holding only `admin.audit.read` cannot query the
 * directory, and a soft-deleted account has no record left — in both cases the
 * history is still the answer to the operator's question, so a failed lookup
 * falls back to a neutral label instead of failing the page.
 */
function useSubjectName(userID: string, canReadUsers: boolean): string | null {
  const [subject, setSubject] = useState<{ id: string; name: string } | null>(null);

  useEffect(() => {
    if (userID === "" || !canReadUsers) return;
    const controller = new AbortController();
    let cancelled = false;
    getUser(userID, controller.signal)
      .then((user) => {
        if (cancelled) return;
        setSubject({ id: userID, name: user.fullName || user.displayName || user.email });
      })
      .catch(() => {
        // No name, no problem: the heading says "usuário selecionado" and the
        // trail below is unaffected.
      });
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [userID, canReadUsers]);

  // Tied to the id it was resolved for, so a name never survives onto a
  // different person's history.
  return subject !== null && subject.id === userID ? subject.name : null;
}
