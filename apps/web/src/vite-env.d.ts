/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_AUTH_API_BASE_URL?: string;
  readonly VITE_CHAT_API_BASE_URL?: string;
  readonly VITE_ADMIN_API_BASE_URL?: string;
  /** URL to the Keycloak Account Console for the realm. Unset disables the "Gerenciar segurança da conta" link. */
  readonly VITE_KEYCLOAK_ACCOUNT_URL?: string;
  readonly VITE_NOTIFICATIONS_API_BASE_URL?: string;
  /**
   * VAPID public key (base64url) this deployment signs its Web Push with. Public
   * by construction — every push service reads it. Unset means no Web Push:
   * the client reports "not_configured" instead of failing at subscribe time.
   */
  readonly VITE_NOTIFICATION_VAPID_PUBLIC_KEY?: string;
}
