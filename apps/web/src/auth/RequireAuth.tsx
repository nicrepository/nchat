import { type ReactNode, useEffect, useRef, useState } from "react";
import { Navigate } from "react-router-dom";

import { clearTokens, getAccessToken, getRefreshToken, setTokens } from "../lib/authSession";
import { refresh } from "./authApi";

type AuthState = "authenticated" | "checking" | "unauthenticated";

function initialAuthState(): AuthState {
  if (getAccessToken() !== null) return "authenticated";
  if (getRefreshToken() !== null) return "checking";
  return "unauthenticated";
}

interface RequireAuthProps {
  children: ReactNode;
}

export default function RequireAuth({ children }: RequireAuthProps) {
  const [authState, setAuthState] = useState<AuthState>(initialAuthState);
  const refreshStarted = useRef(false);

  useEffect(() => {
    if (authState !== "checking" || refreshStarted.current) return;

    refreshStarted.current = true;
    let cancelled = false;
    const refreshToken = getRefreshToken();

    if (refreshToken === null) {
      clearTokens();
      void Promise.resolve().then(() => {
        if (!cancelled) setAuthState("unauthenticated");
      });
      return () => {
        cancelled = true;
      };
    }

    refresh(refreshToken)
      .then(({ accessToken, refreshToken: nextRefreshToken }) => {
        if (cancelled) return;
        setTokens(accessToken, nextRefreshToken);
        setAuthState("authenticated");
      })
      .catch(() => {
        if (cancelled) return;
        clearTokens();
        setAuthState("unauthenticated");
      });

    return () => {
      cancelled = true;
    };
  }, [authState]);

  if (authState === "checking") return null;
  if (authState === "unauthenticated") return <Navigate to="/login" replace />;

  return <>{children}</>;
}
