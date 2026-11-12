import { describe, expect, it, vi } from "vitest";

import {
  GATE_ATTACHMENT,
  KNOWN_CONSOLE_ERRORS,
  RESIZE_OBSERVER_LOOP,
  WATCHED_SURFACES,
  attachGateOnTeardown,
  attachUnjudgedGate,
  classifyResizeObserverNotices,
  collectGate,
  provokedErrorProblems,
  registerGate,
  type AttachTarget,
  type CallbackRecord,
  type GateLog,
  type GateSource,
  type Marker,
  type NativeError,
  type PageLog,
} from "./resizeObserverGate";

// Geometry of the WebKit evidence of issue #1152 at 320×210 (Phase 2, A).
const viewport = { top: 0, bottom: 210, left: 0, right: 320 };
const sheet = { top: 140, bottom: 202, left: 8, right: 312 };
const picker = { top: 8, bottom: 133, left: 8, right: 312 };

const notice = (t: number, update: number | null = 1): NativeError => ({
  kind: "error",
  message: RESIZE_OBSERVER_LOOP,
  t,
  update,
});

/** The touch picker's own observer, on the menu and the picker, in `update`. */
const watchedCallback = (update: number, t = 140): CallbackRecord => ({
  observer: 7,
  update,
  t,
  targets: [...WATCHED_SURFACES],
  watched: true,
});

/** An observer of something else: the message list, say. */
const otherCallback = (update: number, t = 140): CallbackRecord => ({
  observer: 2,
  update,
  t,
  targets: ["DIV"],
  watched: false,
});

/** One opening at t=100, stable at t=500, closed clean at t=900. */
function opening(n: number, offset = 0): Marker[] {
  return [
    { type: "open", opening: n, t: 100 + offset },
    {
      type: "settled",
      opening: n,
      t: 500 + offset,
      stable: true,
      frames: 24,
      observing: [...WATCHED_SURFACES],
    },
    { type: "closed", opening: n, t: 900 + offset, callbacks: 0, observing: [] },
  ];
}

/** The ready-config log of A: one notice in the opening, from the picker's update. */
function accepted(overrides: Partial<GateLog> = {}): GateLog {
  return {
    markers: opening(1),
    native: [notice(150)],
    callbacks: [watchedCallback(1)],
    truncated: false,
    geometry: [{ opening: 1, picker, sheet, viewport }],
    pageErrors: [RESIZE_OBSERVER_LOOP],
    consoleErrors: [...KNOWN_CONSOLE_ERRORS],
    selection: { hit: true, reactions: 1 },
    ...overrides,
  };
}

const reasons = (log: GateLog) =>
  classifyResizeObserverNotices(log).failures.map((failure) => failure.reason);

