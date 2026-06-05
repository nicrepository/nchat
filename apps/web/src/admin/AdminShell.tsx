import "./AdminShell.css";

// ── Inline SVG icons ────────────────────────────────────────────────────────

function IconHash() {
  return (
    <svg
      className="admin-nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <line x1="10" y1="4" x2="8" y2="20" />
      <line x1="16" y1="4" x2="14" y2="20" />
      <line x1="4" y1="9" x2="20" y2="9" />
      <line x1="3" y1="15" x2="19" y2="15" />
    </svg>
  );
}

function IconForum() {
  return (
    <svg
      className="admin-nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
    </svg>
  );
}

function IconFolder() {
  return (
    <svg
      className="admin-nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
    </svg>
  );
}

function IconSettings() {
  return (
    <svg
      className="admin-nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
    </svg>
  );
}

function IconBell() {
  return (
    <svg
      className="admin-icon-btn-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" />
      <path d="M13.73 21a2 2 0 0 1-3.46 0" />
    </svg>
  );
}

// ── Admin tab config ────────────────────────────────────────────────────────

export type AdminTab = "overview" | "users" | "channels" | "audit";

const ADMIN_TABS: { id: AdminTab; label: string }[] = [
  { id: "overview", label: "Visão geral" },
  { id: "users", label: "Usuários" },
  { id: "channels", label: "Canais" },
  { id: "audit", label: "Auditoria" },
];

// ── Shell component ─────────────────────────────────────────────────────────

interface AdminShellProps {
  activeTab: AdminTab;
  children: React.ReactNode;
}

export default function AdminShell({ activeTab, children }: AdminShellProps) {
  return (
    <div className="admin-app" data-testid="admin-shell">
      {/* ── Dark sidebar ──────────────────────────────────────────────── */}
      <aside className="admin-sidebar" aria-label="NIC Chat" data-testid="admin-sidebar">
        <div className="admin-sidebar__brand" aria-label="NIC Chat — Workspace NIC-Labs">
          <div className="admin-sidebar__brand-mark" aria-hidden="true">
            <img src="/assets/nic-labs-logo.png" alt="" className="admin-sidebar__brand-img" />
          </div>
          <div>
            <p className="admin-sidebar__brand-title">NIC Chat</p>
            <p className="admin-sidebar__brand-sub">Workspace NIC-Labs</p>
          </div>
        </div>

        <nav className="admin-sidebar__nav" aria-label="Navegação do workspace">
          <div className="admin-sidebar__section-label">Canais</div>
          <a href="#" className="admin-sidebar__nav-item" aria-label="Canais">
            <IconHash />
            <span>Canais</span>
          </a>

          <div className="admin-sidebar__section-label" style={{ marginTop: "12px" }}>
            Mensagens diretas
          </div>
          <a href="#" className="admin-sidebar__nav-item" aria-label="Mensagens diretas">
            <IconForum />
            <span>Mensagens diretas</span>
          </a>

          <div className="admin-sidebar__section-label" style={{ marginTop: "12px" }}>
            Arquivos
          </div>
          <a href="#" className="admin-sidebar__nav-item" aria-label="Arquivos">
            <IconFolder />
            <span>Arquivos</span>
          </a>
        </nav>

        <div className="admin-sidebar__footer">
          <a
            href="/admin"
            className="admin-sidebar__nav-item admin-sidebar__nav-item--settings-active"
            aria-current="page"
          >
            <IconSettings />
            <span>Configurações</span>
          </a>
          <div className="admin-sidebar__user">
            <span className="admin-sidebar__avatar" aria-hidden="true">
              AN
            </span>
            <div className="admin-sidebar__user-info">
              <div className="admin-sidebar__user-name">Workspace NIC-Labs</div>
              <div className="admin-sidebar__user-role">Administrador</div>
            </div>
          </div>
        </div>
      </aside>

      {/* ── Main area (tabnav + scaffold) ─────────────────────────────── */}
      <div className="admin-main">
        <nav className="admin-tabnav" aria-label="Administração" data-testid="admin-topnav">
          <div className="admin-tabnav__items" role="tablist">
            {ADMIN_TABS.map((tab) => (
              <a
                key={tab.id}
                href={tab.id === "users" ? "/admin/users" : "#"}
                className={`admin-tabnav__item${activeTab === tab.id ? " admin-tabnav__item--active" : ""}`}
                role="tab"
                aria-selected={activeTab === tab.id}
                aria-current={activeTab === tab.id ? "page" : undefined}
              >
                {tab.label}
              </a>
            ))}
          </div>
          <div className="admin-tabnav__right">
            <button className="admin-icon-btn" aria-label="Notificações" type="button">
              <IconBell />
            </button>
            <span className="admin-tabnav__avatar" aria-hidden="true">
              AN
            </span>
          </div>
        </nav>

        <div className="admin-scaffold-body">
          <div className="admin-scaffold-container">{children}</div>
        </div>
      </div>
    </div>
  );
}
