import { useCallback, useState } from "react";

import type { UserStatusAction } from "../lib/userStatus";
import {
  listUsers,
  revokeUserSessions,
  updateUserStatus,
  type AdminUser,
  type UserFilters,
} from "../api/managementApi";
import ConfirmDialog from "../components/ConfirmDialog";
import Pagination from "../components/Pagination";
import QueryStates from "../components/QueryStates";
import { formatDateTime } from "../lib/units";
import { noStatusActionReason, userStatusAction } from "../lib/userStatus";
import { classify, useAdminQuery } from "../lib/useAdminQuery";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import { useAdminSession } from "../session/useAdminSession";
import UserDetailDialog from "./UserDetailDialog";

const PAGE_SIZE = 25;

/**
 * The filter values the server accepts.
 *
 * They are restated here because they are the *labels* the console shows; the
 * values are the server's allowlist keys, and an option the server does not
 * know would come back as a 400 rather than as a filter that matches nothing.
 */
const STATUS_OPTIONS = [
  { value: "", label: "Todos os status" },
  { value: "active", label: "Ativos" },
  { value: "suspended", label: "Desativados" },
  { value: "invited", label: "Convidados" },
  { value: "locked", label: "Bloqueados" },
];

const SOURCE_OPTIONS = [
  { value: "", label: "Todas as origens" },
  { value: "manual", label: "Local (NChat)" },
  { value: "oidc", label: "SSO (Keycloak)" },
  { value: "imported", label: "Importados" },
];

const ADMIN_OPTIONS = [
  { value: "", label: "Administradores e usuários" },
  { value: "true", label: "Somente administradores" },
  { value: "false", label: "Somente não administradores" },
];

/**
 * Workspace roles, not platform ones.
 *
 * The values are the server's allowlist keys; the labels are what an operator
 * reads. A value the server does not know comes back as a 400 rather than as a
 * filter that silently matches nothing.
 *
 * The directory is platform-wide — no request names a workspace — so "Owner"
 * here means "owns at least one workspace", which is what the label says.
 */
const WORKSPACE_ROLE_OPTIONS = [
  { value: "", label: "Qualquer papel de workspace" },
  { value: "owner", label: "Owner de algum workspace" },
  { value: "admin", label: "Admin de algum workspace" },
  { value: "moderator", label: "Moderador de algum workspace" },
  { value: "member", label: "Membro comum" },
  { value: "guest", label: "Convidado (guest)" },
];

const ACTIVITY_OPTIONS = [
  { value: "", label: "Qualquer último acesso" },
  { value: "7d", label: "Sem acesso há 7 dias" },
  { value: "30d", label: "Sem acesso há 30 dias" },
  { value: "90d", label: "Sem acesso há 90 dias" },
  { value: "never", label: "Nunca acessaram" },
];

interface Feedback {
  tone: "ok" | "error";
  text: string;
}

type PendingAction =
  | { kind: "status"; user: AdminUser; action: UserStatusAction }
  | { kind: "sessions"; user: AdminUser };

/**
 * Operational management of platform users.
 *
 * Everything the table shows is one server-side page. There is no client-side
 * filtering and no "load everything then search": the search, the filters and
 * the paging are all query parameters, so the browser never holds the platform's
 * user base in order to look at twenty rows of it.
 *
 * The action menu is drawn from capabilities, which is a courtesy and not a
 * control: an operator who edits it in a debugger gets more buttons and the same
 * 403s. Every mutation is re-authorized server-side on the request it makes.
 */