describe("classifyResizeObserverNotices", () => {
  it("accepts the known notice inside a settled, cleaned-up, functional opening", () => {
    expect(classifyResizeObserverNotices(accepted())).toEqual({
      failures: [],
      notices: { 1: 1 },
      shared: {},
    });
  });

  it("counts the native channel only: four events behind one pageerror are four, not five", () => {
    const log = accepted({
      native: [150, 160, 170, 180].map((t, i) => notice(t, i + 1)),
      callbacks: [1, 2, 3, 4].map((update) => watchedCallback(update)),
    });
    expect(classifyResizeObserverNotices(log)).toEqual({
      failures: [],
      notices: { 1: 4 },
      shared: {},
    });
  });

  it("gives a second opening its own window and its own count", () => {
    const log = accepted({
      markers: [...opening(1), ...opening(2, 1000)],
      native: [notice(150, 1), notice(1150, 2)],
      callbacks: [watchedCallback(1), watchedCallback(2, 1140)],
      pageErrors: [],
      geometry: [
        { opening: 1, picker, sheet, viewport },
        { opening: 2, picker, sheet, viewport },
      ],
    });
    expect(classifyResizeObserverNotices(log)).toEqual({
      failures: [],
      notices: { 1: 1, 2: 1 },
      shared: {},
    });
  });

  it("passes a test with no notice and no selection", () => {
    expect(reasons(accepted({ native: [], pageErrors: [], selection: undefined }))).toEqual([]);
  });

  describe("attribution", () => {
    it("fails a notice from an update in which only another observer ran", () => {
      const failures = classifyResizeObserverNotices(
        accepted({ callbacks: [otherCallback(1)] }),
      ).failures;
      expect(failures).toEqual([
        {
          reason: "unattributed-notice",
          detail: "abertura 1, t=150, update 1: #2[DIV]",
        },
      ]);
    });

    it("fails a notice that arrived outside any update", () => {
      expect(reasons(accepted({ native: [notice(150, null)] }))).toEqual(["unattributed-notice"]);
    });

    it("fails a notice whose update ran the picker's observer only in another frame", () => {
      expect(reasons(accepted({ callbacks: [watchedCallback(2)] }))).toEqual([
        "unattributed-notice",
      ]);
    });

    it("says so when the callback records were truncated", () => {
      const failures = classifyResizeObserverNotices(
        accepted({ callbacks: [], truncated: true }),
      ).failures;
      expect(failures).toEqual([
        {
          reason: "unattributed-notice",
          detail: "abertura 1, t=150, update 1: nenhum callback (registro de callbacks truncado)",
        },
      ]);
    });

    it("accepts a notice of an update shared with another observer, and counts it shared", () => {
      expect(
        classifyResizeObserverNotices(
          accepted({ callbacks: [otherCallback(1), watchedCallback(1)] }),
        ),
      ).toEqual({ failures: [], notices: { 1: 1 }, shared: { 1: 1 } });
    });

    it("still counts an unattributed notice against the limit", () => {
      const native = [1, 2, 3, 4, 5].map((update) => notice(100 + update * 10, update));
      const callbacks = [1, 2, 3, 4].map((update) => watchedCallback(update));
      expect(reasons(accepted({ native, callbacks }))).toEqual([
        "unattributed-notice",
        "too-many-notices",
      ]);
    });
  });

  describe("persistence", () => {
    it("fails a fifth notice in one opening", () => {
      const native = [1, 2, 3, 4, 5].map((update) => notice(100 + update * 10, update));
      const callbacks = [1, 2, 3, 4, 5].map((update) => watchedCallback(update));
      expect(reasons(accepted({ native, callbacks }))).toEqual(["too-many-notices"]);
    });

    it("fails an opening that never had twenty stable frames", () => {
      const markers: Marker[] = [
        { type: "open", opening: 1, t: 100 },
        {
          type: "settled",
          opening: 1,
          t: 4100,
          stable: false,
          frames: 240,
          observing: [...WATCHED_SURFACES],
        },
        { type: "closed", opening: 1, t: 4200, callbacks: 0, observing: [] },
      ];
      const native = Array.from({ length: 240 }, (_, frame) => notice(101 + frame * 16, frame));
      const callbacks = Array.from({ length: 240 }, (_, frame) => otherCallback(frame));
      expect(reasons(accepted({ markers, native, callbacks }))).toEqual([
        ...Array<string>(240).fill("unattributed-notice"),
        "too-many-notices",
        "not-settled",
      ]);
    });

    it("fails a notice after the opening settled", () => {
      expect(reasons(accepted({ native: [notice(150), notice(600)] }))).toEqual([
        "notice-after-stability",
      ]);
    });

    it("fails an opening whose stability was never checked", () => {
      const markers: Marker[] = [
        { type: "open", opening: 1, t: 100 },
        { type: "closed", opening: 1, t: 900, callbacks: 0, observing: [] },
      ];
      expect(reasons(accepted({ markers }))).toEqual(["not-settled"]);
    });
  });

  describe("a different error", () => {
    it("fails a thrown error in both channels, named by channel", () => {
      const message = "controle negativo: erro diferente";
      const failures = classifyResizeObserverNotices(
        accepted({
          native: [notice(150), { kind: "error", message, t: 160, update: null }],
          pageErrors: [RESIZE_OBSERVER_LOOP, message],
        }),
      ).failures;
      expect(failures).toEqual([
        { reason: "unexpected-error", detail: `window.error: ${message}` },
        { reason: "unexpected-error", detail: `pageerror: ${message}` },
      ]);
    });

    it("fails an unhandled rejection", () => {
      const native: NativeError[] = [
        { kind: "unhandledrejection", message: "falhou", t: 150, update: null },
      ];
      expect(reasons(accepted({ native, pageErrors: [] }))).toEqual(["unexpected-error"]);
    });

    it("takes the message exactly: another engine's wording is not the known notice", () => {
      const message = "ResizeObserver loop limit exceeded";
      const native: NativeError[] = [{ kind: "error", message, t: 150, update: 1 }];
      expect(reasons(accepted({ native, pageErrors: [] }))).toEqual(["unexpected-error"]);
    });

    it("fails a pageerror the page itself never saw", () => {
      expect(reasons(accepted({ native: [] }))).toEqual(["unmatched-pageerror"]);
    });

    it("fails a console error that is not known in advance", () => {
      expect(reasons(accepted({ consoleErrors: ["Falha de rede"] }))).toEqual([
        "unexpected-console-error",
      ]);
    });
  });

  describe("RO-N2: only the provoked error", () => {
    const message = "controle negativo: erro diferente";
    const thrown = (text: string, t = 600): NativeError => ({
      kind: "error",
      message: text,
      t,
      update: null,
    });
    /** The provoked error after the opening settled, in both channels, as Chromium words it. */
    const provoked = (overrides: Partial<GateLog> = {}) =>
      accepted({
        native: [notice(150), thrown(`Uncaught Error: ${message}`)],
        pageErrors: [RESIZE_OBSERVER_LOOP, message],
        ...overrides,
      });
    const problems = (log: GateLog) =>
      provokedErrorProblems(classifyResizeObserverNotices(log), message);

    it("recognizes the provoked error alone, once in each channel", () => {
      expect(problems(provoked())).toEqual([]);
      // WebKit words the window event without "Uncaught".
      expect(problems(provoked({ native: [notice(150), thrown(`Error: ${message}`)] }))).toEqual(
        [],
      );
    });

    it("rejects a second exception and a console error that the reasons alone let through", () => {
      const log = provoked({
        native: [
          notice(150),
          thrown(`Uncaught Error: ${message}`),
          thrown("Uncaught Error: outra"),
        ],
        pageErrors: [RESIZE_OBSERVER_LOOP, message, "outra"],
        consoleErrors: [...KNOWN_CONSOLE_ERRORS, "algo deu errado"],
      });
      // The previous RO-N2 check looked at reasons only, and allowed exactly these.
      expect(new Set(reasons(log))).toEqual(
        new Set(["unexpected-error", "unexpected-console-error"]),
      );
      expect(problems(log)).toEqual([
        "unexpected-error: window.error: Uncaught Error: outra",
        "unexpected-error: pageerror: outra",
        "unexpected-console-error: algo deu errado",
      ]);
    });

    it("rejects an unhandled rejection", () => {
      const native: NativeError[] = [
        notice(150),
        thrown(`Uncaught Error: ${message}`),
        { kind: "unhandledrejection", message: "falhou", t: 610, update: null },
      ];
      expect(problems(provoked({ native }))).toEqual([
        "unexpected-error: window.unhandledrejection: falhou",
      ]);
    });

    it("rejects the provoked error missing from a channel, or thrown twice", () => {
      expect(problems(provoked({ pageErrors: [RESIZE_OBSERVER_LOOP] }))).toEqual([
        "pageerror do erro provocado: 0",
      ]);
      expect(problems(provoked({ native: [notice(150)] }))).toEqual([
        "window.error do erro provocado: 0",
      ]);
      const twice = provoked({ pageErrors: [RESIZE_OBSERVER_LOOP, message, message] });
      expect(problems(twice)).toEqual(["pageerror do erro provocado: 2"]);
    });

    it("lets the console echo the provoked error, and nothing that merely contains it", () => {
      expect(problems(provoked({ consoleErrors: [`Uncaught Error: ${message}`] }))).toEqual([]);
      expect(problems(provoked({ consoleErrors: [`antes ${message}`] }))).toEqual([
        `unexpected-console-error: antes ${message}`,
      ]);
    });

    it("rejects any other reason the gate found", () => {
      expect(problems(provoked({ selection: { hit: false, reactions: 1 } }))).toEqual([
        expect.stringMatching(/^missing-functional-evidence: /),
      ]);
    });
  });

  describe("a notice with the picker closed", () => {
    it("fails one after the picker closed", () => {
      expect(reasons(accepted({ native: [notice(150), notice(950, 2)] }))).toEqual([
        "notice-while-closed",
      ]);
    });

    it("fails one before any picker opened", () => {
      expect(reasons(accepted({ native: [notice(50)] }))).toEqual(["notice-while-closed"]);
    });

    it("fails an observer still watching, or still called, once the picker closed", () => {
      const markers: Marker[] = [
        ...opening(1).slice(0, 2),
        { type: "closed", opening: 1, t: 900, callbacks: 2, observing: ["Reagir à mensagem"] },
      ];
      expect(reasons(accepted({ markers }))).toEqual([
        "observer-not-disconnected",
        "callbacks-after-close",
      ]);
    });

    it("fails an opening whose cleanup was never checked", () => {
      expect(reasons(accepted({ markers: opening(1).slice(0, 2) }))).toEqual([
        "unverified-cleanup",
      ]);
    });
  });

  describe("observed surfaces", () => {
    it.each([
      ["the menu", ["Escolher reação"], "Reagir à mensagem"],
      ["the picker", ["Reagir à mensagem"], "Escolher reação"],
      ["either", [], "Escolher reação, Reagir à mensagem"],
    ])("fails an opening that settled without observing %s", (_, observing, missing) => {
      const [open, settled, closed] = opening(1);
      const markers = [open, { ...settled, observing } as Marker, closed];
      const failures = classifyResizeObserverNotices(
        accepted({ markers, native: [], pageErrors: [] }),
      ).failures;
      expect(failures).toEqual([
        { reason: "observer-missing", detail: `abertura 1: ninguém observava ${missing}` },
      ]);
    });
  });

  describe("geometry and function", () => {
    it("fails the sheet-only overlap: picker bottom 111 under a menu at 92", () => {
      const geometry = [
        {
          opening: 1,
          picker: { top: 8, bottom: 111, left: 8, right: 312 },
          sheet: { top: 92, bottom: 202, left: 8, right: 312 },
          viewport,
        },
      ];
      expect(reasons(accepted({ geometry }))).toEqual(["picker-overlaps-sheet"]);
    });

    it("lets boxes that only touch, within subpixel rounding, be", () => {
      const geometry = [{ opening: 1, picker: { ...picker, bottom: 140.4 }, sheet, viewport }];
      expect(reasons(accepted({ geometry }))).toEqual([]);
    });

    it("fails a picker leaving the visual viewport", () => {
      const geometry = [{ opening: 1, picker: { ...picker, top: -12 }, sheet, viewport }];
      expect(reasons(accepted({ geometry }))).toEqual(["picker-outside-viewport"]);
    });

    it("fails a geometry sample with no picker in it", () => {
      const geometry = [{ opening: 1, picker: null, sheet, viewport }];
      expect(reasons(accepted({ geometry }))).toEqual(["missing-functional-evidence"]);
    });

    it.each([
      ["no selection", undefined],
      ["a missed hit-test", { hit: false, reactions: 1 }],
      ["two reactions", { hit: true, reactions: 2 }],
      ["no reaction", { hit: true, reactions: 0 }],
    ])("accepts no notice with %s", (_, selection) => {
      expect(reasons(accepted({ selection }))).toEqual(["missing-functional-evidence"]);
    });
  });
});

