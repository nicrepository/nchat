import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";

import "./AdminUsersPage.css";
import AdminShell from "./AdminShell";
import {
  type AdminErrorKind,
  type AdminUser,
  classifyAdminError,
  createAdminInvite,
} from "./adminUsersApi";
import { FILTER_CHIPS, type FilterChip, filterAdminUsers } from "./adminUsersFilter";
import { type AdminUsersState, useAdminUsers } from "./useAdminUsers";

// ── Helpers ────────────────────────────────────────────────────────────────

/** Derives up to two uppercase initials from a display name. */
function initials(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return "?";
  const parts = trimmed.split(/\s+/);
  if (parts.length === 1) return (parts[0][0] ?? "?").toUpperCase();
  return ((parts[0][0] ?? "") + (parts[1][0] ?? "")).toUpperCase();
}

/** Formats an ISO-8601 date string as a localised short date. */
function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString("pt-BR", {
      day: "2-digit",
      month: "short",
      year: "numeric",
    });
  } catch {
    return iso;
  }
}

// ── Sub-components ─────────────────────────────────────────────────────────

interface StatusBadgeProps {
  status: string;
}

function StatusBadge({ status }: StatusBadgeProps) {
  const s = status.toLowerCase();
  if (s === "active") {
    return (
      <span className="admin-users__badge admin-users__badge--success">
        <span className="admin-users__status-dot admin-users__status-dot--success" />
        Ativo
      </span>
    );
  }
  if (s === "suspended") {
    return (
      <span className="admin-users__badge admin-users__badge--danger">
        <span className="admin-users__status-dot admin-users__status-dot--danger" />
        Suspenso
      </span>
    );
  }
  return (
    <span className="admin-users__badge admin-users__badge--neutral">
      <span className="admin-users__status-dot admin-users__status-dot--neutral" />
      {status}
    </span>
  );
}

/** Renders the authentication origin (auth_source field) as a neutral label. */
function OriginBadge({ authSource }: { authSource: string }) {
  return <span className="admin-users__badge admin-users__badge--neutral">{authSource}</span>;
}

// ── Status action button (disabled until admin JWT/RBAC guard exists) ──────

interface StatusActionButtonProps {
  status: string;
}

function StatusActionButton({ status }: StatusActionButtonProps) {
  const s = status.toLowerCase();
  if (s === "active") {
    return (
      <button
        type="button"
        className="admin-users__action-btn"
        disabled
        aria-disabled="true"
        title="Requer permissão de administrador"
      >
        Suspender
      </button>
    );
  }
  if (s === "suspended") {
    return (
      <button
        type="button"
        className="admin-users__action-btn"
        disabled
        aria-disabled="true"
        title="Requer permissão de administrador"
      >
        Ativar
      </button>
    );
  }
  return null;
}

// ── Skeleton rows ──────────────────────────────────────────────────────────

const SKELETON_COUNT = 5;

function SkeletonRows() {
  return (
    <>
      {Array.from({ length: SKELETON_COUNT }, (_, i) => (
        <tr key={i} className="admin-users__skeleton-row" aria-hidden="true">
          <td>
            <div className="admin-users__skel-cell">
              <span className="admin-users__skel admin-users__skel--avatar" />
              <div>
                <div className="admin-users__skel" style={{ width: 120, marginBottom: 5 }} />
                <div className="admin-users__skel" style={{ width: 80, height: 11 }} />
              </div>
            </div>
          </td>
          <td className="admin-users__col-email">
            <div className="admin-users__skel" style={{ width: 160 }} />
          </td>
          <td>
            <div className="admin-users__skel" style={{ width: 70 }} />
          </td>
          <td>
            <div className="admin-users__skel" style={{ width: 60 }} />
          </td>
          <td className="admin-users__col-created">
            <div className="admin-users__skel" style={{ width: 90 }} />
          </td>
          <td>
            <div className="admin-users__skel" style={{ width: 68 }} />
          </td>
        </tr>
      ))}
    </>
  );
}

// ── Empty state ────────────────────────────────────────────────────────────

