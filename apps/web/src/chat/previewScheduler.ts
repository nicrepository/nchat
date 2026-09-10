/**
 * Concurrency and priority for attachment preview/content fetches (issue #675).
 *
 * A timeline can bring dozens of attachments into the prefetch region in one
 * flick of the wheel. Each of them wants bytes through the same authenticated
 * client, and starting all of them at once is what makes a large conversation
 * feel broken: the browser's per-origin connection pool serialises them anyway,
 * so the *visible* image ends up queued behind twenty that nobody is looking
 * at yet.
 *
 * So the requests go through here instead of straight to fetch:
 *
 *   - at most MAX_CONCURRENT_PREVIEWS *live operations* at a time — a cancelled
     request keeps its slot until its promise actually settles, because a
     transport that is slow to honour an abort is still using the connection;
 *   - the highest priority waiting task starts next (visible before prefetch),
 *     FIFO inside one priority so the reading order is preserved;
 *   - the same key asked for twice shares one request rather than making two;
 *   - a task that becomes visible while still queued is promoted;
 *   - a subscriber that goes away takes its claim with it, and a task nobody
 *     wants any more is dropped from the queue or aborted mid-flight.
 *
 * What this is NOT: a cache. Bytes are never held here — every attachment route
 * answers `Cache-Control: private, no-store`, and keeping a copy of decrypted
 * corporate content alive past the component that shows it is exactly the thing
 * useAttachmentBlobUrl's revoke discipline exists to prevent. Deduplication is
 * only ever between requests that overlap in time.
 *
 * Authorization is untouched: this decides *when* a request is made, never
 * whether it is allowed. Every task still runs the caller's own authenticated
 * fetch, and file-service re-checks membership and the malware-scan gate on
 * each one.
 */

/** P0 visible · P1 inside the prefetch region · P2 speculative/idle. */
export type PreviewPriority = 0 | 1 | 2;

/**
 * How many preview/content fetches may be in flight at once.
 *
 * Sits inside the issue's 4–6 suggestion and just under the ~6 per-host
 * connection budget browsers give HTTP/1.1, so a preview burst never starves
 * the message/API requests the chat itself needs to stay usable.
 */
export const MAX_CONCURRENT_PREVIEWS = 5;

interface ScheduledTask {
  key: string;
  priority: PreviewPriority;
  /** Enqueue order, so ties inside one priority start in reading order. */
  seq: number;
  run: (signal: AbortSignal) => Promise<Blob>;
  controller: AbortController;
  subscribers: number;
  promise: Promise<Blob>;
  started: boolean;
  /**
   * Whether this task still holds one of the concurrency slots.
   *
   * Released only from the run's own `finally`, never from cancellation.
   * Aborting is a request, not an event: `fetch` may take an arbitrary time to
   * honour it, and a service worker or a polyfill may ignore it entirely. If
   * the slot were freed at abort time, the limit would bound "requests someone
   * is still waiting for" rather than "requests actually in flight", and a
   * scroll that cancels and re-arms repeatedly could leave far more than
   * MAX_CONCURRENT_PREVIEWS connections alive at once.
   */
  counted: boolean;
  /**
   * Whether this task has been abandoned. It keeps running to the end of its
   * own promise — that is the only moment its slot can honestly be released —
   * but its result is discarded and it is no longer reachable for dedupe.
   */
  cancelled: boolean;
  /** Settles `promise` from the run's own result. Assigned once, at creation. */
  resolveRun: (result: Promise<Blob>) => void;
}

const tasks = new Map<string, ScheduledTask>();
let nextSeq = 0;
let active = 0;

function abortError(): DOMException {
  return new DOMException("Aborted", "AbortError");
}

/**
 * Starts the highest-priority waiting tasks until the concurrency budget is
 * spent.
 *
 * ponytail: a linear scan of the pending tasks rather than a heap. The map only
 * ever holds what is currently mounted in one conversation's window — tens of
 * entries — so ordering it properly would cost more code than it saves cycles.
 * Upgrade path if a timeline ever holds thousands: a bucket per priority.
 */
function pump(): void {
  while (active < MAX_CONCURRENT_PREVIEWS) {
    let next: ScheduledTask | null = null;
    for (const task of tasks.values()) {
      if (task.started) continue;
      const better =
        !next ||
        task.priority < next.priority ||
        (task.priority === next.priority && task.seq < next.seq);
      if (better) next = task;
    }
    if (!next) return;
    start(next);
  }
}

/** Frees the slot a task holds, at most once. */
function uncount(task: ScheduledTask): void {
  if (!task.counted) return;
  task.counted = false;
  active -= 1;
}

