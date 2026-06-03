import { type FormEvent, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { clearTokens, setTokens } from "../lib/authSession";
import "../tokens.css";
import "./auth.css";
import { login, oidcLoginUrl } from "./authApi";

const LOGIN_ERROR = "E-mail ou senha inválidos. Tente novamente.";
const CHANGE_PASSWORD_REQUIRED_MESSAGE =
  "Troca de senha obrigatória ainda não implementada. Encerre a sessão e solicite suporte interno.";

export default function LoginPage() {
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [passwordChangeRequired, setPasswordChangeRequired] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError(null);
    setPasswordChangeRequired(false);

    try {
      const result = await login({ email, password, deviceName: "NIC Chat Web" });
      if (result.user.mustChangePassword) {
        // TODO(auth): route this state into the real change-password flow when available.
        clearTokens(); // evict any prior stale session; new tokens are intentionally not stored
        setPasswordChangeRequired(true);
        return;
      }
      setTokens(result.accessToken, result.refreshToken);
      navigate("/", { replace: true });
    } catch {
      setError(LOGIN_ERROR);
    } finally {
      setLoading(false);
    }
  }

  function handleEndRequiredPasswordSession() {
    clearTokens();
    setPasswordChangeRequired(false);
    setPassword("");
  }

  return (
    <div className="auth-root">
      <main className="auth-main">
        <section className="auth-card" aria-labelledby="login-title">
          <div className="auth-header">
            <img className="auth-logo" src="/assets/nic-labs-logo.png" alt="NIC Chat" />
            <h1 id="login-title" className="auth-title">
              Entrar no NIC Chat
            </h1>
            <p className="auth-subtitle">Comunicação interna segura para equipes NIC-Labs</p>
          </div>

          {error && (
            <div id="login-error" className="auth-alert auth-alert--error" role="alert">
              {error}
            </div>
          )}

          {passwordChangeRequired ? (
            <div className="auth-form">
              <div className="auth-alert auth-alert--error" role="alert">
                {CHANGE_PASSWORD_REQUIRED_MESSAGE}
              </div>
              <button
                type="button"
                className="auth-btn auth-btn--secondary"
                onClick={handleEndRequiredPasswordSession}
              >
                Encerrar sessão
              </button>
            </div>
          ) : (
            <form className="auth-form" onSubmit={handleSubmit} noValidate>
              <div className="auth-field">
                <label className="auth-label" htmlFor="login-email">
                  E-mail corporativo
                </label>
                <div className="auth-input-wrap">
                  <span className="material-symbols-outlined auth-input-icon" aria-hidden="true">
                    mail
                  </span>
                  <input
                    id="login-email"
                    className="auth-input"
                    type="email"
                    placeholder="nome@nic-labs.com"
                    autoComplete="username"
                    required
                    value={email}
                    onChange={(event) => setEmail(event.target.value)}
                    aria-invalid={error !== null ? "true" : undefined}
                    aria-describedby={error !== null ? "login-error" : undefined}
                  />
                </div>
              </div>

              <div className="auth-field">
                <label className="auth-label" htmlFor="login-password">
                  Senha
                </label>
                <div className="auth-input-wrap">
                  <span className="material-symbols-outlined auth-input-icon" aria-hidden="true">
                    lock
                  </span>
                  <input
                    id="login-password"
                    className="auth-input"
                    type="password"
                    placeholder="********"
                    autoComplete="current-password"
                    required
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    aria-invalid={error !== null ? "true" : undefined}
                    aria-describedby={error !== null ? "login-error" : undefined}
                  />
                </div>
              </div>

              <div className="auth-actions">
                <button type="submit" className="auth-btn auth-btn--primary" disabled={loading}>
                  <span className="material-symbols-outlined auth-btn-icon" aria-hidden="true">
                    login
                  </span>
                  {loading ? "Entrando..." : "Entrar"}
                </button>
                <a className="auth-btn auth-btn--secondary" href={oidcLoginUrl()}>
                  <span className="material-symbols-outlined auth-btn-icon" aria-hidden="true">
                    shield_locked
                  </span>
                  Entrar com Keycloak
                </a>
              </div>

              <Link to="/forgot-password" className="auth-link">
                Esqueci minha senha
              </Link>
            </form>
          )}
        </section>
      </main>
      <footer className="auth-footer">
        <strong>NIC-Labs</strong> - Plataforma interna
      </footer>
    </div>
  );
}
