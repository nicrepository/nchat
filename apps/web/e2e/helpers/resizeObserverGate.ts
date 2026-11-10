/**
 * Strict gate for the one browser notice issue #1152 knowingly produces.
 *
 * A touch reaction picker watches itself and the menu it keeps clear of (see
 * useAnchoredPicker). Placing it from inside that observer resizes a shallower
 * target, so the engine defers the new size to the next frame and reports
 * "ResizeObserver loop completed with undelivered notifications." — a finite,
 * converging notice, not a loop (drafts.csswg.org/resize-observer, "deliver
 * resize loop error"). The WebKit evidence of the issue showed at most four
 * per opening, all while the picker was being placed.
 *
 * The gate accepts that exact notice only inside the window it belongs to and
 * only with the proof that nothing went wrong around it; everything else stays
 * a failure, with the reason it failed:
 *
 * - every native `error`/`unhandledrejection` and every Playwright `pageerror`
 *   is recorded, in separate channels: a `pageerror` is a subset of the native
 *   events (WebKit reports one per test where the page saw four), so only the
 *   native channel is counted, and the other only has to agree with it;
 * - a notice counts against the opening it fell in — from just before the tap
 *   that opens the picker to the end of twenty consecutive frames in which
 *   nothing moved, the picker's observers did not run and no error arrived —
 *   and no opening may have more than four;
 * - a notice must come out of an update in which an observer of the picker or
 *   of its menu ran (see "Attribution" below);
 * - a notice after that stability, or while no picker is open, fails;
 * - so does any other error, any console error not known in advance, a picker
 *   that crosses the menu or leaves the visual viewport, an opening in which
 *   the picker and the menu were not both observed, an observer still watching
 *   or still being called once the picker closed, and an accepted notice
 *   without a hit-tested, exactly-once reaction to show for it.
 *
 * Attribution, and its limit: the error event names no observer. The engine
 * reports it at the end of the rendering update that ran the callbacks, so
 * the page side tags each callback and each error with the frame it happened
 * in — from the frame's first callback to the next frame's animation
 * callbacks. A notice from a frame with no callback on the picker or the menu
 * is `unattributed-notice`. When other observers ran in the same frame too, the
 * notice is still accepted but counted as `shared`: the frame is known, the
 * observer that left a size undelivered is not. That is correlation within one
 * frame, not proof of which callback caused it.
 *
 * Nothing is filtered at the source: the page's own listeners still see every
 * event, and no event is prevented. Whatever a test ends with, the log is
 * attached: by `verdict()`, or by `attachGateOnTeardown` in an `afterEach`.
 */

import type { Page, TestInfo } from "@playwright/test";

export const RESIZE_OBSERVER_LOOP = "ResizeObserver loop completed with undelivered notifications.";
export const MAX_NOTICES_PER_OPENING = 4;
export const STABLE_FRAMES = 20;
/** How long a settle may look for its stable frames before it gives up. */
export const MAX_SETTLE_FRAMES = 240;
/** The page keeps at most this many callback records; past it the log says so. */
export const MAX_CALLBACK_RECORDS = 5000;
/** The two surfaces a touch picker opening must observe: the picker and its menu. */
export const WATCHED_SURFACES = ["Escolher reação", "Reagir à mensagem"];
/** WebKit does not know the viewport meta's `interactive-widget`; nothing else is expected. */
export const KNOWN_CONSOLE_ERRORS = [
  'Viewport argument key "interactive-widget" not recognized and ignored.',
];
export const GATE_ATTACHMENT = "resize-observer-gate";
/** Subpixel rounding between two boxes is not an overlap. */
const tolerance = 0.5;

export interface Box {
  top: number;
  bottom: number;
  left: number;
  right: number;
}

export type Marker =
  | { type: "open"; opening: number; t: number }
  | {
      type: "settled";
      opening: number;
      t: number;
      stable: boolean;
      frames: number;
      observing: string[];
    }
  | { type: "closed"; opening: number; t: number; callbacks: number; observing: string[] };

export interface NativeError {
  kind: "error" | "unhandledrejection";
  message: string;
  t: number;
  /** The frame of observer callbacks this error arrived in; null outside one. */
  update: number | null;
}

/** One observer callback: when, in which update, and on which targets. */
export interface CallbackRecord {
  observer: number;
  update: number;
  t: number;
  targets: string[];
  watched: boolean;
}

