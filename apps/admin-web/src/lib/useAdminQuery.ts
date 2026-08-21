import { useCallback, useEffect, useState } from "react";

import { AdminApiError } from "../api/client";

/**
 * How a load ended.
 *
 * `forbidden` is separate from `error` because they mean different things to
 * the person reading the screen: one is a permission they do not have, the
 * other is something broken. `network` is separate from both because retrying
 * is the right response to exactly one of the three.
 */
export type QueryStatus = "loading" | "ready" | "forbidden" | "network" | "error";

export interface AdminQuery<T> {
  status: QueryStatus;
  data: T | null;
  message: string;
  reload: () => void;
}

export function classify(error: unknown): { status: QueryStatus; message: string } {
  if (error instanceof AdminApiError) {
    if (error.status === 403) {
      return { status: "forbidden", message: "Você não tem permissão para esta seção." };
    }
    if (error.status === 0) {
      return {
        status: "network",
        message: "Falha de rede. Verifique a conexão e tente novamente.",
      };
    }
    if (error.status === 503) {
      return { status: "error", message: "O serviço administrativo está indisponível." };
    }
    return { status: "error", message: error.message || "Não foi possível carregar os dados." };
  }
  return { status: "error", message: "Não foi possível carregar os dados." };
}

/** What one completed load produced, tagged with the load it came from. */
interface Settled<T> {
  load: (signal: AbortSignal) => Promise<T>;
  attempt: number;
  status: QueryStatus;
  data: T | null;
  message: string;
}

/**
 * Runs one loader and keeps its result, without ever letting an older answer
 * overwrite a newer one.
 *
 * Three things keep that true, and each covers a case the others do not:
 *
 *   - the AbortSignal cancels the request the browser has in flight, which is
 *     what stops a typed-over search from finishing at all;
 *   - a `cancelled` flag set synchronously in the effect cleanup drops any
 *     result that still arrives. A response can already be in the microtask
 *     queue when the abort is issued, so the abort alone does not guarantee
 *     that the last request to *start* is the last one to *land*;
 *   - the stored result is tagged with the loader and attempt that produced it,
 *     and the status is derived during render by comparing that tag with the
 *     current one. So the moment the inputs change, the hook reports `loading`
 *     without an effect having to write that state — the transition is a
 *     consequence of the inputs, not a second render pass.
 *
 * `load` must be stable — wrap it in useCallback with the inputs it reads.
 * Changing it is what starts a new load, so an unstable one loops.
 */
export function useAdminQuery<T>(load: (signal: AbortSignal) => Promise<T>): AdminQuery<T> {
  const [settled, setSettled] = useState<Settled<T> | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    let cancelled = false;

    load(controller.signal)
      .then((data) => {
        if (cancelled) return;
        setSettled({ load, attempt, status: "ready", data, message: "" });
      })
      .catch((error: unknown) => {
        // An aborted request is not a failure: it was replaced on purpose, and
        // showing an error for it would flash a message the operator caused by
        // typing.
        if (cancelled || controller.signal.aborted) return;
        const resolved = classify(error);
        setSettled({
          load,
          attempt,
          status: resolved.status,
          data: null,
          message: resolved.message,
        });
      });

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [load, attempt]);

  const reload = useCallback(() => setAttempt((value) => value + 1), []);

  const fresh = settled !== null && settled.load === load && settled.attempt === attempt;
  return {
    status: fresh ? settled.status : "loading",
    data: fresh ? settled.data : null,
    message: fresh ? settled.message : "",
    reload,
  };
}
