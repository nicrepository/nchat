import { useState, type FormEvent } from "react";

import { createAdminSession } from "../api/adminApi";
import { AdminApiError } from "../api/client";
import { AuthError, login, OIDC_LOGIN_PATH } from "../api/authApi";
import { useAdminSession } from "../session/useAdminSession";

/**
 * The console's sign-in screen.
 *
 * The sequence is deliberate: prove the identity with auth-service, hand the
 * resulting access token straight to the Admin API, and drop it. The token is a
 * local constant for the length of one async function — it is never written to
 * Web Storage, never kept in component state, and never becomes the console's
 * credential. What the console keeps is the HttpOnly cookie the Admin API sets.
 */
export default function LoginPage() {
  const { adopt, status, message } = useAdminSession();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    try {
      const accessToken = await login(email, password);
      const bootstrap = await createAdminSession(accessToken);
      // adopt() flips the session to "ready", which swaps this screen out of the
      // tree for the console shell. There is deliberately no setSubmitting after
      // it: the success path leaves the component rather than settling it, and
      // restoring the button here would be a state update on a screen that is
      // gone. Every failure below restores it, which is what lets an
      // administrator try again.
      adopt(bootstrap);
    } catch (caught) {
      setError(describe(caught));
      setSubmitting(false);
    }
  }

  return (
    <main className="admin-login" id="admin-main">
      <h1>Console administrativo</h1>
      <p className="admin-lead">Acesso restrito a administradores da plataforma.</p>

      {status === "forbidden" && (
        <p role="alert" className="admin-alert">
          Sua conta não possui acesso administrativo.
        </p>
      )}
      {status === "unavailable" && (
        <p role="alert" className="admin-alert">
          {message}
        </p>
      )}

      <form onSubmit={submit} noValidate>
        <label htmlFor="admin-login-email">E-mail</label>
        <input
          id="admin-login-email"
          name="email"
          type="email"
          autoComplete="username"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          required
        />

        <label htmlFor="admin-login-password">Senha</label>
        <input
          id="admin-login-password"
          name="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          required
        />

        {error !== "" && (
          <p role="alert" className="admin-alert">
            {error}
          </p>
        )}

        <button type="submit" className="admin-button" disabled={submitting}>
          {submitting ? "Entrando…" : "Entrar"}
        </button>
      </form>

      <p className="admin-login__sso">
        <a href={OIDC_LOGIN_PATH}>Entrar com SSO</a>
      </p>
    </main>
  );
}

/**
 * Turns a failure into something an administrator can act on, without repeating
 * a server message that might carry internal detail.
 *
 * A 403 here is the interesting one: the credentials were right and the account
 * still has no administrative authority. Saying so is what stops someone
 * retrying a correct password forever.
 */
function describe(error: unknown): string {
  if (error instanceof AdminApiError && error.status === 403) {
    return "Sua conta não possui acesso administrativo.";
  }
  if (error instanceof AdminApiError && error.status === 429) {
    return "Muitas tentativas. Tente novamente em instantes.";
  }
  if (error instanceof AuthError && error.status === 401) {
    return "E-mail ou senha inválidos.";
  }
  if (error instanceof AuthError && error.status === 0) {
    return "Falha de rede.";
  }
  return "Não foi possível entrar no console administrativo.";
}