export interface Geometry {
  opening: number;
  picker: Box | null;
  sheet: Box | null;
  viewport: Box;
}

export interface GateLog {
  markers: Marker[];
  native: NativeError[];
  callbacks: CallbackRecord[];
  /** Whether callback records were dropped past MAX_CALLBACK_RECORDS. */
  truncated: boolean;
  geometry: Geometry[];
  pageErrors: string[];
  consoleErrors: string[];
  selection?: { hit: boolean; reactions: number };
}

export type Reason =
  | "unexpected-error"
  | "unexpected-console-error"
  | "unmatched-pageerror"
  | "notice-while-closed"
  | "notice-after-stability"
  | "unattributed-notice"
  | "too-many-notices"
  | "not-settled"
  | "observer-missing"
  | "unverified-cleanup"
  | "observer-not-disconnected"
  | "callbacks-after-close"
  | "picker-overlaps-sheet"
  | "picker-outside-viewport"
  | "missing-functional-evidence";

export interface Failure {
  reason: Reason;
  detail: string;
}

export interface Verdict {
  failures: Failure[];
  /** Notices inside each opening's window, attributed or not: the native channel only. */
  notices: Record<number, number>;
  /** Of those, the attributed ones whose update also ran other observers. */
  shared: Record<number, number>;
}

type Phase = { kind: "closed" } | { kind: "window" | "stable"; opening: number };

/** Where an event at `t` fell: the last marker at or before it decides. */
function phaseAt(markers: Marker[], t: number): Phase {
  const last = markers.filter((marker) => marker.t <= t).at(-1);
  if (!last || last.type === "closed") return { kind: "closed" };
  const stable = last.type === "settled" && last.stable;
  return { kind: stable ? "stable" : "window", opening: last.opening };
}

function overlaps(a: Box, b: Box): boolean {
  return (
    a.bottom - tolerance > b.top &&
    a.top + tolerance < b.bottom &&
    a.right - tolerance > b.left &&
    a.left + tolerance < b.right
  );
}

function inside(box: Box, band: Box): boolean {
  return (
    box.top >= band.top - tolerance &&
    box.bottom <= band.bottom + tolerance &&
    box.left >= band.left - tolerance &&
    box.right <= band.right + tolerance
  );
}

const describeBox = (box: Box) => `${box.top}–${box.bottom}×${box.left}–${box.right}`;

const increment = (counts: Record<number, number>, key: number) => {
  counts[key] = (counts[key] ?? 0) + 1;
};

/** What ran in a notice's update: whether a watched observer did, and whether others did too. */
function updateOf(log: GateLog, update: number | null) {
  const ran = update === null ? [] : log.callbacks.filter((record) => record.update === update);
  return {
    watched: ran.some((record) => record.watched),
    others: ran.some((record) => !record.watched),
    describe: ran.length
      ? ran.map((record) => `#${record.observer}[${record.targets.join(", ")}]`).join(" ")
      : "nenhum callback",
  };
}

function classifyNative(log: GateLog, markers: Marker[], fail: (failure: Failure) => void) {
  const notices: Record<number, number> = {};
  const shared: Record<number, number> = {};
  for (const event of log.native) {
    if (event.kind !== "error" || event.message !== RESIZE_OBSERVER_LOOP) {
      fail({ reason: "unexpected-error", detail: `window.${event.kind}: ${event.message}` });
      continue;
    }
    const phase = phaseAt(markers, event.t);
    if (phase.kind === "closed") {
      fail({ reason: "notice-while-closed", detail: `t=${event.t}` });
      continue;
    }
    if (phase.kind === "stable") {
      fail({ reason: "notice-after-stability", detail: `abertura ${phase.opening}, t=${event.t}` });
      continue;
    }
    increment(notices, phase.opening);
    const ran = updateOf(log, event.update);
    if (!ran.watched) {
      const truncated = log.truncated ? " (registro de callbacks truncado)" : "";
      fail({
        reason: "unattributed-notice",
        detail: `abertura ${phase.opening}, t=${event.t}, update ${event.update}: ${ran.describe}${truncated}`,
      });
    } else if (ran.others) {
      increment(shared, phase.opening);
    }
  }
  for (const [opening, count] of Object.entries(notices)) {
    if (count > MAX_NOTICES_PER_OPENING) {
      fail({ reason: "too-many-notices", detail: `abertura ${opening}: ${count} eventos nativos` });
    }
  }
  return { notices, shared };
}

