import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";

import { getUser, grantAdminRole, revokeAdminRole, type AdminRole } from "../api/managementApi";
import ConfirmDialog from "../components/ConfirmDialog";
import QueryStates from "../components/QueryStates";
import { formatDateTime } from "../lib/units";
import { classify, useAdminQuery } from "../lib/useAdminQuery";
import { useAdminSession } from "../session/useAdminSession";

interface UserDetailDialogProps {
  userID: string;
  onClose: () => void;
  /** Called after a change the listing behind this dialog should reflect. */
  onChanged: () => void;
}

/**
 * One person's record: memberships, administrative roles and what each grants.
 *
 * It loads on open rather than travelling with every row of the directory,
 * because it costs aggregates a table of twenty people has no use for.
 *
 * Role management is the one operation here, and it is offered only to a
 * principal holding admin.superuser — conferring authority you do not hold is
 * escalation, so the endpoint requires all of it. The console hides the control
 * for anyone else; the API refuses them regardless.
 */
export default function UserDetailDialog({ userID, onClose, onChanged }: UserDetailDialogProps) {
  const { can, bootstrap } = useAdminSession();
  const canGrantRoles = can("admin.superuser");
  // Reading somebody's record does not imply reading the audit trail: the two
  // are separate capabilities, and the API enforces that whatever this shows.
  const canReadAudit = can("admin.audit.read");
  const isSelf = bootstrap?.identity.user_id === userID;
  const navigate = useNavigate();

  const closeRef = useRef<HTMLButtonElement>(null);
  const opener = useRef<Element | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [error, setError] = useState("");
  // One value rather than a pair of booleans: "which role, and in which
  // direction" is a single fact, and splitting it into flags that must agree is
  // how a dialog ends up confirming an action nobody chose.
  const [confirming, setConfirming] = useState<PendingRoleAction | null>(null);

  useEffect(() => {
    opener.current = document.activeElement;
    closeRef.current?.focus();
    return () => {
      if (opener.current instanceof HTMLElement) opener.current.focus();
    };
  }, []);

  const load = useCallback((signal: AbortSignal) => getUser(userID, signal), [userID]);
  const query = useAdminQuery(load);
  const detail = query.data;

  /**
   * Applies the confirmed role change.
   *
   * Nothing calls the API before this runs: the buttons open the confirmation
   * and only the confirmation submits. Changing who administers the platform is
   * the highest-impact operation this console offers, and a misplaced click
   * must not be enough to perform it.
   */
  async function applyRoleChange() {
    if (confirming === null || pending !== null) return;
    const { type, role } = confirming;
    setPending(role.slug);
    setError("");
    try {
      await (type === "revoke"
        ? revokeAdminRole(userID, role.slug)
        : grantAdminRole(userID, role.slug));
      setConfirming(null);
      query.reload();
      onChanged();
    } catch (failure) {
      setError(classify(failure).message);
      // The dialog closes on failure too: the error belongs to the record
      // behind it, and leaving a confirmation open over a message the operator
      // cannot read is worse than making them re-open it.
      setConfirming(null);
    } finally {
      setPending(null);
    }
  }

  return (
    <div className="admin-dialog-backdrop">
      <div
        className="admin-dialog admin-dialog--wide"
        role="dialog"
        aria-modal="true"
        aria-labelledby="admin-user-detail-title"
        onKeyDown={(event) => {
          if (event.key === "Escape" && pending === null) onClose();
        }}
      >
        <div className="admin-dialog__header">
          <h2 id="admin-user-detail-title">
            {detail === null ? "Detalhes do usuário" : detail.fullName || detail.displayName}
          </h2>
          <div className="admin-actions">
            {canReadAudit && (
              <button
                type="button"
                className="admin-button admin-button--ghost"
                disabled={pending !== null}
                onClick={() => {
                  // The identifier goes in the URL so the history survives a
                  // refresh and can be handed to a colleague. It names a filter,
                  // not a permission — the API re-checks the session and the
                  // capability on the request it produces.
                  onClose();
                  void navigate(`/audit?user=${encodeURIComponent(userID)}`);
                }}
              >
                Ver histórico de auditoria
              </button>
            )}
            <button
              type="button"
              className="admin-button admin-button--ghost"
              ref={closeRef}
              onClick={onClose}
              disabled={pending !== null}
            >
              Fechar
            </button>
          </div>
        </div>

        <QueryStates
          status={query.status}
          message={query.message}
          empty="Registro indisponível."
          isEmpty={false}
          onRetry={query.reload}
        />

        {error !== "" && (
          <p role="alert" className="admin-alert">
            {error}
          </p>
        )}

        {query.status === "ready" && detail !== null && (
          <>
            <dl className="admin-definitions">
              <dt>E-mail</dt>
              <dd>{detail.email}</dd>
              <dt>Origem da identidade</dt>
              <dd>
                {detail.identityManagedExternally
                  ? `Gerenciado pelo ${detail.externalProvider || "provedor de identidade"}`
                  : "Local (NChat)"}
              </dd>
              <dt>Status</dt>
              <dd>{detail.status}</dd>
              <dt>Criado em</dt>
              <dd>{formatDateTime(detail.createdAt)}</dd>
              <dt>Último acesso</dt>
              <dd>{formatDateTime(detail.lastLoginAt)}</dd>
              <dt>Sessões ativas</dt>
              <dd>{detail.activeSessions}</dd>
              <dt>Canais que integra</dt>
              <dd>{detail.channelCount}</dd>
            </dl>

            {detail.identityManagedExternally && (
              <p className="admin-notice">
                Os dados de identidade desta conta pertencem ao provedor externo. O NChat não altera
                senha nem atributos do IdP; desativar aqui bloqueia o acesso ao NChat e não desativa
                a conta no provedor.
              </p>
            )}

            <h3>Workspaces</h3>
            {detail.memberships.length === 0 ? (
              <p className="admin-empty">Não participa de nenhum workspace.</p>
            ) : (
              <div className="admin-table-scroll">
                <table className="admin-table">
                  <caption className="admin-visually-hidden">Workspaces do usuário</caption>
                  <thead>
                    <tr>
                      <th scope="col">Workspace</th>
                      <th scope="col">Papel</th>
                      <th scope="col">Situação</th>
                      <th scope="col">Desde</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.memberships.map((membership) => (
                      <tr key={membership.workspaceId}>
                        <th scope="row">{membership.workspaceName}</th>
                        <td>{membership.role}</td>
                        <td>{membership.status}</td>
                        <td>{formatDateTime(membership.joinedAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}

            <h3>Papéis administrativos de plataforma</h3>
            {!canGrantRoles && (
              <p className="admin-notice">
                Somente um administrador com <code>admin.superuser</code> pode conceder ou remover
                papéis administrativos.
              </p>
            )}
            <ul className="admin-role-list">
              {detail.availableRoles.map((role) => {
                const grant = detail.roleGrants.find((held) => held.slug === role.slug);
                const held = grant !== undefined;
                return (
                  <li key={role.slug} className="admin-role">
                    <div>
                      <p className="admin-role__name">
                        <code>{role.slug}</code> — {role.description}
                      </p>
                      <p className="admin-table__muted">{role.capabilities.join(", ")}</p>
                      {grant !== undefined && (
                        <p className="admin-table__muted">
                          Concedido em {formatDateTime(grant.grantedAt)}
                          {grant.grantedBy ? ` por ${grant.grantedBy}` : ""}
                        </p>
                      )}
                    </div>
                    <div className="admin-role__action">
                      <span className={held ? "admin-badge admin-badge--strong" : "admin-badge"}>
                        {held ? "Concedido" : "Não concedido"}
                      </span>
                      {canGrantRoles && !isSelf && (
                        <button
                          type="button"
                          className="admin-button admin-button--ghost"
                          disabled={pending !== null}
                          onClick={() => setConfirming({ type: held ? "revoke" : "grant", role })}
                        >
                          {pending === role.slug ? "Aplicando…" : held ? "Remover" : "Conceder"}
                        </button>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
            {canGrantRoles && isSelf && (
              <p className="admin-notice">
                Um administrador não altera os próprios papéis. A API recusa a operação.
              </p>
            )}
          </>
        )}

        {confirming !== null && detail !== null && (
          <ConfirmDialog
            title={
              confirming.type === "grant"
                ? "Conceder este papel administrativo?"
                : "Remover este papel administrativo?"
            }
            description={roleDescription(confirming, detail.fullName || detail.displayName)}
            impact={roleImpact(confirming)}
            confirmLabel={confirming.type === "grant" ? "Conceder" : "Remover"}
            pending={pending !== null}
            onConfirm={() => void applyRoleChange()}
            onCancel={() => setConfirming(null)}
          />
        )}
      </div>
    </div>
  );
}

/** Which role is about to change, and in which direction. */
type PendingRoleAction = { type: "grant"; role: AdminRole } | { type: "revoke"; role: AdminRole };

function roleDescription(action: PendingRoleAction, who: string): string {
  return action.type === "grant"
    ? `${who} passará a administrar a plataforma com o papel ${action.role.slug}.`
    : `${who} deixará de ter o papel ${action.role.slug}.`;
}

function roleImpact(action: PendingRoleAction): string {
  const capabilities = action.role.capabilities.join(", ") || "nenhuma capability";
  return action.type === "grant"
    ? `Concede: ${capabilities}. Vale na requisição seguinte, sem novo login.`
    : `Retira: ${capabilities}. Vale imediatamente, e a API recusa a remoção se ela deixar a plataforma sem administrador.`;
}
