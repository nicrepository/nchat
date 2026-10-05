import { useEffect, useRef, useState } from "react";

/**
 * At most one write at a time for a "Nova conversa" flow (issue #1023).
 *
 * Pessoa, Grupo and Canal each make a different write, but guard it the same
 * way: a ref that stops a second click fired in the same tick (state updates
 * are asynchronous, so `pending` alone cannot), an abort when the dialog
 * unmounts, and a mounted guard so a late answer never touches state or calls
 * back into a parent that already closed the dialog.
 *
 * Error copy is the caller's: `toMessage` turns a failure into a generic
 * sentence before it reaches state, so server detail is never stored here.
 * `onPendingChange` lets the dialog shell hold the door shut while a write is
 * in flight without knowing which flow started it.
 */
export function useSingleSubmission(onPendingChange: (pending: boolean) => void) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const inFlightRef = useRef(false);
  const abortRef = useRef<AbortController>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      abortRef.current?.abort();
    };
  }, []);

  function markPending(next: boolean) {
    setPending(next);
    onPendingChange(next);
  }

  async function execute<T>(
    action: (signal: AbortSignal) => Promise<T>,
    onSuccess: (result: T) => void,
    toMessage: (error: unknown) => string,
  ) {
    const controller = new AbortController();
    abortRef.current = controller;
    try {
      const result = await action(controller.signal);
      if (mountedRef.current) onSuccess(result);
    } catch (failure) {
      if (mountedRef.current) setError(toMessage(failure));
    } finally {
      inFlightRef.current = false;
      if (mountedRef.current) markPending(false);
    }
  }

  /**
   * Starts `action` unless a write is already in flight. Returns whether it
   * started, so a caller can tie per-attempt UI to the attempt that actually
   * runs. On failure the caller's fields are untouched and a retry is one call.
   */
  function run<T>(
    action: (signal: AbortSignal) => Promise<T>,
    onSuccess: (result: T) => void,
    toMessage: (error: unknown) => string,
  ): boolean {
    if (inFlightRef.current) return false;
    inFlightRef.current = true;
    markPending(true);
    setError("");
    void execute(action, onSuccess, toMessage);
    return true;
  }

  return { pending, error, setError, run };
}
