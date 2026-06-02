import { type FormEvent, useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";

import { ApiRequestError } from "../lib/api";
import "../tokens.css";
import "./auth.css";
import { resetPassword } from "./authApi";

const PASSWORD_MISMATCH_ERROR = "As senhas não coincidem.";

function readToken(search: string): string {
  return new URLSearchParams(search).get("token")?.trim() ?? "";
}

function resetErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError && error.code === "invalid_token") {
    return "Link inválido ou expirado. Solicite uma nova redefinição.";
  }

  if (error instanceof ApiRequestError && error.status === 400) {
    return "A senha não atende aos requisitos de segurança.";
  }

  return "Não foi possível redefinir a senha. Tente novamente.";
}

export default function ResetPasswordPage() {
  const location = useLocation();
  const navigate = useNavigate();
  const [token, setToken] = useState(() => readToken(location.search));
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
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
          <section className="auth-card" aria-labelledby="reset-invalid-title">
            <div className="auth-header">
              <img className="auth-logo" src="/assets/nic-labs-logo.png" alt="NIC Chat" />
              <h1 id="reset-invalid-title" className="auth-title">
                Link inválido
              </h1>
              <p className="auth-subtitle">Este link de redefinição é inválido ou expirou.</p>
            </div>
            <div className="auth-actions">
              <Link to="/forgot-password" className="auth-btn auth-btn--primary">
                Solicitar novo link
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
    setConfirmError(null);
    setError(null);

    if (newPassword !== confirmPassword) {
      setConfirmError(PASSWORD_MISMATCH_ERROR);
      return;
    }

    if (token === "") {
      setError("Link inválido ou expirado. Solicite uma nova redefinição.");
      return;
    }

    setLoading(true);
    try {
      await resetPassword(token, newPassword);
      setToken("");
      setSuccess(true);
    } catch (err) {
      setError(resetErrorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="auth-root">
      <main className="auth-main">
        <section className="auth-card" aria-labelledby="reset-title">
          <div className="auth-header">
            <img className="auth-logo" src="/assets/nic-labs-logo.png" alt="NIC Chat" />
            <h1 id="reset-title" className="auth-title">
              Redefinir senha
            </h1>
            <p className="auth-subtitle">Escolha uma nova senha segura</p>
          </div>

          {success ? (
            <>
              <div className="auth-alert auth-alert--success" role="status">
                Senha redefinida com sucesso.
              </div>
              <Link to="/login" className="auth-link">
                Ir para entrar
              </Link>
            </>
          ) : (
            <>
              {error && (
                <div id="reset-error" className="auth-alert auth-alert--error" role="alert">
                  {error}
                </div>
              )}
              <form className="auth-form" onSubmit={handleSubmit} noValidate>
                <div className="auth-field">
                  <label className="auth-label" htmlFor="reset-password">
                    Nova senha
                  </label>
                  <div className="auth-input-wrap">
                    <span className="material-symbols-outlined auth-input-icon" aria-hidden="true">
                      lock
                    </span>
                    <input
                      id="reset-password"
                      className="auth-input"
                      type="password"
                      placeholder="********"
                      autoComplete="new-password"
                      required
                      value={newPassword}
                      onChange={(event) => setNewPassword(event.target.value)}
                      aria-invalid={error !== null ? "true" : undefined}
                      aria-describedby={error !== null ? "reset-error" : undefined}
                    />
                  </div>
                </div>

                <div className="auth-field">
                  <label className="auth-label" htmlFor="reset-confirm-password">
                    Confirmar senha
                  </label>
                  <div className="auth-input-wrap">
                    <span className="material-symbols-outlined auth-input-icon" aria-hidden="true">
                      lock
                    </span>
                    <input
                      id="reset-confirm-password"
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
                          ? "reset-confirm-error"
                          : error !== null
                            ? "reset-error"
                            : undefined
                      }
                    />
                  </div>
                  {confirmError && (
                    <p id="reset-confirm-error" className="auth-field-error" role="alert">
                      {confirmError}
                    </p>
                  )}
                </div>

                <div className="auth-actions">
                  <button type="submit" className="auth-btn auth-btn--primary" disabled={loading}>
                    <span className="material-symbols-outlined auth-btn-icon" aria-hidden="true">
                      password
                    </span>
                    {loading ? "Redefinindo..." : "Redefinir senha"}
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
