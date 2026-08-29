import "./SecuritySettingsPage.css";

function accountManagementUrl(raw: string | undefined): string | undefined {
  if (!raw) return undefined;
  try {
    const url = new URL(raw);
    return url.protocol === "https:" && !url.username && !url.password ? url.href : undefined;
  } catch {
    return undefined;
  }
}

export default function SecuritySettingsPage() {
  const keycloakAccountUrl = accountManagementUrl(import.meta.env.VITE_KEYCLOAK_ACCOUNT_URL);

  return (
    <div className="security-settings">
      <header className="security-settings__header">
        <h2 className="security-settings__title">Segurança</h2>
      </header>
      <section className="security-settings__card" aria-label="Segurança da conta">
        <p className="security-settings__note">
          Gerencie senha e autenticação no provedor de identidade.
        </p>
        {keycloakAccountUrl ? (
          <a
            href={keycloakAccountUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="security-settings__manage-btn"
          >
            Gerenciar segurança da conta
          </a>
        ) : (
          <p className="security-settings__unavailable">
            O gerenciamento de conta do provedor de identidade não está configurado neste ambiente.
          </p>
        )}
      </section>
    </div>
  );
}
