import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import { destroyAdminSession, fetchAdminBootstrap, type AdminBootstrap } from "../api/adminApi";
import { AdminApiError } from "../api/client";
import { AdminSessionContext, type AdminSessionStatus } from "./AdminSessionContext";

const SUPERUSER = "admin.superuser";

interface AdminSessionProviderProps {
  children: ReactNode;
}

function statusForError(error: unknown): { status: AdminSessionStatus; message: string } {
  if (error instanceof AdminApiError) {
    switch (error.status) {
      case 401:
        return { status: "unauthenticated", message: "" };
      case 403:
        return { status: "forbidden", message: "" };
      case 503:
        return {
          status: "unavailable",
          message: "O console administrativo está indisponível no momento.",
        };
      case 0:
        return { status: "error", message: "Falha de rede ao carregar o console." };
      default:
        // Any other status — 500 included — is an error the console cannot
        // interpret. It never becomes "signed in".
        return { status: "error", message: "Não foi possível carregar o console." };
    }
  }
  return { status: "error", message: "Não foi possível carregar o console." };
}

/**
 * Loads the administrative session and holds it for the tree below.
 *
 * Everything the console knows about the current administrator comes from one
 * server response. There is no client-side inference: no capability is derived
 * from a route, no environment from a hostname, no identity from a stored
 * token.
 *
 * # Why a generation counter
 *
 * More than one thing can decide what the session is, and they overlap in time.
 * The provider starts a bootstrap request on mount; meanwhile the single
 * sign-on return can finish its exchange and call adopt() with a session that
 * demonstrably exists. Without an ordering rule the older request wins by
 * arriving last: its 401 — correct when it was sent, stale by the time it
 * lands — would overwrite a live session with "unauthenticated".
 *
 * So every decision that deliberately establishes a newer session state —
 * adopt, signOut, reload — advances a generation, and every asynchronous
 * result carries the generation it was started under. A result from an older
 * generation is dropped rather than applied. The rule is one sentence: an
 * operation may only change the state if it still belongs to the current
 * generation.
 *
 * An AbortSignal would not be enough on its own. A response can already be in
 * the microtask queue when the abort is issued, and there is more than one way
 * for a result to become stale, so the generation check stays the invariant
 * whether or not a request is also cancelled.
 */
export default function AdminSessionProvider({ children }: AdminSessionProviderProps) {
  const [status, setStatus] = useState<AdminSessionStatus>("loading");
  const [bootstrap, setBootstrap] = useState<AdminBootstrap | null>(null);
  const [message, setMessage] = useState("");
  const [attempt, setAttempt] = useState(0);
  const generationRef = useRef(0);

  useEffect(() => {
    const generation = generationRef.current;
    let cancelled = false;
    // `cancelled` covers unmount; the generation covers everything that
    // establishes a newer session while this request is still in flight.
    const isStale = () => cancelled || generation !== generationRef.current;

    fetchAdminBootstrap()
      .then((loaded) => {
        if (isStale()) return;
        setBootstrap(loaded);
        setMessage("");
        setStatus("ready");
      })
      .catch((error: unknown) => {
        if (isStale()) return;
        const resolved = statusForError(error);
        setBootstrap(null);
        setMessage(resolved.message);
        setStatus(resolved.status);
      });
    return () => {
      cancelled = true;
    };
  }, [attempt]);

  // `loading` is set here rather than inside the effect: the effect's job is to
  // talk to the server, and setting state synchronously in its body is what
  // produces the cascading re-render React warns about. The initial state is
  // already `loading`, so a first mount needs nothing.
  const reload = useCallback(() => {
    generationRef.current += 1;
    setStatus("loading");
    setAttempt((value) => value + 1);
  }, []);

  const adopt = useCallback((loaded: AdminBootstrap) => {
    generationRef.current += 1;
    setBootstrap(loaded);
    setMessage("");
    setStatus("ready");
  }, []);

  const signOut = useCallback(async () => {
    // Advanced before the request, not after: a bootstrap that is already in
    // flight must not be able to resurrect the session with a 200 that arrives
    // after the operator asked to leave.
    generationRef.current += 1;
    try {
      await destroyAdminSession();
    } catch {
      // A failed revocation is not re-thrown, and not shown. There is no
      // action an administrator could take on it, and leaving the promise
      // rejected would surface only as an unhandled rejection. The console
      // drops its session state below either way, and the server-side row
      // still carries its own idle and absolute deadlines, so a revocation
      // this request failed to record still expires on its own.
    } finally {
      // The console drops its own state whatever the server answered: a browser
      // must never be left showing an administrative shell after the operator
      // asked to leave it.
      setBootstrap(null);
      setMessage("");
      setStatus("unauthenticated");
    }
  }, []);

  const value = useMemo(() => {
    const held = new Set(bootstrap?.capabilities ?? []);
    return {
      status,
      bootstrap,
      message,
      reload,
      adopt,
      signOut,
      can: (capability: string) => held.has(capability) || held.has(SUPERUSER),
    };
  }, [status, bootstrap, message, reload, adopt, signOut]);

  return <AdminSessionContext.Provider value={value}>{children}</AdminSessionContext.Provider>;
}
