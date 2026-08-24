import { useCallback, useEffect, useRef, useState } from "react";

import type { DiagnosticReport } from "../api/integrationsApi";
import { classify } from "./useAdminQuery";

/**
 * One integration's diagnostic run.
 *
 * Four properties, and each covers something the others do not:
 *
 *   - nothing runs on render. A diagnostic opens outbound connections, so it
 *     starts when an operator presses a button and at no other moment. There is
 *     no effect in this hook that fetches;
 *   - a run in flight is abandoned when the component unmounts, and when the
 *     operator moves to another integration. The server cancels the outbound
 *     work with the request, so both stop the connections rather than leaving
 *     them to finish into nothing;
 *   - a second press while one is running is ignored rather than queued. The
 *     server bounds concurrency anyway; this keeps the button from producing a
 *     backlog the operator cannot see;
 *   - **an abandoned run can never write state again.** That is what the
 *     generation counter below is for, and it is the property the abort alone
 *     does not give.
 *
 * # Why a generation counter and not just the AbortController
 *
 * Aborting stops the request; it does not stop the promise that was already
 * settling. A response resolved into the microtask queue still lands after the
 * abort, and a `fetcher` that ignores the signal — a stub, a cached client, a
 * library that swallows it — lands regardless. Without a second guard the
 * sequence
 *
 *     run A starts → reset → run B starts → A's finally → A resolves
 *
 * lets A clear B's `running`, drop B's controller and, worst of all, paint A's
 * report under B's heading. A diagnostic carries no integration id in its
 * result, so that report would read as B's diagnosis of B.
 *
 * The counter makes that impossible by construction: every run captures the
 * generation it was started at, every state write is gated on that generation
 * still being current, and `reset` moves it. One integer, three guards, no
 * state machine.
 */
export interface DiagnosticRun {
  report: DiagnosticReport | null;
  running: boolean;
  failure: string;
  start: (fetcher: (signal: AbortSignal) => Promise<DiagnosticReport>) => void;
  /**
   * Abandons whatever is in flight and clears what is on screen.
   *
   * Called when the operator opens a different integration, so the next card
   * starts empty and can run its own diagnostic immediately.
   */
  reset: () => void;
}

export function useDiagnosticRun(): DiagnosticRun {
  const [report, setReport] = useState<DiagnosticReport | null>(null);
  const [running, setRunning] = useState(false);
  const [failure, setFailure] = useState("");
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);

  // abandon invalidates the run in flight without touching rendered state, so
  // it is safe to call from an unmount cleanup as well as from reset.
  const abandon = useCallback(() => {
    generation.current += 1;
    controller.current?.abort();
    controller.current = null;
  }, []);

  useEffect(() => abandon, [abandon]);

  const start = useCallback((fetcher: (signal: AbortSignal) => Promise<DiagnosticReport>) => {
    if (controller.current !== null) return;
    generation.current += 1;
    const mine = generation.current;
    const active = new AbortController();
    controller.current = active;
    setRunning(true);
    // The previous result goes with the previous question. Leaving it on
    // screen next to "Executando…" invites reading it as the new answer.
    setReport(null);
    setFailure("");

    const current = () => mine === generation.current;
    fetcher(active.signal)
      .then((result) => {
        if (current()) setReport(result);
      })
      .catch((cause: unknown) => {
        // An abandoned run says nothing. It was cancelled on purpose, and an
        // error banner for it would describe a question nobody is asking.
        if (current()) setFailure(classify(cause).message);
      })
      .finally(() => {
        if (!current()) return;
        controller.current = null;
        setRunning(false);
      });
  }, []);

  const reset = useCallback(() => {
    abandon();
    setReport(null);
    setFailure("");
    setRunning(false);
  }, [abandon]);

  return { report, running, failure, start, reset };
}
