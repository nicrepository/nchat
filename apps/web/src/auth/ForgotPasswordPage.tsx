import { type FormEvent, useState } from "react";
import { Link } from "react-router";

import "../tokens.css";
import "./auth.css";
import { forgotPassword } from "./authApi";

const FORGOT_SUCCESS =
  "Se o e-mail estiver cadastrado, enviaremos instruções para redefinir sua senha.";

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState("");
  const [loading, setLoading] = useState(false);
  const [submitted, setSubmitted] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);

    try {
      await forgotPassword(email);
    } catch {
      // Intentionally keep the same success state to avoid account enumeration.
    } finally {
      setLoading(false);
      setSubmitted(true);
    }
  }

  return (
    <div className="auth-root">
      <main className="auth-main">
        <section className="auth-card" aria-labelledby="forgot-title">
          <div className="auth-header">
            <img className="auth-logo" src="/assets/nic-labs-logo.png" alt="NIC Chat" />
            <h1 id="forgot-title" className="auth-title">
              Recuperar senha
            </h1>
            <p className="auth-subtitle">Informe o e-mail associado à sua conta</p>
          </div>

          {submitted ? (
            <>
              <div className="auth-alert auth-alert--success" role="status">
                {FORGOT_SUCCESS}
              </div>
              <Link to="/login" className="auth-link">
                Voltar para entrar
              </Link>
            </>
          ) : (
            <form className="auth-form" onSubmit={handleSubmit} noValidate>
              <div className="auth-field">
                <label className="auth-label" htmlFor="forgot-email">
                  E-mail corporativo
                </label>
                <div className="auth-input-wrap">
                  <span className="material-symbols-outlined auth-input-icon" aria-hidden="true">
                    mail
                  </span>
                  <input
                    id="forgot-email"
                    className="auth-input"
                    type="email"
                    placeholder="nome@nic-labs.com"
                    autoComplete="username"
                    required
                    value={email}
                    onChange={(event) => setEmail(event.target.value)}
                  />
                </div>
              </div>

              <div className="auth-actions">
                <button type="submit" className="auth-btn auth-btn--primary" disabled={loading}>
                  <span className="material-symbols-outlined auth-btn-icon" aria-hidden="true">
                    outgoing_mail
                  </span>
                  {loading ? "Enviando..." : "Enviar instruções"}
                </button>
              </div>

              <Link to="/login" className="auth-link">
                Voltar para entrar
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
