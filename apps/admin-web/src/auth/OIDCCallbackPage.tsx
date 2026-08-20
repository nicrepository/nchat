import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";

import { createAdminSession, type AdminBootstrap } from "../api/adminApi";
import { AdminApiError } from "../api/client";
import { exchangeOIDCCode } from "../api/authApi";
import { useAdminSession } from "../session/useAdminSession";

/**
 * One in-flight exchange, remembered with the code that started it.
 *
 * The code is what makes this more than a boolean flag: react-router keeps this
 * element mounted when only the search string changes, so a second callback
 * must start its own exchange instead of reading the first one's result.
 */
interface ExchangeAttempt {
  code: string;
  result: Promise<AdminBootstrap>;
}

/**
 * Completes a single-sign-on return.
 *
 * The only value read from the URL is the one-time exchange code, and it is
 * sent straight back to auth-service — it is never used to build a destination.
 * Where the console goes afterwards is a fixed in-app route, so a crafted
 * callback URL cannot turn this screen into an open redirect.
 *
 * # Why the exchange lives in a ref
 *
 * An OIDC authorization code may be redeemed once. React's StrictMode runs
 * every effect as setup → cleanup → setup, so an effect that calls the exchange
 * in its body redeems the code twice and the second attempt comes back
 * `invalid_grant` — which would break administrative SSO in development, where
 * StrictMode is on (see src/main.tsx).
 *
 * A plain "already started" flag does not fix it: the first setup would own the
 * only promise, its cleanup would mark that subscription dead, and the second
 * setup — the one actually mounted — would subscribe to nothing and wait
 * forever.
 *
 * So the *operation* is made one-shot and the *subscription* is not. The
 * promise is created once, in a ref, and every setup attaches its own handlers
 * to that same promise; cleanup cancels only its own handlers. The code is
 * redeemed exactly once and the live effect still sees the result.
 *
 * This is deliberately a client lifecycle fix. The authorization code stays
 * single-use server-side: nothing here caches an access token against a code,
 * writes a code to Web Storage, or asks the backend to tolerate a replay.
 */
export default function OIDCCallbackPage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const { adopt } = useAdminSession();
  const [error, setError] = useState("");
  const attemptRef = useRef<ExchangeAttempt | null>(null);

  const code = params.get("code") ?? "";
  // A callback with no code is a malformed return, not a failed exchange. It is
  // derived during render rather than pushed into state from the effect, so the
  // message appears on the first paint.
  const message = error !== "" ? error : code === "" ? "Retorno de SSO inválido." : "";

  useEffect(() => {
    if (code === "") return;

    if (attemptRef.current?.code !== code) {
      attemptRef.current = {
        code,
        result: exchangeOIDCCode(code).then((accessToken) => createAdminSession(accessToken)),
      };
    }
    const attempt = attemptRef.current;

    // Scoped to this setup only. Cleanup silences these handlers; it never
    // cancels the exchange, which the next setup is about to read.
    let active = true;
    attempt.result
      .then((bootstrap) => {
        if (!active) return;
        adopt(bootstrap);
        // Replace, not push: the one-time exchange code must not stay in the
        // address bar or in history, and the back button must not land on a
        // callback that can no longer be replayed. The destination is a fixed
        // in-app route, never a value read from the URL.
        void navigate("/", { replace: true });
      })
      .catch((caught: unknown) => {
        if (!active) return;
        setError(describe(caught));
      });

    return () => {
      active = false;
    };
  }, [code, adopt, navigate]);

  return (
    <main className="admin-login" id="admin-main">
      <h1>Concluindo login</h1>
      {message === "" ? (
        <p role="status">Validando retorno do provedor de identidade…</p>
      ) : (
        <p role="alert" className="admin-alert">
          {message}
        </p>
      )}
    </main>
  );
}

/**
 * A 403 here means the identity was proven and the account still has no
 * administrative authority — worth saying, because retrying SSO will never fix
 * it. Everything else is one generic message: the provider's own wording can
 * carry detail an administrator should not be shown.
 */
function describe(error: unknown): string {
  if (error instanceof AdminApiError && error.status === 403) {
    return "Sua conta não possui acesso administrativo.";
  }
  return "Não foi possível concluir o login com SSO.";
}
