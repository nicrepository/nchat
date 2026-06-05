import { useEffect, useState } from "react";

import "./AdminUsersPage.css";
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
            👥
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
            ⚠️
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

// ── Page ───────────────────────────────────────────────────────────────────

type PageState = { kind: "loading" } | { kind: "error" } | { kind: "success"; users: AdminUser[] };

export default function AdminUsersPage() {
  const [state, setState] = useState<PageState>({ kind: "loading" });

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

  let tableBody: React.ReactNode;
  if (state.kind === "loading") {
    tableBody = <SkeletonRows />;
  } else if (state.kind === "error") {
    tableBody = <ErrorState />;
  } else if (state.users.length === 0) {
    tableBody = <EmptyState />;
  } else {
    tableBody = <UserRows users={state.users} />;
  }

  return (
    <main className="admin-users">
      <div className="admin-users__head">
        <h1 className="admin-users__title">Usuários</h1>
        <p className="admin-users__subtitle">Visão geral das contas registradas no workspace.</p>
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
    </main>
  );
}
