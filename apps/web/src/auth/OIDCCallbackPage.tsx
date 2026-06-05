import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";

import { setTokens } from "../lib/authSession";
import "../tokens.css";
import "./auth.css";
import { oidcExchange } from "./authApi";

const SSO_ERROR = "Não foi possível concluir o login com SSO.";

export default function OIDCCallbackPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const code = searchParams.get("code")?.trim() ?? "";
  const [error, setError] = useState<string | null>(() => (code ? null : SSO_ERROR));

  useEffect(() => {
    let cancelled = false;
    if (!code) {
      return;
    }

    async function exchangeCode() {
      try {
        const result = await oidcExchange(code);
        if (cancelled) {
          return;
        }
        setTokens(result.accessToken, result.refreshToken);
        window.history.replaceState({}, "", "/oidc-callback");
        navigate("/", { replace: true });
      } catch {
        if (!cancelled) {
          window.history.replaceState({}, "", "/oidc-callback");
          setError(SSO_ERROR);
        }
      }
    }

    void exchangeCode();

    return () => {
      cancelled = true;
    };
  }, [code, navigate]);

  return (
    <div className="auth-root">
      <main className="auth-main">
        <section className="auth-card" aria-labelledby="oidc-callback-title">
          <div className="auth-header">
            <img className="auth-logo" src="/assets/nic-labs-logo.png" alt="NIC Chat" />
            <h1 id="oidc-callback-title" className="auth-title">
              Entrando com SSO
            </h1>
            <p className="auth-subtitle">Validando o acesso corporativo</p>
          </div>

          {error ? (
            <div className="auth-form">
              <div className="auth-alert auth-alert--error" role="alert">
                {error}
              </div>
              <Link to="/login" className="auth-btn auth-btn--secondary">
                Voltar ao login
              </Link>
            </div>
          ) : (
            <div className="auth-alert auth-alert--success" role="status">
              Concluindo autenticação...
            </div>
          )}
        </section>
      </main>
      <footer className="auth-footer">
        <strong>NIC-Labs</strong> - Plataforma interna
      </footer>
    </div>
  );
}