function classifyChannels(log: GateLog, fail: (failure: Failure) => void) {
  for (const message of log.pageErrors) {
    if (message !== RESIZE_OBSERVER_LOOP) {
      fail({ reason: "unexpected-error", detail: `pageerror: ${message}` });
    }
  }
  const delivered = log.pageErrors.filter((message) => message === RESIZE_OBSERVER_LOOP).length;
  const native = log.native.filter((event) => event.message === RESIZE_OBSERVER_LOOP).length;
  if (delivered > native) {
    fail({
      reason: "unmatched-pageerror",
      detail: `${delivered} pageerror para ${native} eventos nativos`,
    });
  }
  for (const message of log.consoleErrors) {
    if (!KNOWN_CONSOLE_ERRORS.includes(message)) {
      fail({ reason: "unexpected-console-error", detail: message });
    }
  }
}

function classifySettle(n: number, settled: Marker | undefined, fail: (failure: Failure) => void) {
  if (settled?.type !== "settled") {
    fail({ reason: "not-settled", detail: `abertura ${n}: estabilidade não verificada` });
    return;
  }
  if (!settled.stable) {
    fail({
      reason: "not-settled",
      detail: `abertura ${n}: sem ${STABLE_FRAMES} quadros estáveis em ${settled.frames}`,
    });
  }
  const missing = WATCHED_SURFACES.filter((surface) => !settled.observing.includes(surface));
  if (missing.length) {
    fail({
      reason: "observer-missing",
      detail: `abertura ${n}: ninguém observava ${missing.join(", ")}`,
    });
  }
}

function classifyCleanup(n: number, closed: Marker | undefined, fail: (failure: Failure) => void) {
  if (closed?.type !== "closed") {
    fail({ reason: "unverified-cleanup", detail: `abertura ${n}: fechamento não verificado` });
    return;
  }
  if (closed.observing.length) {
    fail({
      reason: "observer-not-disconnected",
      detail: `abertura ${n}: ainda observa ${closed.observing.join(", ")}`,
    });
  }
  if (closed.callbacks) {
    fail({
      reason: "callbacks-after-close",
      detail: `abertura ${n}: ${closed.callbacks} callbacks após fechar`,
    });
  }
}

function classifyMarkers(markers: Marker[], fail: (failure: Failure) => void) {
  for (const { opening: n } of markers.filter((marker) => marker.type === "open")) {
    const of = (type: Marker["type"]) =>
      markers.find((marker) => marker.type === type && marker.opening === n);
    classifySettle(n, of("settled"), fail);
    classifyCleanup(n, of("closed"), fail);
  }
}

function classifyGeometry(log: GateLog, fail: (failure: Failure) => void) {
  for (const { opening, picker, sheet, viewport } of log.geometry) {
    if (!picker) {
      fail({ reason: "missing-functional-evidence", detail: `abertura ${opening}: sem picker` });
      continue;
    }
    if (sheet && overlaps(picker, sheet)) {
      fail({
        reason: "picker-overlaps-sheet",
        detail: `abertura ${opening}: picker ${describeBox(picker)}, menu ${describeBox(sheet)}`,
      });
    }
    if (!inside(picker, viewport)) {
      fail({
        reason: "picker-outside-viewport",
        detail: `abertura ${opening}: picker ${describeBox(picker)}, tela ${describeBox(viewport)}`,
      });
    }
  }
}

/** The verdict on one test's log: no failures is the only pass. */
export function classifyResizeObserverNotices(log: GateLog): Verdict {
  const failures: Failure[] = [];
  const fail = (failure: Failure) => failures.push(failure);
  const markers = [...log.markers].sort((a, b) => a.t - b.t);
  const { notices, shared } = classifyNative(log, markers, fail);
  classifyChannels(log, fail);
  classifyMarkers(markers, fail);
  classifyGeometry(log, fail);
  const accepted = Object.values(notices).reduce((sum, count) => sum + count, 0);
  const proven = log.selection?.hit === true && log.selection.reactions === 1;
  if (accepted && !proven) {
    fail({
      reason: "missing-functional-evidence",
      detail: `${accepted} avisos sem hit-test e reação única: ${JSON.stringify(log.selection ?? null)}`,
    });
  }
  return { failures, notices, shared };
}

