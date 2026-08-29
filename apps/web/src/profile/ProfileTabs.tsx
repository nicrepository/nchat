import { type KeyboardEvent } from "react";
import { Link, useLocation } from "react-router";

import "./ProfileTabs.css";

const sections = [
  { key: "profile", to: "/profile", label: "Perfil", end: true },
  {
    key: "notifications",
    to: "/profile/notifications",
    label: "Notificações",
    end: false,
  },
  { key: "security", to: "/profile/security", label: "Segurança", end: false },
  { key: "sessions", to: "/profile/sessions", label: "Sessões", end: false },
] as const;

function isActive(pathname: string, section: (typeof sections)[number]): boolean {
  return section.end
    ? pathname === section.to
    : pathname === section.to || pathname.startsWith(`${section.to}/`);
}

export default function ProfileTabs() {
  const { pathname } = useLocation();

  function handleKeyDown(event: KeyboardEvent<HTMLUListElement>) {
    const tabs = Array.from(event.currentTarget.querySelectorAll<HTMLElement>("[role='tab']"));
    const current = tabs.findIndex((tab) => tab === document.activeElement);
    if (current < 0) return;

    let next: number;
    switch (event.key) {
      case "ArrowRight":
        next = (current + 1) % tabs.length;
        break;
      case "ArrowLeft":
        next = (current - 1 + tabs.length) % tabs.length;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = tabs.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    tabs[next]?.focus();
    tabs[next]?.click();
  }

  return (
    <nav className="profile-tabs" aria-label="Seções da conta">
      <ul
        className="profile-tabs__list"
        role="tablist"
        aria-orientation="horizontal"
        onKeyDown={handleKeyDown}
      >
        {sections.map((section) => {
          const active = isActive(pathname, section);
          return (
            <li key={section.to} className="profile-tabs__item" role="presentation">
              <Link
                id={`profile-tab-${section.key}`}
                to={section.to}
                role="tab"
                aria-controls="profile-settings-panel"
                aria-selected={active}
                aria-current={active ? "page" : undefined}
                tabIndex={active ? 0 : -1}
                className={
                  active ? "profile-tabs__link profile-tabs__link--active" : "profile-tabs__link"
                }
              >
                {section.label}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
