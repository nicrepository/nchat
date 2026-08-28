import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { deferred } from "../test/harness";
import { useLatestResult } from "./useLatestResult";

/**
 * The ordering rule, exercised directly.
 *
 * Every response here is held open and settled by hand: a race asserted with
 * timers is a race asserted against the speed of the machine, which is the one
 * property these specs must not have.
 */
describe("useLatestResult", () => {
  it("applies a result while it is still the newest", async () => {
    const { result } = renderHook(() => useLatestResult<string>());
    expect(result.current.value).toBeNull();

    await act(async () => {
      await result.current.run(() => Promise.resolve("first"));
    });
    expect(result.current.value).toBe("first");
  });

  it("discards an earlier response that lands after a later one", async () => {
    const older = deferred<string>();
    const newer = deferred<string>();
    const { result } = renderHook(() => useLatestResult<string>());

    // A starts, then B starts. Both are in flight.
    let a: Promise<void>;
    let b: Promise<void>;
    act(() => {
      a = result.current.run(() => older.promise);
      b = result.current.run(() => newer.promise);
    });

    // B lands first and is applied.
    await act(async () => {
      newer.resolve("newer");
      await b;
    });
    expect(result.current.value).toBe("newer");

    // A lands last, and must not put the older value back.
    await act(async () => {
      older.resolve("older");
      await a;
    });
    expect(result.current.value).toBe("newer");
  });

  it("keeps applying results when they land in the order they started", async () => {
    // The guard must not turn into a lock: a legitimate second refresh has to
    // replace the first.
    const { result } = renderHook(() => useLatestResult<string>());

    await act(async () => {
      await result.current.run(() => Promise.resolve("first"));
    });
    await act(async () => {
      await result.current.run(() => Promise.resolve("second"));
    });
    expect(result.current.value).toBe("second");
  });

  it("reports a failure that is still the newest", async () => {
    const { result } = renderHook(() => useLatestResult<string>());
    const failure = new Error("boom");

    await expect(
      act(async () => {
        await result.current.run(() => Promise.reject(failure));
      }),
    ).rejects.toBe(failure);
  });

  it("keeps the last good value when a run fails", async () => {
    const { result } = renderHook(() => useLatestResult<string>());
    await act(async () => {
      await result.current.run(() => Promise.resolve("good"));
    });

    await act(async () => {
      await result.current.run(() => Promise.reject(new Error("boom"))).catch(() => undefined);
    });
    expect(result.current.value).toBe("good");
  });

  it("says nothing when a superseded run fails", async () => {
    const older = deferred<string>();
    const { result } = renderHook(() => useLatestResult<string>());

    let a: Promise<void>;
    act(() => {
      a = result.current.run(() => older.promise);
      void result.current.run(() => Promise.resolve("newer"));
    });

    // An error banner about a request the page has already replaced describes
    // a question nobody is asking, so a superseded failure resolves quietly.
    const onRejected = vi.fn();
    await act(async () => {
      older.reject(new Error("boom"));
      await a.catch(onRejected);
    });
    expect(onRejected).not.toHaveBeenCalled();
  });

  it("supersedes everything in flight when the value is discarded", async () => {
    const pending = deferred<string>();
    const { result } = renderHook(() => useLatestResult<string>());

    let run: Promise<void>;
    act(() => {
      run = result.current.run(() => pending.promise);
    });
    act(() => result.current.discard());

    await act(async () => {
      pending.resolve("stale");
      await run;
    });
    // A full reload took over. A refresh that started before it must not
    // resurrect the collection it was replacing.
    expect(result.current.value).toBeNull();
  });

  it("does not apply a response that lands after unmount", async () => {
    const pending = deferred<string>();
    const { result, unmount } = renderHook(() => useLatestResult<string>());

    let run: Promise<void>;
    act(() => {
      run = result.current.run(() => pending.promise);
    });
    unmount();

    await act(async () => {
      pending.resolve("late");
      await run;
    });
    expect(result.current.value).toBeNull();
  });

  it("keeps run and discard stable across renders and across applied values", async () => {
    const { result, rerender } = renderHook(() => useLatestResult<string>());
    const { run, discard } = result.current;

    rerender();
    await act(async () => {
      await result.current.run(() => Promise.resolve("applied"));
    });

    // The pages put these in useCallback deps, and the callback they build
    // from `run` is what drives the interval. An identity that changed when a
    // value was applied would tear the timer down and rebuild it on every
    // refresh.
    expect(result.current.value).toBe("applied");
    expect(result.current.run).toBe(run);
    expect(result.current.discard).toBe(discard);
  });
});
