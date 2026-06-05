import { useEffect, useMemo, useState } from "react";

import "./AdminUsersPage.css";
import AdminShell from "./AdminShell";
import { type AdminUser, listAdminUsers } from "./adminUsersApi";

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
        </tr>
      ))}
    </>
  );
}

// ── Empty state ────────────────────────────────────────────────────────────

function EmptyState() {
  return (
    <tr>
      <td colSpan={5}>
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
          <p className="admin-users__empty-title">Nenhum usuário encontrado</p>
          <p className="admin-users__empty-sub">
            Usuários aparecerão aqui assim que o serviço estiver disponível.
          </p>
        </div>
      </td>
    </tr>
  );
}

// ── Error state ────────────────────────────────────────────────────────────

function ErrorState() {
  return (
    <tr>
      <td colSpan={5}>
        <div className="admin-users__error">
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
          <p className="admin-users__error-title">Não foi possível carregar os usuários</p>
          <p className="admin-users__error-sub">
            Verifique sua conexão ou tente novamente em instantes.
          </p>
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
        </tr>
      ))}
    </>
  );
}

// ── Filter chip types ───────────────────────────────────────────────────────

type FilterChip = "all" | "active" | "suspended" | "admins" | "invites";

const CHIPS: { id: FilterChip; label: string }[] = [
  { id: "all", label: "Todos" },
  { id: "active", label: "Ativos" },
  { id: "suspended", label: "Suspensos" },
  { id: "admins", label: "Admins" },
  { id: "invites", label: "Convites pendentes" },
];

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

// ── Page ───────────────────────────────────────────────────────────────────

type PageState = { kind: "loading" } | { kind: "error" } | { kind: "success"; users: AdminUser[] };

export default function AdminUsersPage() {
  const [state, setState] = useState<PageState>({ kind: "loading" });
  const [activeFilter, setActiveFilter] = useState<FilterChip>("all");
  const [search, setSearch] = useState("");

  useEffect(() => {
    let cancelled = false;

    listAdminUsers()
      .then((users) => {
        if (!cancelled) setState({ kind: "success", users });
      })
      .catch(() => {
        if (!cancelled) setState({ kind: "error" });
      });

    return () => {
      cancelled = true;
    };
  }, []);

  const filteredUsers = useMemo(() => {
    if (state.kind !== "success") return [];
    let users = state.users;
    if (activeFilter === "active") {
      users = users.filter((u) => u.status.toLowerCase() === "active");
    } else if (activeFilter === "suspended") {
      users = users.filter((u) => u.status.toLowerCase() === "suspended");
    } else if (activeFilter === "admins" || activeFilter === "invites") {
      // No role or invite data available from API; show empty
      users = [];
    }
    if (search.trim()) {
      const q = search.toLowerCase();
      users = users.filter(
        (u) => u.displayName.toLowerCase().includes(q) || u.email.toLowerCase().includes(q),
      );
    }
    return users;
  }, [state, activeFilter, search]);

  let tableBody: React.ReactNode;
  if (state.kind === "loading") {
    tableBody = <SkeletonRows />;
  } else if (state.kind === "error") {
    tableBody = <ErrorState />;
  } else if (filteredUsers.length === 0) {
    tableBody = <EmptyState />;
  } else {
    tableBody = <UserRows users={filteredUsers} />;
  }

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
          disabled
          aria-disabled="true"
          title="Funcionalidade não disponível nesta versão"
        >
          <IconPersonAdd />
          Convidar usuário
        </button>
      </div>

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
          {CHIPS.map((chip) => (
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
            </tr>
          </thead>
          <tbody>{tableBody}</tbody>
        </table>
      </div>
    </AdminShell>
  );
}
