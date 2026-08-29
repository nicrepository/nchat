import { Outlet } from "react-router";

import "./ProfileSettingsShell.css";
import ProfileTabs from "./ProfileTabs";

export default function ProfileSettingsShell() {
  return (
    <div className="profile-settings" data-testid="profile-settings-shell">
      <header className="profile-settings__header">
        <h1 className="profile-settings__title">Configurações da conta</h1>
      </header>
      <ProfileTabs />
      <div
        id="profile-settings-panel"
        className="profile-settings__content"
        role="tabpanel"
        aria-label="Conteúdo da seção selecionada"
      >
        <Outlet />
      </div>
    </div>
  );
}