/** A gate's sources, the page side answering with the A log or with `failure`. */
function source(failure?: Error): GateSource {
  const { markers, native, callbacks, truncated, geometry } = accepted();
  const page: PageLog = { markers, native, callbacks, truncated, geometry };
  return {
    steps: [
      { step: "open", opening: 1 },
      { step: "settle", opening: 1, stable: true },
      { step: "closed", opening: 1 },
    ],
    pageErrors: [RESIZE_OBSERVER_LOOP],
    consoleErrors: [],
    selection: { hit: true, reactions: 1 },
    readPage: failure ? () => Promise.reject(failure) : () => Promise.resolve(page),
  };
}

/** A test's info as the teardown sees it, keeping what is attached. */
function testInfo(status: AttachTarget["status"], expectedStatus: AttachTarget["expectedStatus"]) {
  const attachments: { name: string; body: Record<string, unknown> }[] = [];
  const target: AttachTarget = {
    status,
    expectedStatus,
    attach: vi.fn(async (name: string, options: { body?: string | Buffer }) => {
      attachments.push({ name, body: JSON.parse(String(options.body)) });
    }),
  };
  return { target, attachments };
}

describe("collectGate", () => {
  it("reports both sides and the verdict", async () => {
    const report = await collectGate(source());
    expect(report.verdict).toEqual({ failures: [], notices: { 1: 1 }, shared: {} });
    expect(report.log?.pageErrors).toEqual([RESIZE_OBSERVER_LOOP]);
    expect(report.steps.map((step) => step.step)).toEqual(["open", "settle", "closed"]);
    expect(report.collectionError).toBeUndefined();
  });

  it("keeps the Node side whole when the page cannot be read, and says why", async () => {
    const report = await collectGate(source(new Error("Target page has been closed")));
    expect(report).toEqual({
      steps: source().steps,
      pageErrors: [RESIZE_OBSERVER_LOOP],
      consoleErrors: [],
      log: null,
      verdict: null,
      collectionError: "Target page has been closed",
    });
  });
});

