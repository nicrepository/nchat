/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_AUTH_API_BASE_URL?: string;
  readonly VITE_CHAT_API_BASE_URL?: string;
  readonly VITE_ADMIN_API_BASE_URL?: string;
  /** URL to the Keycloak Account Console for the realm. Unset disables the "Gerenciar segurança da conta" link. */
  readonly VITE_KEYCLOAK_ACCOUNT_URL?: string;
}
