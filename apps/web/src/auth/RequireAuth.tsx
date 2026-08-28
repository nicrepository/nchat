import { type ReactNode, useEffect, useRef, useState } from "react";
import { Navigate, useLocation } from "react-router";

import {
  clearTokens,
  getAccessToken,
  isAuthenticated,
  onAuthChange,
  setTokens,
} from "../lib/authSession";
import { refresh } from "./authApi";

type AuthState = "authenticated" | "checking" | "unauthenticated";

function initialAuthState(): AuthState {
  if (getAccessToken() !== null) return "authenticated";
  // No access token: attempt silent refresh via the HttpOnly RT cookie.
  // We can't read the cookie from JS; the browser sends it automatically.
  return "checking";
}

interface RequireAuthProps {
  children: ReactNode;
}

export default function RequireAuth({ children }: RequireAuthProps) {
  const [authState, setAuthState] = useState<AuthState>(initialAuthState);
  const refreshPromise = useRef<ReturnType<typeof refresh> | null>(null);
  const location = useLocation();

  useEffect(() => {
    return onAuthChange(() => {
      setAuthState(isAuthenticated() ? "authenticated" : "unauthenticated");
    });
  }, []);

  useEffect(() => {
    if (authState !== "checking") return;

    const pendingRefresh = refreshPromise.current ?? refresh();
    refreshPromise.current = pendingRefresh;
    let cancelled = false;

    pendingRefresh
      .then(({ accessToken }) => {
        if (cancelled) return;
        setTokens(accessToken);
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
