import { NavLink } from "react-router";

import "./ProfileTabs.css";

const sections = [
  { to: "/profile", label: "Perfil", end: true },
  { to: "/profile/notifications", label: "Notificações", end: false },
  { to: "/profile/security", label: "Segurança", end: false },
  { to: "/profile/sessions", label: "Sessões", end: false },
] as const;

export default function ProfileTabs() {
  return (
    <nav className="profile-tabs" aria-label="Seções da conta">
      <ul className="profile-tabs__list">
        {sections.map((section) => (
          <li key={section.to} className="profile-tabs__item">
            <NavLink
              to={section.to}
              end={section.end}
              className={({ isActive }) =>
                isActive ? "profile-tabs__link profile-tabs__link--active" : "profile-tabs__link"
              }
            >
              {section.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  );
}