/**
 * RO-N2: whatever the verdict failed for, other than the provoked error once in
 * each channel. A reason is not enough: every failure must be that error, so a
 * second exception, an unhandled rejection or another console error remains.
 * The console may echo the provoked error itself; the window event carries the
 * engine's prefix ("Uncaught Error: " in Chromium, "Error: " in WebKit).
 */
export function provokedErrorProblems(verdict: Verdict, message: string): string[] {
  const thrown = `(Uncaught )?(Error: )?${message.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`;
  const windowError = new RegExp(`^window\\.error: ${thrown}$`);
  const consoleEcho = new RegExp(`^${thrown}$`);
  const problems: string[] = [];
  let pageHits = 0;
  let windowHits = 0;
  for (const { reason, detail } of verdict.failures) {
    if (reason === "unexpected-error" && detail === `pageerror: ${message}`) pageHits++;
    else if (reason === "unexpected-error" && windowError.test(detail)) windowHits++;
    else if (reason !== "unexpected-console-error" || !consoleEcho.test(detail))
      problems.push(`${reason}: ${detail}`);
  }
  if (pageHits !== 1) problems.push(`pageerror do erro provocado: ${pageHits}`);
  if (windowHits !== 1) problems.push(`window.error do erro provocado: ${windowHits}`);
  return problems;
}

/** What the page side holds: everything in the log but the Playwright channels. */
export type PageLog = Pick<GateLog, "markers" | "native" | "callbacks" | "truncated" | "geometry">;

interface PageGate extends PageLog {
  settle(opening: number): Promise<boolean>;
  closed(opening: number): Promise<void>;
  measure(opening: number): void;
}

interface PageGateOptions {
  stableFrames: number;
  maxFrames: number;
  maxRecords: number;
  watched: string[];
}

/**
 * Runs in the page before any of its scripts. It only counts: the observer
 * callbacks are passed through untouched, and what it reads in them is the
 * targets' `aria-label`, never their geometry, so the scheduling being judged
 * is the application's own. The one thing it adds is an empty animation
 * callback per frame that ran an observer, for the next frame.
 */
