import { type ReactNode, useEffect, useRef, useState } from "react";
import { Navigate, useLocation } from "react-router-dom";

import {
  clearTokens,
  getAccessToken,
  getRefreshToken,
  isAuthenticated,
  onAuthChange,
  setTokens,
} from "../lib/authSession";
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
  const location = useLocation();

  // React to setTokens / clearTokens called anywhere after mount.
  useEffect(() => {
    return onAuthChange(() => {
      setAuthState(isAuthenticated() ? "authenticated" : "unauthenticated");
    });
  }, []);

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
  if (authState === "unauthenticated")
    return <Navigate to="/login" state={{ from: location.pathname }} replace />;

  return <>{children}</>;
}
