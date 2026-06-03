import { type FormEvent, useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";

import { ApiRequestError } from "../lib/api";
import "../tokens.css";
import "./auth.css";
import { acceptInvite } from "./authApi";

const PASSWORD_MISMATCH_ERROR = "As senhas não coincidem.";
const DISPLAY_NAME_ERROR = "Informe um nome de exibição.";

function readToken(search: string): string {
  return new URLSearchParams(search).get("token")?.trim() ?? "";
}

function inviteErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError && error.code === "invalid_invite_token") {
    return "Convite inválido ou expirado. Solicite um novo convite.";
  }

  if (error instanceof ApiRequestError && error.status === 400) {
    return "A senha não atende aos requisitos de segurança.";
  }

  return "Não foi possível ativar a conta. Tente novamente.";
}

export default function AcceptInvitePage() {
  const location = useLocation();
  const navigate = useNavigate();
  const [token, setToken] = useState(() => readToken(location.search));
  const [displayName, setDisplayName] = useState("");
  const [fullName, setFullName] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [displayNameError, setDisplayNameError] = useState<string | null>(null);
  const [confirmError, setConfirmError] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);

  useEffect(() => {
    if (location.search !== "") {
      navigate(location.pathname, { replace: true });
    }
  }, [location.pathname, location.search, navigate]);

  if (token === "" && !success) {
    return (
      <div className="auth-root">
        <main className="auth-main">
          <section className="auth-card" aria-labelledby="invite-invalid-title">
            <div className="auth-header">
              <img className="auth-logo" src="/assets/nic-labs-logo.png" alt="NIC Chat" />
              <h1 id="invite-invalid-title" className="auth-title">
                Convite inválido
              </h1>
              <p className="auth-subtitle">Este convite é inválido ou expirou.</p>
            </div>
            <div className="auth-actions">
              <Link to="/login" className="auth-btn auth-btn--primary">
                Ir para entrar
              </Link>
            </div>
          </section>
        </main>
        <footer className="auth-footer">
          <strong>NIC-Labs</strong> - Plataforma interna
        </footer>
      </div>
    );
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setDisplayNameError(null);
    setConfirmError(null);
    setError(null);

    const trimmedDisplayName = displayName.trim();
    const trimmedFullName = fullName.trim();

    if (trimmedDisplayName === "") {
      setDisplayNameError(DISPLAY_NAME_ERROR);
      return;
    }

    if (password !== confirmPassword) {
      setConfirmError(PASSWORD_MISMATCH_ERROR);
      return;
    }

    if (token === "") {
      setError("Convite inválido ou expirado. Solicite um novo convite.");
      return;
    }

    setLoading(true);
    try {
      await acceptInvite({
        token,
        displayName: trimmedDisplayName,
        fullName: trimmedFullName || undefined,
        password,
      });
      setToken("");
      setSuccess(true);
    } catch (err) {
      setError(inviteErrorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="auth-root">
      <main className="auth-main">
        <section className="auth-card auth-card--wide" aria-labelledby="invite-title">
          <div className="auth-header">
            <img className="auth-logo" src="/assets/nic-labs-logo.png" alt="NIC Chat" />
            <h1 id="invite-title" className="auth-title">
              Ativar conta
            </h1>
            <p className="auth-subtitle">Complete seu cadastro para acessar o NIC Chat</p>
          </div>

          {success ? (
            <>
              <div className="auth-alert auth-alert--success" role="status">
                Conta ativada com sucesso. Faça login para começar.
              </div>
              <Link to="/login" className="auth-link">
                Ir para entrar
              </Link>
            </>
          ) : (
            <>
              {error && (
                <div id="invite-error" className="auth-alert auth-alert--error" role="alert">
                  {error}
                </div>
              )}
              <form className="auth-form" onSubmit={handleSubmit} noValidate>
                <div className="auth-field">
                  <label className="auth-label" htmlFor="invite-display-name">
                    Nome de exibição
                  </label>
                  <input
                    id="invite-display-name"
                    className="auth-input auth-input--no-icon"
                    type="text"
                    placeholder="Como você quer ser chamado"
                    autoComplete="nickname"
                    required
                    value={displayName}
                    onChange={(event) => setDisplayName(event.target.value)}
                    aria-invalid={displayNameError !== null ? "true" : undefined}
                    aria-describedby={
                      displayNameError !== null ? "invite-display-name-error" : undefined
                    }
                  />
                  {displayNameError && (
                    <p id="invite-display-name-error" className="auth-field-error" role="alert">
                      {displayNameError}
                    </p>
                  )}
                </div>

                <div className="auth-field">
                  <label className="auth-label" htmlFor="invite-full-name">
                    Nome completo <span className="auth-label-note">(opcional)</span>
                  </label>
                  <input
                    id="invite-full-name"
                    className="auth-input auth-input--no-icon"
                    type="text"
                    placeholder="Nome completo"
                    autoComplete="name"
                    value={fullName}
                    onChange={(event) => setFullName(event.target.value)}
                  />
                </div>

                <div className="auth-field">
                  <label className="auth-label" htmlFor="invite-password">
                    Senha
                  </label>
                  <div className="auth-input-wrap">
                    <span className="material-symbols-outlined auth-input-icon" aria-hidden="true">
                      lock
                    </span>
                    <input
                      id="invite-password"
                      className="auth-input"
                      type="password"
                      placeholder="********"
                      autoComplete="new-password"
                      required
                      value={password}
                      onChange={(event) => setPassword(event.target.value)}
                      aria-invalid={error !== null ? "true" : undefined}
                      aria-describedby={error !== null ? "invite-error" : undefined}
                    />
                  </div>
                </div>

                <div className="auth-field">
                  <label className="auth-label" htmlFor="invite-confirm-password">
                    Confirmar senha
                  </label>
                  <div className="auth-input-wrap">
                    <span className="material-symbols-outlined auth-input-icon" aria-hidden="true">
                      lock
                    </span>
                    <input
                      id="invite-confirm-password"
                      className="auth-input"
                      type="password"
                      placeholder="********"
                      autoComplete="new-password"
                      required
                      value={confirmPassword}
                      onChange={(event) => setConfirmPassword(event.target.value)}
                      aria-invalid={confirmError !== null || error !== null ? "true" : undefined}
                      aria-describedby={
                        confirmError !== null
                          ? "invite-confirm-error"
                          : error !== null
                            ? "invite-error"
                            : undefined
                      }
                    />
                  </div>
                  {confirmError && (
                    <p id="invite-confirm-error" className="auth-field-error" role="alert">
                      {confirmError}
                    </p>
                  )}
                </div>

                <div className="auth-actions">
                  <button type="submit" className="auth-btn auth-btn--primary" disabled={loading}>
                    <span className="material-symbols-outlined auth-btn-icon" aria-hidden="true">
                      person_check
                    </span>
                    {loading ? "Ativando..." : "Ativar conta"}
                  </button>
                </div>
              </form>
            </>
          )}
        </section>
      </main>
      <footer className="auth-footer">
        <strong>NIC-Labs</strong> - Plataforma interna
      </footer>
    </div>
  );
}