export function installPageGate({ stableFrames, maxFrames, maxRecords, watched }: PageGateOptions) {
  const label = (element: Element) => element.getAttribute("aria-label") ?? element.tagName;
  const box = (element: Element | null) => {
    if (!element) return null;
    const { top, bottom, left, right } = element.getBoundingClientRect();
    return { top, bottom, left, right };
  };
  const picker = () => document.querySelector('[role="dialog"][aria-label="Escolher reação"]');
  const sheet = () => document.querySelector('[role="toolbar"][aria-label="Reagir à mensagem"]');
  const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));
  const observed: Set<Element>[] = [];
  /** The surfaces some observer is watching right now. */
  const observing = () =>
    observed.flatMap((targets) => [...targets].map(label)).filter((name) => watched.includes(name));
  let watchedCalls = 0;
  // An update is a rendering frame: it begins with its first callback and ends
  // at the next frame's animation callbacks, so a loop error is tagged with it
  // whether the engine reports it there and then or from a task it queued.
  let update = 0;
  let inUpdate = false;
  const enterUpdate = () => {
    if (inUpdate) return update;
    inUpdate = true;
    update++;
    requestAnimationFrame(() => {
      inUpdate = false;
    });
    return update;
  };

  const gate: PageGate = {
    markers: [],
    native: [],
    callbacks: [],
    truncated: false,
    geometry: [],
    measure(opening) {
      const view = window.visualViewport;
      const top = view?.offsetTop ?? 0;
      const left = view?.offsetLeft ?? 0;
      gate.geometry.push({
        opening,
        picker: box(picker()),
        sheet: box(sheet()),
        viewport: {
          top,
          left,
          bottom: top + (view?.height ?? innerHeight),
          right: left + (view?.width ?? innerWidth),
        },
      });
    },
    async settle(opening) {
      const signature = () =>
        JSON.stringify([watchedCalls, gate.native.length, box(picker()), box(sheet())]);
      let last = signature();
      let unchanged = 0;
      let frames = 0;
      while (unchanged < stableFrames && frames < maxFrames) {
        await frame();
        frames++;
        const now = signature();
        unchanged = now === last ? unchanged + 1 : 0;
        last = now;
      }
      const stable = unchanged >= stableFrames;
      const t = performance.now();
      gate.markers.push({ type: "settled", opening, t, stable, frames, observing: observing() });
      gate.measure(opening);
      return stable;
    },
    async closed(opening) {
      const t = performance.now();
      const before = watchedCalls;
      for (let frames = 0; frames < stableFrames; frames++) await frame();
      gate.markers.push({
        type: "closed",
        opening,
        t,
        callbacks: watchedCalls - before,
        observing: observing(),
      });
    },
  };
  (window as unknown as { __roGate: PageGate }).__roGate = gate;

  addEventListener("error", (event) => {
    const t = performance.now();
    gate.native.push({
      kind: "error",
      message: event.message,
      t,
      update: inUpdate ? update : null,
    });
  });
  addEventListener("unhandledrejection", (event) => {
    const reason = event.reason as { message?: string } | undefined;
    gate.native.push({
      kind: "unhandledrejection",
      message: String(reason?.message ?? reason),
      t: performance.now(),
      update: inUpdate ? update : null,
    });
  });

  // A map rather than a class field: this function is serialized into the
  // page, so it must not lean on any transpiler helper outside it.
  const targetsOf = new WeakMap<ResizeObserver, Set<Element>>();
  const Native = window.ResizeObserver;
  window.ResizeObserver = class extends Native {
    constructor(callback: ResizeObserverCallback) {
      const id = observed.length;
      const targets = new Set<Element>();
      super((entries, observer) => {
        const names = entries.map((entry) => label(entry.target));
        const isWatched = names.some((name) => watched.includes(name));
        if (isWatched) watchedCalls++;
        const record = { observer: id, update: enterUpdate(), t: performance.now() };
        if (gate.callbacks.length < maxRecords) {
          gate.callbacks.push({ ...record, targets: names, watched: isWatched });
        } else {
          gate.truncated = true;
        }
        // The browser calls back with the observer as `this`, and so does the wrapper.
        callback.call(observer, entries, observer);
      });
      targetsOf.set(this, targets);
      observed.push(targets);
    }
    observe(target: Element, options?: ResizeObserverOptions) {
      targetsOf.get(this)!.add(target);
      super.observe(target, options);
    }
    unobserve(target: Element) {
      targetsOf.get(this)!.delete(target);
      super.unobserve(target);
    }
    disconnect() {
      targetsOf.get(this)!.clear();
      super.disconnect();
    }
  };
}

export type Step =
  | { step: "open" | "closed" | "measure"; opening: number }
  | { step: "settle"; opening: number; stable: boolean };

/** Everything a gate has recorded, on both sides; the page side may be gone. */
export interface GateSource {
  steps: Step[];
  pageErrors: string[];
  consoleErrors: string[];
  selection?: GateLog["selection"];
  readPage(): Promise<PageLog>;
}

export interface GateReport {
  steps: Step[];
  pageErrors: string[];
  consoleErrors: string[];
  log: GateLog | null;
  verdict: Verdict | null;
  /** Why the page side could not be read, when it could not. */
  collectionError?: string;
}

/** The report of a gate as it stands; a page that cannot be read leaves the Node side whole. */
export async function collectGate(source: GateSource): Promise<GateReport> {
  const node = {
    steps: [...source.steps],
    pageErrors: [...source.pageErrors],
    consoleErrors: [...source.consoleErrors],
  };
  try {
    const log: GateLog = {
      ...(await source.readPage()),
      pageErrors: node.pageErrors,
      consoleErrors: node.consoleErrors,
      selection: source.selection,
    };
    return { ...node, log, verdict: classifyResizeObserverNotices(log) };
  } catch (error) {
    const collectionError = error instanceof Error ? error.message : String(error);
    return { ...node, log: null, verdict: null, collectionError };
  }
}

export type AttachTarget = Pick<TestInfo, "attach" | "status" | "expectedStatus">;

/** Attaches a report; `partial` says it is not a verdict the test reached. */
async function attachReport(target: AttachTarget, report: GateReport, partial: boolean) {
  await target.attach(GATE_ATTACHMENT, {
    body: JSON.stringify({ partial, status: target.status, ...report }, null, 2),
    contentType: "application/json",
  });
}

/**
 * The teardown of a gate no verdict was reached for: its report is attached as
 * it stands. A test that failed keeps its own failure — nothing here throws
 * over it. A test that passed without a verdict is a harness mistake, and
 * fails here, with whatever went wrong reading the page.
 */