function EmptyState() {
  return (
    <tr>
      <td colSpan={6}>
        <div className="admin-users__empty">
          <div className="admin-users__empty-icon" aria-hidden="true">
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2" />
              <circle cx="9" cy="7" r="4" />
              <path d="M23 21v-2a4 4 0 0 0-3-3.87" />
              <path d="M16 3.13a4 4 0 0 1 0 7.75" />
            </svg>
          </div>
          <p className="admin-users__empty-title">Nenhum usuário disponível</p>
          <p className="admin-users__empty-sub">
            As contas registradas aparecerão aqui quando o serviço retornar resultados.
          </p>
        </div>
      </td>
    </tr>
  );
}

// ── Error state ────────────────────────────────────────────────────────────

/**
 * Each failure gets its own wording. Presenting a 403 as a connection problem —
 * or, as this screen used to, presenting any failure as "no users" — sends the
 * admin looking in the wrong place.
 */
const ERROR_COPY: Record<AdminErrorKind, { title: string; sub: string; retryable: boolean }> = {
  unauthorized: {
    title: "Sua sessão expirou",
    sub: "Entre novamente para gerenciar os usuários do workspace.",
    retryable: false,
  },
  forbidden: {
    title: "Você não tem permissão para ver os usuários",
    sub: "Esta área é restrita a administradores do workspace.",
    retryable: false,
  },
  "rate-limited": {
    title: "Muitas solicitações",
    sub: "Aguarde alguns instantes antes de tentar novamente.",
    // No retry button: offering one invites the user to hammer a limit that is
    // already refusing them.
    retryable: false,
  },
  error: {
    title: "Não foi possível carregar os usuários",
    sub: "Verifique sua conexão ou tente novamente em instantes.",
    retryable: true,
  },
};

interface ErrorStateProps {
  kind: AdminErrorKind;
  onRetry: () => void;
}

