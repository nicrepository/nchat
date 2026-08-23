import { useCallback, useEffect, useRef, useState } from "react";

/**
 * A value that only ever moves forward in time.
 *
 * The operational screens have more than one thing that can replace what is on
 * screen: a periodic background refresh, a manual refresh button, and a full
 * reload. They overlap freely, and HTTP gives no guarantee that the request
 * that started last is the one that lands last. Without an explicit rule, a
 * slow earlier response can arrive after a fast later one and put an older
 * collection back on screen — with an older timestamp — which is worse than
 * showing nothing, because an operator reads it as current.
 *
 * The rule, and the whole of this hook: **the latest run wins**. Every run is
 * stamped with a generation on the way out, and a result may only be applied
 * if its generation is still the newest. A superseded run neither applies its
 * value nor reports its failure — it is silent, because it is describing a
 * question nobody is asking any more.
 *
 * Why here rather than in `useAutoRefresh`: the manual refresh does not go
 * through the interval at all, so a guard living in the timer would order the
 * periodic refreshes against each other and leave the periodic-versus-manual
 * race — the one that actually bites — untouched. Ordering belongs where the
 * value lives, and that is here.
 *
 * Why a generation counter rather than AbortController: the racing requests
 * are separate calls to the same endpoint rather than one call replacing
 * another, so there is no request to cancel — and even with cancellation, a
 * response already resolved into the microtask queue still lands. The counter
 * is what makes the outcome deterministic; abort would only save the bytes.
 * `useAdminQuery` already aborts the *initial* load, and pairs its
 * AbortController with exactly this kind of guard for exactly this reason.
 */
export interface LatestResult<T> {
  /** The newest value that has been applied, or null before the first one. */
  value: T | null;
  /**
   * Runs `fetcher` and applies its result only if no newer run has started.
   *
   * Resolves when the value was applied and when the run was superseded — the
   * two are indistinguishable to a caller on purpose, because neither is
   * something to report. Rejects only when the run that failed is still the
   * newest, so a caller's error handling describes the current state and never
   * a request that has already been replaced.
   *
   * Its identity is stable for the life of the component, so a caller can put
   * it in a dependency array. That matters here: the pages build their refresh
   * callback from it and hand that to the interval, and an identity that
   * changed whenever `value` did would tear the timer down and rebuild it on
   * every applied refresh.
   */
  run: (fetcher: () => Promise<T>) => Promise<void>;
  /**
   * Drops the current value and supersedes every run in flight.
   *
   * Used when a full reload takes over: without it, a background refresh that
   * started before the reload could land after it and overwrite fresh data
   * with a collection from before.
   *
   * Stable for the life of the component, like `run`.
   */
  discard: () => void;
}

export function useLatestResult<T>(): LatestResult<T> {
  const [value, setValue] = useState<T | null>(null);
  const generation = useRef(0);

  // Unmounting supersedes everything in flight, so a late response cannot
  // write state into a screen that is gone.
  useEffect(() => {
    return () => {
      generation.current += 1;
    };
  }, []);

  const run = useCallback(async (fetcher: () => Promise<T>) => {
    const started = generation.current + 1;
    generation.current = started;
    try {
      const result = await fetcher();
      if (started === generation.current) setValue(result);
    } catch (cause) {
      if (started === generation.current) throw cause;
    }
  }, []);

  const discard = useCallback(() => {
    generation.current += 1;
    setValue(null);
  }, []);

  return { value, run, discard };
}