export async function attachUnjudgedGate(target: AttachTarget, source: GateSource) {
  const report = await collectGate(source);
  await attachReport(target, report, true);
  if (target.status === "skipped" || target.status !== target.expectedStatus) return;
  const reading = report.collectionError ? ` Leitura da página: ${report.collectionError}` : "";
  throw new Error(`O gate do ResizeObserver terminou sem veredito.${reading}`);
}

interface Registered extends GateSource {
  judged: boolean;
}

const registered = new Map<AttachTarget, Registered>();

/** Records a gate for its test's teardown; one gate per test. */
export function registerGate(testInfo: AttachTarget, source: GateSource): Registered {
  if (registered.has(testInfo)) throw new Error("Já há um gate do ResizeObserver neste teste.");
  const entry = Object.assign(source, { judged: false });
  registered.set(testInfo, entry);
  return entry;
}

/** For an `afterEach`: attaches the gate of a test that never reached its verdict. */
export async function attachGateOnTeardown(testInfo: AttachTarget) {
  const entry = registered.get(testInfo);
  registered.delete(testInfo);
  if (!entry || entry.judged) return;
  await attachUnjudgedGate(testInfo, entry);
}

/**
 * Installs the gate on a page before it navigates. The test marks each
 * opening just before the tap that opens the picker, settles it once the
 * picker is up, and confirms its cleanup once the picker is gone. The file
 * that uses it registers `attachGateOnTeardown` in an `afterEach`.
 */
export async function installResizeObserverGate(page: Page, testInfo: TestInfo) {
  type GateWindow = { __roGate: PageGate };
  const source = registerGate(testInfo, {
    steps: [],
    pageErrors: [],
    consoleErrors: [],
    readPage: () =>
      page.evaluate(() => {
        const { markers, native, callbacks, truncated, geometry } = (
          window as unknown as GateWindow
        ).__roGate;
        return { markers, native, callbacks, truncated, geometry };
      }),
  });
  page.on("pageerror", (error) => source.pageErrors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") source.consoleErrors.push(message.text());
  });
  await page.addInitScript(installPageGate, {
    stableFrames: STABLE_FRAMES,
    maxFrames: MAX_SETTLE_FRAMES,
    maxRecords: MAX_CALLBACK_RECORDS,
    watched: WATCHED_SURFACES,
  });
  let opening = 0;

  return {
    /** Call just before the tap that opens the picker: a new window, counted on its own. */
    async opening() {
      opening++;
      source.steps.push({ step: "open", opening });
      await page.evaluate(
        (n) =>
          (window as unknown as GateWindow).__roGate.markers.push({
            type: "open",
            opening: n,
            t: performance.now(),
          }),
        opening,
      );
    },
    /** Twenty unchanged frames, then the picker's geometry; false if it never got there. */
    async settle() {
      const n = opening;
      const stable = await page.evaluate(
        (m) => (window as unknown as GateWindow).__roGate.settle(m),
        n,
      );
      source.steps.push({ step: "settle", opening: n, stable });
      return stable;
    },
    /** Call once the picker is gone: twenty frames of no callbacks, nothing observed. */
    async closed() {
      source.steps.push({ step: "closed", opening });
      await page.evaluate((n) => (window as unknown as GateWindow).__roGate.closed(n), opening);
    },
    /** One more geometry sample of the current opening. */
    async measure() {
      source.steps.push({ step: "measure", opening });
      await page.evaluate((n) => (window as unknown as GateWindow).__roGate.measure(n), opening);
    },
    /** What the accepted notices are conditional on. */
    selected(hit: boolean, reactions: number) {
      source.selection = { hit, reactions };
    },
    log: async () => (await collectGate(source)).log,
    /**
     * The verdict, with the full report attached and the notice counts per
     * opening annotated. A page that cannot be read fails here, report attached.
     */
    async verdict() {
      source.judged = true;
      const report = await collectGate(source);
      await attachReport(testInfo, report, false);
      if (!report.verdict) {
        throw new Error(`O gate do ResizeObserver não leu a página: ${report.collectionError}`);
      }
      testInfo.annotations.push({
        type: "ro-notices",
        description: JSON.stringify({
          notices: report.verdict.notices,
          shared: report.verdict.shared,
        }),
      });
      return report.verdict;
    },
  };
}