export default function UsersPage() {
  const { can, bootstrap } = useAdminSession();
  const canManage = can("admin.users.manage");
  const selfUserID = bootstrap?.identity.user_id ?? "";

  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search);
  const [filters, setFilters] = useState<UserFilters>({});
  // A stack rather than a single cursor: keyset pagination can only move
  // forward, so "previous page" is remembering where the previous one started.
  const [cursors, setCursors] = useState<(string | null)[]>([null]);
  const cursor = cursors[cursors.length - 1];

  const [pending, setPending] = useState<string | null>(null);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [confirming, setConfirming] = useState<PendingAction | null>(null);
  const [openUserID, setOpenUserID] = useState<string | null>(null);

  const load = useCallback(
    (signal: AbortSignal) =>
      listUsers({ ...filters, q: debouncedSearch }, cursor, PAGE_SIZE, signal),
    [filters, debouncedSearch, cursor],
  );
  const query = useAdminQuery(load);
  const page = query.data;

  // Any change to what is being searched restarts the paging: a cursor from the
  // previous filter names a position in a different result set.
  const applyFilter = (patch: UserFilters) => {
    setFilters((current) => ({ ...current, ...patch }));
    setCursors([null]);
  };

  const onSearch = (value: string) => {
    setSearch(value);
    setCursors([null]);
  };

  async function runMutation(key: string, action: () => Promise<string>) {
    // The guard is the state itself, not only the disabled attribute: a second
    // click that arrives before React re-renders finds `pending` already set.
    if (pending !== null) return;
    setPending(key);
    setFeedback(null);
    try {
      setFeedback({ tone: "ok", text: await action() });
      query.reload();
    } catch (error) {
      setFeedback({ tone: "error", text: classify(error).message });
    } finally {
      setPending(null);
      setConfirming(null);
    }
  }

  const confirmAction = () => {
    if (confirming === null) return;
    if (confirming.kind === "status") {
      const { user, action } = confirming;
      const to = action.targetStatus;
      void runMutation(`status:${user.id}`, async () => {
        const result = await updateUserStatus(user.id, to);
        return to === "suspended"
          ? `${label(user)} foi desativado. ${result.revokedSessions} sessão(ões) encerrada(s).`
          : `${label(user)} foi reativado. É necessário entrar novamente.`;
      });
      return;
    }
    const { user } = confirming;
    void runMutation(`sessions:${user.id}`, async () => {
      const revoked = await revokeUserSessions(user.id);
      return `${revoked} sessão(ões) de ${label(user)} encerrada(s).`;
    });
  };

  return (
    <section aria-labelledby="admin-users-title">
      <h1 id="admin-users-title">Usuários</h1>
      <p className="admin-lead">
        Gestão operacional das contas da plataforma. A busca e os filtros são aplicados no servidor.
      </p>

      <form
        className="admin-filters"
        role="search"
        aria-label="Filtrar usuários"
        onSubmit={(event) => event.preventDefault()}
      >
        <label className="admin-field">
          <span>Buscar por nome, e-mail ou login</span>
          <input
            type="search"
            value={search}
            onChange={(event) => onSearch(event.target.value)}
            placeholder="ana, ana@exemplo.test…"
          />
        </label>
        <FilterSelect
          label="Status"
          value={filters.status ?? ""}
          options={STATUS_OPTIONS}
          onChange={(status) => applyFilter({ status })}
        />
        <FilterSelect
          label="Origem da identidade"
          value={filters.authSource ?? ""}
          options={SOURCE_OPTIONS}
          onChange={(authSource) => applyFilter({ authSource })}
        />
        <FilterSelect
          label="Papel administrativo"
          value={filters.platformAdmin ?? ""}
          options={ADMIN_OPTIONS}
          onChange={(platformAdmin) => applyFilter({ platformAdmin })}
        />
        <FilterSelect
          label="Papel de workspace"
          value={filters.workspaceRole ?? ""}
          options={WORKSPACE_ROLE_OPTIONS}
          onChange={(workspaceRole) => applyFilter({ workspaceRole })}
        />
        <FilterSelect
          label="Último acesso"
          value={filters.inactivity ?? ""}
          options={ACTIVITY_OPTIONS}
          onChange={(inactivity) => applyFilter({ inactivity })}
        />
      </form>

      {feedback !== null && (
        <p
          className={feedback.tone === "ok" ? "admin-notice" : "admin-alert"}
          role={feedback.tone === "ok" ? "status" : "alert"}
          data-testid="admin-feedback"
        >
          {feedback.text}
        </p>
      )}

      <QueryStates
        status={query.status}
        message={query.message}
        empty="Nenhum usuário corresponde aos filtros aplicados."
        isEmpty={page !== null && page.items.length === 0}
        onRetry={query.reload}
        skeletonRows={5}
      />

      {query.status === "ready" && page !== null && page.items.length > 0 && (
        <>
          <div className="admin-table-scroll">
            <table className="admin-table">
              <caption className="admin-visually-hidden">Usuários da plataforma</caption>
              <thead>
                <tr>
                  <th scope="col">Pessoa</th>
                  <th scope="col">Status</th>
                  <th scope="col">Origem</th>
                  <th scope="col">Workspaces</th>
                  <th scope="col">Administração</th>
                  <th scope="col">Último acesso</th>
                  <th scope="col">Sessões</th>
                  <th scope="col">Ações</th>
                </tr>
              </thead>
              <tbody>
                {page.items.map((user) => (
                  <tr key={user.id}>
                    <th scope="row" className="admin-table__person">
                      <span className="admin-table__name">{label(user)}</span>
                      <span className="admin-table__muted">{user.email}</span>
                    </th>
                    <td>
                      {/* Status is spelled out, never signalled by colour alone. */}
                      <span className={`admin-status admin-status--${user.status}`}>
                        {statusLabel(user.status)}
                      </span>
                    </td>
                    <td>
                      {user.identityManagedExternally ? (
                        <span
                          className="admin-badge"
                          title="A fonte da verdade é o provedor de identidade"
                        >
                          Gerenciado pelo {user.externalProvider || "IdP"}
                        </span>
                      ) : (
                        <span className="admin-table__muted">Local</span>
                      )}
                    </td>
                    <td>
                      {user.workspaceRoles.length === 0 ? (
                        <span className="admin-table__muted">—</span>
                      ) : (
                        user.workspaceRoles.map((role) => (
                          <span key={role.workspaceId} className="admin-chip">
                            {role.workspaceName}: {role.role}
                          </span>
                        ))
                      )}
                    </td>
                    <td>
                      {user.platformAdmin ? (
                        <span className="admin-badge admin-badge--strong">
                          {user.adminRoles.join(", ") || "sem papel"}
                        </span>
                      ) : (
                        <span className="admin-table__muted">—</span>
                      )}
                    </td>
                    <td>{formatDateTime(user.lastLoginAt)}</td>
                    <td>{user.activeSessions}</td>
                    <td>
                      <div className="admin-actions">
                        <button
                          type="button"
                          className="admin-button admin-button--ghost"
                          onClick={() => setOpenUserID(user.id)}
                        >
                          Detalhes
                        </button>
                        {canManage && user.id !== selfUserID && (
                          <>
                            {/* Only a transition the platform really supports
                                gets a button. Everything else says why, rather
                                than offering an action whose only outcome is a
                                refusal. */}
                            {(() => {
                              const action = userStatusAction(user.status);
                              if (action === null) {
                                return (
                                  <span className="admin-table__muted">
                                    {noStatusActionReason(user.status)}
                                  </span>
                                );
                              }
                              return (
                                <button
                                  type="button"
                                  className="admin-button admin-button--ghost"
                                  disabled={pending !== null}
                                  onClick={() => setConfirming({ kind: "status", user, action })}
                                >
                                  {action.label}
                                </button>
                              );
                            })()}
                            <button
                              type="button"
                              className="admin-button admin-button--ghost"
                              disabled={pending !== null || user.activeSessions === 0}
                              onClick={() => setConfirming({ kind: "sessions", user })}
                            >
                              Encerrar sessões
                            </button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <Pagination
            count={page.items.length}
            hasMore={page.hasMore}
            canGoBack={cursors.length > 1}
            busy={pending !== null}
            onNext={() => setCursors((stack) => [...stack, page.nextCursor])}
            onPrevious={() => setCursors((stack) => stack.slice(0, -1))}
          />
        </>
      )}

      {confirming !== null && (
        <ConfirmDialog
          title={confirmTitle(confirming)}
          description={confirmDescription(confirming)}
          impact={confirmImpact(confirming)}
          confirmLabel={confirmLabelFor(confirming)}
          pending={pending !== null}
          onConfirm={confirmAction}
          onCancel={() => setConfirming(null)}
        />
      )}

      {openUserID !== null && (
        <UserDetailDialog
          userID={openUserID}
          onClose={() => setOpenUserID(null)}
          onChanged={() => {
            setFeedback({ tone: "ok", text: "Papel administrativo atualizado." });
            query.reload();
          }}
        />
      )}
    </section>
  );
}

interface FilterSelectProps {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
}

function FilterSelect({ label, value, options, onChange }: FilterSelectProps) {
  return (
    <label className="admin-field">
      <span>{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value)}>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </label>
  );
}

/** The label a person is known by. Never the technical identifier. */
function label(user: { displayName: string; fullName: string; email: string }): string {
  return user.fullName.trim() || user.displayName.trim() || user.email;
}

function statusLabel(status: string): string {
  switch (status) {
    case "active":
      return "Ativo";
    case "suspended":
      return "Desativado";
    case "invited":
      return "Convidado";
    case "locked":
      return "Bloqueado";
    default:
      return status;
  }
}

function confirmTitle(pending: PendingAction): string {
  if (pending.kind === "sessions") return "Encerrar todas as sessões?";
  return pending.action.confirmTitle;
}

function confirmDescription(pending: PendingAction): string {
  const who = label(pending.user);
  if (pending.kind === "sessions") return `${who} será desconectado de todos os dispositivos.`;
  return pending.action.confirmBody(who);
}

function confirmImpact(pending: PendingAction): string {
  if (pending.kind === "sessions") {
    return "As sessões ativas são revogadas imediatamente. O acesso da pessoa não muda: ela pode entrar de novo.";
  }
  return pending.action.impact;
}

function confirmLabelFor(pending: PendingAction): string {
  if (pending.kind === "sessions") return "Encerrar sessões";
  return pending.action.label;
}
