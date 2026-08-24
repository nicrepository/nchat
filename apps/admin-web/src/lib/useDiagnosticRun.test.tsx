import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { AdminApiError } from "../api/client";
import type { DiagnosticReport } from "../api/integrationsApi";
import { deferred } from "../test/harness";
import { useDiagnosticRun } from "./useDiagnosticRun";

const REPORT: DiagnosticReport = {
  integration: "oidc",
  startedAt: "2026-08-23T11:00:00.000Z",
  status: "passed",
  summary: "ok",
  version: "",
  steps: [],
};

/**
 * Lets every queued microtask run.
 *
 * A settled promise reaches `.then`, `.catch` and `.finally` on separate
 * microtask hops, and these specs are about what each of those does — so they
 * have to wait for all of them rather than for the promise itself.
 */
async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("useDiagnosticRun", () => {
  it("starts nothing on its own", () => {
    const fetcher = vi.fn();
    const { result } = renderHook(() => useDiagnosticRun());
    expect(fetcher).not.toHaveBeenCalled();
    expect(result.current.running).toBe(false);
    expect(result.current.report).toBeNull();
  });

  it("keeps one run at a time and ignores a second press", async () => {
    const pending = deferred<DiagnosticReport>();
    const fetcher = vi.fn(() => pending.promise);
    const { result } = renderHook(() => useDiagnosticRun());

    act(() => result.current.start(fetcher));
    act(() => result.current.start(fetcher));
    expect(fetcher).toHaveBeenCalledTimes(1);

    await act(async () => {
      pending.resolve(REPORT);
      await pending.promise;
    });
    await waitFor(() => expect(result.current.running).toBe(false));
    expect(result.current.report).toEqual(REPORT);
  });

  // Navigating away must stop the outbound work rather than leave it to finish
  // into a component that is gone.
  it("aborts the run in flight when the component unmounts", async () => {
    let captured: AbortSignal | null = null;
    const pending = deferred<DiagnosticReport>();
    const { result, unmount } = renderHook(() => useDiagnosticRun());

    act(() =>
      result.current.start((signal) => {
        captured = signal;
        return pending.promise;
      }),
    );
    expect(captured!.aborted).toBe(false);
    unmount();
    expect(captured!.aborted).toBe(true);
  });

  it("reports a failure the operator can act on", async () => {
    const { result } = renderHook(() => useDiagnosticRun());
    await act(async () => {
      result.current.start(() =>
        Promise.reject(new AdminApiError(429, "rate_limited", "too many requests")),
      );
    });
    await waitFor(() => expect(result.current.failure).not.toBe(""));
    expect(result.current.report).toBeNull();
  });

  it("says nothing about a run that was abandoned", async () => {
    const pending = deferred<DiagnosticReport>();
    const { result } = renderHook(() => useDiagnosticRun());
    let captured: AbortController | null = null;

    act(() =>
      result.current.start((signal) => {
        captured = { abort: () => undefined, signal } as unknown as AbortController;
        return pending.promise;
      }),
    );
    expect(captured).not.toBeNull();

    await act(async () => {
      pending.reject(new AdminApiError(0, "network_error", "Falha de rede"));
      await pending.promise.catch(() => undefined);
    });
    await waitFor(() => expect(result.current.failure).not.toBe(""));
  });

  // The regression this hook's generation counter exists for.
  //
  // A diagnostic result carries no integration id, so a late report from an
  // abandoned run would be painted under whichever card is open — reading as
  // that integration's diagnosis of itself. The sequence below is exactly the
  // one that produced it: A starts, the operator switches, B starts, and only
  // then does A settle.
  it("never lets an abandoned run write over the run that replaced it", async () => {
    const runA = deferred<DiagnosticReport>();
    const runB = deferred<DiagnosticReport>();
    let signalA: AbortSignal | null = null;
    const { result } = renderHook(() => useDiagnosticRun());

    act(() =>
      result.current.start((signal) => {
        signalA = signal;
        return runA.promise;
      }),
    );
    expect(result.current.running).toBe(true);

    // The operator opens another integration.
    act(() => result.current.reset());
    expect(signalA!.aborted).toBe(true);
    expect(result.current.running).toBe(false);
    expect(result.current.report).toBeNull();
    expect(result.current.failure).toBe("");

    // The next diagnostic must be startable at once: the abandoned run must not
    // still be holding the single-run slot.
    act(() => result.current.start(() => runB.promise));
    expect(result.current.running).toBe(true);

    // A settles late. Neither its result nor its finally may touch B.
    runA.resolve(REPORT);
    await flush();
    expect(result.current.report).toBeNull();
    expect(result.current.failure).toBe("");
    expect(result.current.running).toBe(true);

    const reportB: DiagnosticReport = { ...REPORT, integration: "smtp", summary: "b" };
    runB.resolve(reportB);
    await flush();
    expect(result.current.report).toEqual(reportB);
    expect(result.current.running).toBe(false);
  });

  // The same race with the abandoned run failing instead of succeeding: an
  // abort is not a finding, and it must not surface as one on the next card.
  it("never shows an abandoned run's failure against the run that replaced it", async () => {
    const runA = deferred<DiagnosticReport>();
    const runB = deferred<DiagnosticReport>();
    const { result } = renderHook(() => useDiagnosticRun());

    act(() => result.current.start(() => runA.promise));
    act(() => result.current.reset());
    act(() => result.current.start(() => runB.promise));

    runA.reject(new AdminApiError(429, "rate_limited", "too many requests"));
    await flush();
    expect(result.current.failure).toBe("");
    expect(result.current.running).toBe(true);

    runB.resolve(REPORT);
    await flush();
    expect(result.current.report).toEqual(REPORT);
    expect(result.current.failure).toBe("");
  });

  // Unmounting is the other way a run is abandoned, and it must not produce a
  // state update on a component that is gone.
  it("writes nothing after the component unmounts", async () => {
    const pending = deferred<DiagnosticReport>();
    const { result, unmount } = renderHook(() => useDiagnosticRun());

    act(() => result.current.start(() => pending.promise));
    unmount();

    pending.resolve(REPORT);
    await flush();
    // No React warning, and the last rendered snapshot never gained a report.
    expect(result.current.report).toBeNull();
  });

  it("clears a previous result on reset", async () => {
    const { result } = renderHook(() => useDiagnosticRun());
    await act(async () => {
      result.current.start(() => Promise.resolve(REPORT));
    });
    await waitFor(() => expect(result.current.report).toEqual(REPORT));

    act(() => result.current.reset());
    expect(result.current.report).toBeNull();
    expect(result.current.failure).toBe("");
  });
});