describe("attachUnjudgedGate", () => {
  it("attaches the partial report of a failed test and leaves its failure alone", async () => {
    const { target, attachments } = testInfo("failed", "passed");
    await expect(attachUnjudgedGate(target, source())).resolves.toBeUndefined();
    expect(attachments).toHaveLength(1);
    expect(attachments[0].name).toBe(GATE_ATTACHMENT);
    expect(attachments[0].body).toMatchObject({
      partial: true,
      status: "failed",
      verdict: { failures: [], notices: { 1: 1 } },
    });
  });

  it("attaches what it has of a timed-out test whose page is gone, without throwing", async () => {
    const { target, attachments } = testInfo("timedOut", "passed");
    await attachUnjudgedGate(target, source(new Error("Target page has been closed")));
    expect(attachments[0].body).toMatchObject({
      partial: true,
      status: "timedOut",
      log: null,
      pageErrors: [RESIZE_OBSERVER_LOOP],
      collectionError: "Target page has been closed",
    });
  });

  it("fails a passing test that never reached its verdict, after attaching", async () => {
    const { target, attachments } = testInfo("passed", "passed");
    await expect(attachUnjudgedGate(target, source())).rejects.toThrow(
      "O gate do ResizeObserver terminou sem veredito.",
    );
    expect(attachments).toHaveLength(1);
  });

  it("names the reading error when a passing test without a verdict lost its page", async () => {
    const { target } = testInfo("passed", "passed");
    await expect(attachUnjudgedGate(target, source(new Error("closed")))).rejects.toThrow(
      "Leitura da página: closed",
    );
  });

  it("does not fail a skipped test", async () => {
    const { target, attachments } = testInfo("skipped", "skipped");
    await expect(attachUnjudgedGate(target, source())).resolves.toBeUndefined();
    expect(attachments).toHaveLength(1);
  });
});

describe("attachGateOnTeardown", () => {
  it("attaches once for a gate without a verdict, and forgets it", async () => {
    const { target, attachments } = testInfo("failed", "passed");
    registerGate(target, source());
    await attachGateOnTeardown(target);
    await attachGateOnTeardown(target);
    expect(attachments).toHaveLength(1);
  });

  it("adds nothing for a gate whose verdict already attached its report", async () => {
    const { target, attachments } = testInfo("passed", "passed");
    registerGate(target, source()).judged = true;
    await attachGateOnTeardown(target);
    expect(attachments).toEqual([]);
  });

  it("does nothing for a test without a gate", async () => {
    const { target, attachments } = testInfo("passed", "passed");
    await attachGateOnTeardown(target);
    expect(attachments).toEqual([]);
  });

  it("refuses a second gate in one test", () => {
    const { target } = testInfo("passed", "passed");
    registerGate(target, source());
    expect(() => registerGate(target, source())).toThrow("Já há um gate");
  });
});
