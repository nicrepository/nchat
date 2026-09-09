import { useMemo, useRef } from "react";

/**
 * The AbortControllers of this conversation's targeted reads, one per key.
 *
 * Every authoritative refetch a realtime event triggers is registered here so a
 * target change or an unmount can abort all of them at once: an answer about
 * channel A must never be applied to channel B. Starting a second request under
 * the same key aborts the first, which is what makes a redelivered event cost
 * one request rather than two.
 */
export interface RequestRegistry {
  /** Begins a request under `key`, aborting whatever was in flight under it. */
  start(key: string): AbortController;
  /** Aborts and forgets the request under `key`, if any. */
  cancel(key: string): void;
  /** Forgets a finished request, unless `key` has already been taken over. */
  finish(key: string, controller: AbortController): void;
  has(key: string): boolean;
  abortAll(): void;
}

export function useRequestRegistry(): RequestRegistry {
  const controllers = useRef<Map<string, AbortController>>(new Map());
  return useMemo<RequestRegistry>(
    () => ({
      start(key) {
        controllers.current.get(key)?.abort();
        const controller = new AbortController();
        controllers.current.set(key, controller);
        return controller;
      },
      cancel(key) {
        controllers.current.get(key)?.abort();
        controllers.current.delete(key);
      },
      finish(key, controller) {
        if (controllers.current.get(key) === controller) controllers.current.delete(key);
      },
      has: (key) => controllers.current.has(key),
      abortAll() {
        for (const controller of controllers.current.values()) controller.abort();
        controllers.current.clear();
      },
    }),
    [],
  );
}

/** An abort is the expected end of a request the client itself cancelled. */
export function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}
