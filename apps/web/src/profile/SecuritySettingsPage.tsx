import "./SecuritySettingsPage.css";

export default function SecuritySettingsPage() {
  const keycloakAccountUrl = import.meta.env.VITE_KEYCLOAK_ACCOUNT_URL;

  return (
    <div className="security-settings">
      <header className="security-settings__header">
        <h1 className="security-settings__title">Segurança</h1>
      </header>
      <section className="security-settings__card" aria-label="Segurança da conta">
        <dl className="security-settings__grid">
          <div className="security-settings__row">
            <dt>Provedor de identidade</dt>
            <dd>Keycloak</dd>
          </div>
        </dl>
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
