import { useState } from "react";
import { NavLink, Outlet } from "react-router";

import EnvironmentBadge from "./EnvironmentBadge";
import { visibleNavItems } from "./navigation";
import { useAdminSession } from "../session/useAdminSession";

/**
 * The console shell: a landmark-per-region layout with the sidebar, the header,
 * the environment indicator, the signed-in identity and sign-out.
 *
 * Accessibility is structural rather than decorative here: the regions are real
 * landmarks (`banner`, `navigation`, `main`), the current page is marked with
 * `aria-current`, unavailable sections are `aria-disabled` and unfocusable, and
 * the skip link makes the sidebar bypassable for keyboard and screen-reader
 * users.
 */
export default function AdminLayout() {
  const { bootstrap, signOut, can } = useAdminSession();
  const [signingOut, setSigningOut] = useState(false);

  if (bootstrap === null) return null;
  const items = visibleNavItems(can);

  return (
    <div className="admin-shell">
      <a className="admin-skip-link" href="#admin-main">
        Ir para o conteúdo
      </a>

      <header className="admin-header" role="banner">
        <div className="admin-header__brand">
          <span className="admin-header__title">NIC Chat</span>
          <span className="admin-header__subtitle">Console administrativo</span>
        </div>
        <div className="admin-header__meta">
          <EnvironmentBadge environment={bootstrap.environment} />
          <span className="admin-header__identity" data-testid="admin-identity">
            <span className="admin-header__name">{bootstrap.identity.display_name}</span>
            <span className="admin-header__email">{bootstrap.identity.email}</span>
          </span>
          <button
            type="button"
            className="admin-button"
            onClick={() => {
              // No handler after the call. signOut always ends in
              // "unauthenticated" — it swallows a failed revocation rather than
              // leaving the operator on an administrative shell — so this
              // component is unmounted by the time the promise settles, and
              // restoring the button would be a state update on a screen that
              // is already gone.
              setSigningOut(true);
              void signOut();
            }}
            disabled={signingOut}
          >
            {signingOut ? "Saindo…" : "Sair"}
          </button>
        </div>
      </header>

      <div className="admin-body">
        <nav className="admin-sidebar" aria-label="Seções administrativas">
          <ul className="admin-sidebar__list">
            {items.map((item) =>
              item.path === undefined ? (
                <li key={item.id}>
                  <span
                    className="admin-sidebar__item admin-sidebar__item--unavailable"
                    aria-disabled="true"
                  >
                    {item.label}
                    <span className="admin-sidebar__tag">não disponível</span>
                  </span>
                </li>
              ) : (
                <li key={item.id}>
                  <NavLink
                    to={item.path}
                    end={item.path === "/"}
                    className={({ isActive }) =>
                      `admin-sidebar__item${isActive ? " admin-sidebar__item--active" : ""}`
                    }
                  >
                    {item.label}
                  </NavLink>
                </li>
              ),
            )}
          </ul>
        </nav>

        <main className="admin-main" id="admin-main" tabIndex={-1}>
          <Outlet />
        </main>
      </div>

      <footer className="admin-footer">
        <span>
          {bootstrap.build.service} {bootstrap.build.version} ({bootstrap.build.commit})
        </span>
      </footer>
    </div>
  );
}
