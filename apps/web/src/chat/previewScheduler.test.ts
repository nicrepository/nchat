/**
 * previewScheduler tests (issue #675).
 *
 * The scheduler exists to make a burst of attachments behave: a bounded number
 * of requests in flight, the visible ones first, one request when two
 * components want the same bytes, and nothing left running for something nobody
 * is looking at any more. Each of those is a property a large conversation
 * depends on, so each is asserted directly rather than through a component that
 * happens to use it.
 */

import { afterEach, describe, expect, it, vi } from "vitest";

import {
  MAX_CONCURRENT_PREVIEWS,
  previewSchedulerStats,
  promoteAttachmentFetch,
  resetPreviewScheduler,
  scheduleAttachmentFetch,
} from "./previewScheduler";

interface Deferred {
  promise: Promise<Blob>;
  resolve: (blob: Blob) => void;
  reject: (error: unknown) => void;
}

function deferred(): Deferred {
  let resolve!: (blob: Blob) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<Blob>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/** A task that never settles on its own, plus a record of whether it ran. */
function blockingTask() {
  const gate = deferred();
  const run = vi.fn(() => gate.promise);
  return { gate, run };
}

/** Swallows an expected AbortError so a rejection never escapes the test. */
function ignoreAbort(promise: Promise<Blob>): Promise<void> {
  return promise.then(
    () => undefined,
    () => undefined,
  );
}

/** Fills every slot, so the next task scheduled is guaranteed to queue. */
function saturate() {
  const tasks = Array.from({ length: MAX_CONCURRENT_PREVIEWS }, blockingTask);
  const controllers = tasks.map(() => new AbortController());
  const settled = tasks.map((task, index) =>
    ignoreAbort(
      scheduleAttachmentFetch(`blocker-${index}`, 1, controllers[index].signal, task.run),
    ),
  );
  return { tasks, controllers, settled };
}

afterEach(() => {
  resetPreviewScheduler();
});

describe("concurrency", () => {
  it("never runs more than the limit at once, and starts the next as one finishes", async () => {
    const tasks = Array.from({ length: MAX_CONCURRENT_PREVIEWS + 3 }, blockingTask);
    const settled = tasks.map((task, index) =>
      ignoreAbort(scheduleAttachmentFetch(`k-${index}`, 1, new AbortController().signal, task.run)),
    );

    expect(tasks.filter((task) => task.run.mock.calls.length > 0)).toHaveLength(
      MAX_CONCURRENT_PREVIEWS,
    );
    expect(previewSchedulerStats()).toEqual({ running: MAX_CONCURRENT_PREVIEWS, queued: 3 });

    tasks[0].gate.resolve(new Blob(["a"]));
    await settled[0];

    expect(tasks[MAX_CONCURRENT_PREVIEWS].run).toHaveBeenCalledTimes(1);
    expect(previewSchedulerStats().running).toBe(MAX_CONCURRENT_PREVIEWS);
  });
});

describe("priority", () => {
  it("starts a visible task before ones that are merely near", async () => {
    const blockers = saturate();
    const near = blockingTask();
    const visible = blockingTask();
    // Enqueued near-first on purpose: without priority, FIFO would start it.
    void ignoreAbort(scheduleAttachmentFetch("near", 1, new AbortController().signal, near.run));
    void ignoreAbort(
      scheduleAttachmentFetch("visible", 0, new AbortController().signal, visible.run),
    );

    blockers.tasks[0].gate.resolve(new Blob(["done"]));
    await blockers.settled[0];

    expect(visible.run).toHaveBeenCalledTimes(1);
    expect(near.run).not.toHaveBeenCalled();
  });

  it("promotes a task that becomes visible while it is still waiting", async () => {
    const blockers = saturate();
    const first = blockingTask();
    const later = blockingTask();
    void ignoreAbort(scheduleAttachmentFetch("first", 1, new AbortController().signal, first.run));
    void ignoreAbort(scheduleAttachmentFetch("later", 1, new AbortController().signal, later.run));

    // "later" scrolls into the viewport before either has started.
    promoteAttachmentFetch("later", 0);

    blockers.tasks[0].gate.resolve(new Blob(["done"]));
    await blockers.settled[0];

    expect(later.run).toHaveBeenCalledTimes(1);
    expect(first.run).not.toHaveBeenCalled();
  });
});

describe("deduplication", () => {
  it("makes one request when two components ask for the same bytes", async () => {
    const task = blockingTask();
    const bytes = new Blob(["shared"]);
    const one = scheduleAttachmentFetch("same", 1, new AbortController().signal, task.run);
    const two = scheduleAttachmentFetch("same", 1, new AbortController().signal, task.run);

    task.gate.resolve(bytes);

    expect(await one).toBe(bytes);
    expect(await two).toBe(bytes);
    expect(task.run).toHaveBeenCalledTimes(1);
  });

  it("keeps the shared request alive while another subscriber still wants it", async () => {
    const task = blockingTask();
    const leaving = new AbortController();
    const abandoned = ignoreAbort(scheduleAttachmentFetch("same", 1, leaving.signal, task.run));
    const kept = scheduleAttachmentFetch("same", 1, new AbortController().signal, task.run);

    leaving.abort();
    await abandoned;

    const bytes = new Blob(["still wanted"]);
    task.gate.resolve(bytes);
    expect(await kept).toBe(bytes);
  });
});

describe("cancellation", () => {
  it("drops a task that stops being relevant before it ever started", async () => {
    saturate();
    const queued = blockingTask();
    const controller = new AbortController();
    const settled = ignoreAbort(
      scheduleAttachmentFetch("queued", 1, controller.signal, queued.run),
    );
    expect(previewSchedulerStats().queued).toBe(1);

    controller.abort();
    await settled;

    expect(queued.run).not.toHaveBeenCalled();
    expect(previewSchedulerStats().queued).toBe(0);
  });

  it("aborts the underlying request, synchronously, when its last subscriber goes", () => {
    let taskSignal: AbortSignal | undefined;
    const controller = new AbortController();
    void ignoreAbort(
      scheduleAttachmentFetch("solo", 0, controller.signal, (signal) => {
        taskSignal = signal;
        return new Promise<Blob>(() => {});
      }),
    );

    controller.abort();

    // The same task as the unmount that caused it: a component must not be able
    // to observe its own teardown as "still in flight".
    expect(taskSignal?.aborted).toBe(true);
  });

  it("rejects immediately for a caller whose signal was already aborted", async () => {
    const controller = new AbortController();
    controller.abort();
    const run = vi.fn(() => Promise.resolve(new Blob(["never"])));

    await expect(scheduleAttachmentFetch("dead", 0, controller.signal, run)).rejects.toMatchObject({
      name: "AbortError",
    });
  });

  it("keeps a cancelled request's slot until the request itself actually finishes", async () => {
    // The task deliberately ignores its AbortSignal, which is what a slow
    // transport, a service worker, or a polyfilled fetch really does. The slot
    // it holds is a live connection either way, so the limit must still count
    // it. Freeing it at abort time would let a scroll that cancels and re-arms
    // repeatedly leave more than MAX_CONCURRENT_PREVIEWS requests in flight.
    const stubborn = Array.from({ length: MAX_CONCURRENT_PREVIEWS }, () => {
      const gate = deferred();
      return { gate, run: vi.fn(() => gate.promise) };
    });
    const controllers = stubborn.map(() => new AbortController());
    const settled = stubborn.map((task, index) =>
      ignoreAbort(scheduleAttachmentFetch(`stub-${index}`, 1, controllers[index].signal, task.run)),
    );
    const waiting = blockingTask();
    void ignoreAbort(
      scheduleAttachmentFetch("waiting", 1, new AbortController().signal, waiting.run),
    );
    expect(previewSchedulerStats()).toEqual({ running: MAX_CONCURRENT_PREVIEWS, queued: 1 });

    controllers[0].abort();
    await settled[0];

    // Still five live operations: the abandoned one has not finished.
    expect(waiting.run).not.toHaveBeenCalled();
    expect(previewSchedulerStats()).toEqual({ running: MAX_CONCURRENT_PREVIEWS, queued: 1 });

    // Only when the ignored abort finally resolves does the slot come back.
    stubborn[0].gate.resolve(new Blob(["late"]));
    await Promise.resolve();
    await Promise.resolve();

    expect(waiting.run).toHaveBeenCalledTimes(1);
    expect(previewSchedulerStats()).toEqual({ running: MAX_CONCURRENT_PREVIEWS, queued: 0 });
  });

  it("never exceeds the limit across a burst of cancel-and-rearm cycles", async () => {
    const live: Array<{ gate: Deferred; run: ReturnType<typeof vi.fn> }> = [];
    let peak = 0;
    const stubbornTask = () => {
      const gate = deferred();
      const entry = {
        gate,
        // Ignores the signal on purpose — see the test above.
        run: vi.fn(() => gate.promise),
      };
      live.push(entry);
      return entry;
    };

    const settled: Array<Promise<void>> = [];
    for (let cycle = 0; cycle < 4; cycle += 1) {
      for (let i = 0; i < 4; i += 1) {
        const controller = new AbortController();
        const task = stubbornTask();
        settled.push(
          ignoreAbort(
            scheduleAttachmentFetch(`burst-${cycle}-${i}`, 1, controller.signal, task.run),
          ),
        );
        if (i % 2 === 0) controller.abort();
        peak = Math.max(peak, previewSchedulerStats().running);
      }
    }

    expect(peak).toBeLessThanOrEqual(MAX_CONCURRENT_PREVIEWS);
    expect(previewSchedulerStats().running).toBeLessThanOrEqual(MAX_CONCURRENT_PREVIEWS);

    // Let every stubborn request finish so the scheduler drains cleanly.
    for (const entry of live) entry.gate.resolve(new Blob(["done"]));
    await Promise.all(settled);
    expect(previewSchedulerStats()).toEqual({ running: 0, queued: 0 });
  });

  it("still drops a queued task instantly, since it never occupied a slot", async () => {
    const blockers = saturate();
    const queued = blockingTask();
    const controller = new AbortController();
    const settled = ignoreAbort(scheduleAttachmentFetch("q", 1, controller.signal, queued.run));
    const next = blockingTask();
    void ignoreAbort(scheduleAttachmentFetch("next", 1, new AbortController().signal, next.run));
    expect(previewSchedulerStats().queued).toBe(2);

    controller.abort();
    await settled;

    expect(queued.run).not.toHaveBeenCalled();
    expect(previewSchedulerStats()).toEqual({ running: MAX_CONCURRENT_PREVIEWS, queued: 1 });
    // The blockers still hold every slot, so nothing was let through early.
    expect(next.run).not.toHaveBeenCalled();
    void blockers;
  });
});

describe("failure", () => {
  it("reports a failed request to its caller and frees the slot", async () => {
    const gate = deferred();
    const failing = scheduleAttachmentFetch(
      "bad",
      0,
      new AbortController().signal,
      () => gate.promise,
    );

    gate.reject(new Error("403"));

    await expect(failing).rejects.toThrow("403");
    expect(previewSchedulerStats()).toEqual({ running: 0, queued: 0 });
  });
});