function ErrorState({ kind, onRetry }: ErrorStateProps) {
  const copy = ERROR_COPY[kind];
  return (
    <tr>
      <td colSpan={6}>
        <div className="admin-users__error" role="alert">
          <div className="admin-users__error-icon" aria-hidden="true">
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
              <line x1="12" y1="9" x2="12" y2="13" />
              <line x1="12" y1="17" x2="12.01" y2="17" />
            </svg>
          </div>
          <p className="admin-users__error-title">{copy.title}</p>
          <p className="admin-users__error-sub">{copy.sub}</p>
          {copy.retryable && (
            <button type="button" className="admin-users__retry-btn" onClick={onRetry}>
              Tentar novamente
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}

// ── User rows ──────────────────────────────────────────────────────────────

interface UserRowsProps {
  users: AdminUser[];
}

function UserRows({ users }: UserRowsProps) {
  return (
    <>
      {users.map((user) => (
        <tr key={user.id}>
          <td>
            <div className="admin-users__user-cell">
              <span className="admin-users__avatar" aria-hidden="true">
                {initials(user.displayName)}
              </span>
              <div>
                <div className="admin-users__user-name">{user.displayName}</div>
                {user.fullName && user.fullName !== user.displayName && (
                  <div className="admin-users__user-sub">{user.fullName}</div>
                )}
              </div>
            </div>
          </td>
          <td className="admin-users__col-email admin-users__muted">{user.email}</td>
          <td>
            <StatusBadge status={user.status} />
          </td>
          <td>
            <OriginBadge authSource={user.authSource} />
          </td>
          <td className="admin-users__col-created admin-users__muted">
            {formatDate(user.createdAt)}
          </td>
          <td>
            <StatusActionButton status={user.status} />
          </td>
        </tr>
      ))}
    </>
  );
}

// ── Invite icon (SVG) ──────────────────────────────────────────────────────

function IconPersonAdd() {
  return (
    <svg
      className="admin-users__invite-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <line x1="19" y1="8" x2="19" y2="14" />
      <line x1="22" y1="11" x2="16" y2="11" />
    </svg>
  );
}

// ── Search icon (SVG) ──────────────────────────────────────────────────────

function IconSearch() {
  return (
    <svg
      className="admin-users__search-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <circle cx="11" cy="11" r="8" />
      <line x1="21" y1="21" x2="16.65" y2="16.65" />
    </svg>
  );
}

// ── Invite dialog ──────────────────────────────────────────────────────────

interface InviteDialogProps {
  onClose: () => void;
  onInvited: () => void;
}

/** Maps a failed invite to user-facing copy. Server messages are never shown
 * verbatim: they may carry detail that does not belong on screen. */
function inviteErrorMessage(kind: AdminErrorKind): string {
  switch (kind) {
    case "forbidden":
      return "Você não tem permissão para convidar usuários.";
    case "unauthorized":
      return "Sua sessão expirou. Entre novamente para convidar usuários.";
    case "rate-limited":
      return "Muitos convites enviados. Aguarde alguns instantes e tente novamente.";
    default:
      return "Não foi possível enviar o convite. Tente novamente.";
  }
}

/**
 * Minimal invite form. It collects an e-mail and a display name and nothing
 * else — notably no role, because granting privileges is not something this
 * screen may do and the endpoint would ignore the field anyway.
 */
function InviteDialog({ onClose, onInvited }: InviteDialogProps) {
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const titleId = useId();
  const emailId = useId();
  const nameId = useId();
  const errorId = useId();
  const emailRef = useRef<HTMLInputElement>(null);

  // Focus lands on the first field so a keyboard user is not stranded at the
  // top of the page after the dialog opens.
  useEffect(() => {
    emailRef.current?.focus();
  }, []);

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    // Guarding on `submitting` is what makes a double click one invite: the
    // button is also disabled, but a form can still be submitted with Enter.
    if (submitting) return;

    const trimmedEmail = email.trim();
    const trimmedName = displayName.trim();
    if (!trimmedEmail || !trimmedName) {
      setError("Informe e-mail e nome de exibição.");
      return;
    }
    // Shape check only, for immediate feedback. The server validates and
    // normalises the address, and that is what decides.
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmedEmail)) {
      setError("Informe um e-mail válido.");
      return;
    }

    setSubmitting(true);
    setError(null);
    try {
      await createAdminInvite({ email: trimmedEmail, displayName: trimmedName });
      onInvited();
    } catch (err) {
      // The server message is not shown verbatim: it may carry detail that does
      // not belong on screen.
      // The dialog never resubmits on its own. A 429 in particular is reported
      // and left to the operator: an automatic retry would spend the budget
      // that is already exhausted.
      const kind = classifyAdminError(err);
      setError(inviteErrorMessage(kind));
      setSubmitting(false);
    }
  }

  return (
    <div className="admin-users__modal-backdrop">
      <div className="admin-users__modal" role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <h2 className="admin-users__modal-title" id={titleId}>
          Convidar usuário
        </h2>
        <form onSubmit={handleSubmit} noValidate>
          <div className="admin-users__field">
            <label className="admin-users__label" htmlFor={emailId}>
              E-mail
            </label>
            <input
              id={emailId}
              ref={emailRef}
              className="admin-users__input"
              type="email"
              value={email}
              onChange={(e) => {
                setEmail(e.target.value);
                setError(null);
              }}
              disabled={submitting}
              aria-describedby={error ? errorId : undefined}
              aria-invalid={error ? true : undefined}
            />
          </div>
          <div className="admin-users__field">
            <label className="admin-users__label" htmlFor={nameId}>
              Nome de exibição
            </label>
            <input
              id={nameId}
              className="admin-users__input"
              type="text"
              value={displayName}
              onChange={(e) => {
                setDisplayName(e.target.value);
                setError(null);
              }}
              disabled={submitting}
              aria-describedby={error ? errorId : undefined}
              aria-invalid={error ? true : undefined}
            />
          </div>

          {error && (
            <p className="admin-users__modal-error" id={errorId} role="alert">
              {error}
            </p>
          )}

          <div className="admin-users__modal-actions">
            <button
              type="button"
              className="admin-users__modal-cancel"
              onClick={onClose}
              disabled={submitting}
            >
              Cancelar
            </button>
            <button type="submit" className="admin-users__modal-submit" disabled={submitting}>
              {submitting ? "Enviando…" : "Enviar convite"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ── Page ───────────────────────────────────────────────────────────────────

/**
 * Chooses the table body for the current query state.
 *
 * Split out of the component so the state-to-view mapping is one readable
 * expression rather than an if/else chain interleaved with rendering. The empty
 * state is reachable only from `success`, which is what makes it mean what it
 * says.
 */
function TableBody({
  state,
  users,
  onRetry,
}: {
  state: AdminUsersState;
  users: AdminUser[];
  onRetry: () => void;
}) {
  if (state.kind === "loading") return <SkeletonRows />;
  if (state.kind === "error") return <ErrorState kind={state.error} onRetry={onRetry} />;
  if (users.length === 0) return <EmptyState />;
  return <UserRows users={users} />;
}

export default function AdminUsersPage() {
  const { state, reload, loadMore, loadingMore, loadMoreError, hasMore } = useAdminUsers();
  const [activeFilter, setActiveFilter] = useState<FilterChip>("all");
  const [search, setSearch] = useState("");
  const [inviteOpen, setInviteOpen] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const inviteButtonRef = useRef<HTMLButtonElement>(null);

  const closeInvite = useCallback(() => {
    setInviteOpen(false);
    inviteButtonRef.current?.focus();
  }, []);

  const handleInvited = useCallback(() => {
    // An invite is not a user: it creates an invitation the recipient has yet
    // to accept, so no row is fabricated here. The list is refetched and shows
    // whatever the server actually has.
    setNotice("Convite enviado.");
    closeInvite();
    reload();
  }, [closeInvite, reload]);

  const filteredUsers = useMemo(
    () => (state.kind === "success" ? filterAdminUsers(state.users, activeFilter, search) : []),
    [state, activeFilter, search],
  );

  return (
    <AdminShell activeTab="users">
      <div className="admin-users__page-head">
        <div>
          <h1 className="admin-users__title">Usuários</h1>
          <p className="admin-users__subtitle">
            Gerencie acessos e estado de cada conta no workspace.
          </p>
        </div>
        <button
          className="admin-users__invite-btn"
          type="button"
          ref={inviteButtonRef}
          onClick={() => {
            setNotice(null);
            setInviteOpen(true);
          }}
        >
          <IconPersonAdd />
          Convidar usuário
        </button>
      </div>

      <p className="admin-users__notice" role="status" aria-live="polite">
        {notice}
      </p>

      <div className="admin-users__toolbar">
        <div className="admin-users__search-wrap">
          <IconSearch />
          <input
            className="admin-users__search-input"
            type="search"
            placeholder="Buscar por nome ou e-mail…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            aria-label="Buscar usuários"
          />
        </div>
        <div className="admin-users__chips" role="group" aria-label="Filtrar usuários">
          {FILTER_CHIPS.map((chip) => (
            <button
              key={chip.id}
              type="button"
              className={`admin-users__chip${activeFilter === chip.id ? " admin-users__chip--active" : ""}`}
              onClick={() => setActiveFilter(chip.id)}
              aria-pressed={activeFilter === chip.id}
            >
              {chip.label}
            </button>
          ))}
        </div>
      </div>

      {state.kind === "success" && hasMore && (
        <p className="admin-users__filter-note">Os filtros consideram os usuários já carregados.</p>
      )}

      <div className="admin-users__table-wrap">
        <table
          className="admin-users__table"
          aria-label="Lista de usuários"
          aria-busy={state.kind === "loading"}
        >
          <thead>
            <tr>
              <th scope="col">Nome</th>
              <th scope="col" className="admin-users__col-email">
                E-mail
              </th>
              <th scope="col">Status</th>
              <th scope="col">Origem</th>
              <th scope="col" className="admin-users__col-created">
                Criado em
              </th>
              <th scope="col">Ações</th>
            </tr>
          </thead>
          <tbody>
            <TableBody state={state} users={filteredUsers} onRetry={reload} />
          </tbody>
        </table>
      </div>

      {state.kind === "success" && hasMore && (
        <div className="admin-users__load-more">
          <button
            type="button"
            className="admin-users__load-more-btn"
            onClick={loadMore}
            disabled={loadingMore}
            aria-busy={loadingMore || undefined}
          >
            {loadingMore ? "Carregando mais usuários…" : "Carregar mais usuários"}
          </button>
          {loadMoreError && (
            <p className="admin-users__load-more-error" role="alert">
              {ERROR_COPY[loadMoreError].title}
            </p>
          )}
        </div>
      )}

      {inviteOpen && <InviteDialog onClose={closeInvite} onInvited={handleInvited} />}
    </AdminShell>
  );
}
