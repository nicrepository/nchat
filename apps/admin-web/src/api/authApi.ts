/**
 * The console's half of NChat authentication.
 *
 * It reaches auth-service through the same origin the console is served from,
 * so the browser holds one set of cookies for one host. Nothing here persists a
 * token: the access token is handed straight to createAdminSession and then
 * dropped.
 */

const AUTH_BASE = "/api/auth";

export class AuthError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "AuthError";
    this.status = status;
    this.code = code;
  }
}

interface RawTokenResponse {
  access_token: string;
}

async function postForAccessToken(path: string, body: unknown): Promise<string> {
  let response: Response;
  try {
    response = await fetch(`${AUTH_BASE}${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "include",
      body: JSON.stringify(body),
    });
  } catch {
    throw new AuthError(0, "network_error", "Falha de rede");
  }
  if (!response.ok) {
    let code = "unknown_error";
    let message = "Não foi possível autenticar";
    try {
      const parsed = (await response.json()) as { error?: { code: string; message: string } };
      if (parsed.error) {
        code = parsed.error.code;
        message = parsed.error.message;
      }
    } catch {
      // Keep the generic message rather than surfacing a transport detail.
    }
    throw new AuthError(response.status, code, message);
  }
  const parsed = (await response.json()) as RawTokenResponse;
  return parsed.access_token;
}

export function login(email: string, password: string): Promise<string> {
  return postForAccessToken("/login", {
    email,
    password,
    device_name: "NIC Chat Admin Console",
  });
}

export function exchangeOIDCCode(code: string): Promise<string> {
  return postForAccessToken("/oidc/keycloak/exchange", { code });
}

/**
 * The single-sign-on entry point for the administrative console.
 *
 * `app=admin` is a label, not a destination. auth-service maps it to the
 * provider callback URI configured for the console host and records the choice
 * next to the OIDC state, so the identity provider returns the browser to this
 * origin rather than to the chat one. No URL is proposed here, and none could
 * be: the server accepts exactly two labels and resolves the address itself.
 *
 * The constant is fixed and same-origin, so nothing a visitor controls reaches
 * it either.
 */
export const OIDC_LOGIN_PATH = `${AUTH_BASE}/oidc/keycloak/login?app=admin`;