function start(task: ScheduledTask): void {
  task.started = true;
  task.counted = true;
  active += 1;
  const settle = () => {
    // The one honest moment to say the operation is over, whether it finished,
    // failed, or finally honoured an abort issued long before.
    uncount(task);
    if (tasks.get(task.key) === task) tasks.delete(task.key);
    pump();
  };
  // The caller's promise is `task.promise`, created below around this call, so
  // the settle handler here is the only place that must never itself reject.
  task.resolveRun(task.run(task.controller.signal).finally(settle));
}

function acquire(
  key: string,
  priority: PreviewPriority,
  run: (signal: AbortSignal) => Promise<Blob>,
): ScheduledTask {
  const existing = tasks.get(key);
  if (existing) {
    if (priority < existing.priority) existing.priority = priority;
    return existing;
  }
  let resolveRun!: (result: Promise<Blob>) => void;
  const promise = new Promise<Blob>((resolve, reject) => {
    resolveRun = (result) => result.then(resolve, reject);
  });
  const task: ScheduledTask = {
    key,
    priority,
    seq: nextSeq++,
    run,
    controller: new AbortController(),
    subscribers: 0,
    promise,
    started: false,
    counted: false,
    cancelled: false,
    resolveRun,
  };
  // Nothing else observes this promise until a subscriber attaches below, and
  // an aborted task rejects it — mark it handled so a dropped task never
  // surfaces as an unhandled rejection.
  promise.catch(() => {});
  tasks.set(key, task);
  return task;
}

function release(task: ScheduledTask): void {
  task.subscribers -= 1;
  if (task.subscribers > 0) return;
  task.cancelled = true;
  // Unreachable for dedupe from here on: a later subscriber must get a fresh
  // task rather than attach to one already being torn down.
  if (tasks.get(task.key) === task) tasks.delete(task.key);
  task.controller.abort();
  if (!task.started) {
    // Never started, never counted: it costs nothing and simply disappears.
    // Something else may now fit in its place.
    pump();
    return;
  }
  // Started: the slot stays taken until the request itself finishes. `settle`
  // is what frees it and what pumps the next task — see the `counted` comment.
}

/**
 * Runs `run` when the scheduler has room for it, at the given priority.
 *
 * The returned promise rejects with an AbortError as soon as `signal` aborts,
 * whether or not the underlying request had started — the caller stops waiting
 * immediately, and the request itself is cancelled once no other subscriber
 * still wants it.
 */
export function scheduleAttachmentFetch(
  key: string,
  priority: PreviewPriority,
  signal: AbortSignal,
  run: (signal: AbortSignal) => Promise<Blob>,
): Promise<Blob> {
  const task = acquire(key, priority, run);
  task.subscribers += 1;

  let detach = () => {};
  let released = false;
  // Releasing on the abort event itself, not on the resulting rejection:
  // cancellation has to be observable in the same task as the unmount that
  // caused it, so a component's own AbortController still reaches the network
  // synchronously the way it did before this scheduler existed.
  const releaseOnce = () => {
    if (released) return;
    released = true;
    detach();
    release(task);
  };

  const subscribed = new Promise<Blob>((resolve, reject) => {
    const onAbort = () => {
      releaseOnce();
      reject(abortError());
    };
    if (signal.aborted) {
      onAbort();
    } else {
      signal.addEventListener("abort", onAbort, { once: true });
      detach = () => signal.removeEventListener("abort", onAbort);
    }
    task.promise.then(resolve, reject);
  });

  subscribed.then(releaseOnce, releaseOnce);

  pump();
  return subscribed;
}

/**
 * Raises a still-queued task's priority — what an attachment scrolling from
 * the prefetch region into the viewport does. A task already running has
 * nothing to gain from it, and one that finished is gone.
 */
export function promoteAttachmentFetch(key: string, priority: PreviewPriority): void {
  const task = tasks.get(key);
  if (!task || task.started || priority >= task.priority) return;
  task.priority = priority;
}

/** Test seam: drops every queued task and aborts every running one. */
export function resetPreviewScheduler(): void {
  for (const task of tasks.values()) {
    task.cancelled = true;
    task.controller.abort();
    task.counted = false;
  }
  tasks.clear();
  active = 0;
  nextSeq = 0;
}

/**
 * Test/diagnostic view of the queue. Never used to make a decision.
 *
 * `running` counts live operations, cancelled-but-unfinished ones included —
 * that is exactly the number MAX_CONCURRENT_PREVIEWS bounds.
 */
export function previewSchedulerStats(): { running: number; queued: number } {
  let queued = 0;
  for (const task of tasks.values()) if (!task.started) queued += 1;
  return { running: active, queued };
}
