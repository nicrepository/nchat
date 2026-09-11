import { Outlet, useOutletContext } from "react-router";

import "./ProfileSettingsShell.css";
import ProfileTabs from "./ProfileTabs";

export default function ProfileSettingsShell() {
  // The chat shell above this one owns the sidebar hook and hands it down as
  // outlet context (issue #729). A nested <Outlet /> publishes its own context
  // and would otherwise hand `null` to every /profile child — which is what
  // would push a settings page into mounting a second useChatSidebar, with its
  // own fetch, its own WebSocket targets and its own copy of state. Forwarding
  // keeps exactly one instance for /chat/* and /profile/* alike.
  const context = useOutletContext<unknown>();

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
        <Outlet context={context} />
      </div>
    </div>
  );
}
