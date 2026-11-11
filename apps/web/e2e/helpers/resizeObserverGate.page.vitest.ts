// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  RESIZE_OBSERVER_LOOP,
  WATCHED_SURFACES,
  installPageGate,
  type PageLog,
} from "./resizeObserverGate";

interface PageGate extends PageLog {
  settle(opening: number): Promise<boolean>;
  closed(opening: number): Promise<void>;
}

/** jsdom has no ResizeObserver: this one delivers when a test says so. */
class FakeObserver {
  static instances: FakeObserver[] = [];
  callback: ResizeObserverCallback;
  constructor(callback: ResizeObserverCallback) {
    this.callback = callback;
    FakeObserver.instances.push(this);
  }
  observe() {}
  unobserve() {}
  disconnect() {}
  /** Calls back as the browser does: the observer as `this` and as the second argument. */
  deliver(...targets: Element[]) {
    const entries = targets.map((target) => ({ target }) as ResizeObserverEntry);
    this.callback.call(this, entries, this as unknown as ResizeObserver);
    return entries;
  }
}

const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));
const loopError = () =>
  window.dispatchEvent(new ErrorEvent("error", { message: RESIZE_OBSERVER_LOOP }));

function surface(name: string, role?: string) {
  const element = document.createElement("div");
  element.setAttribute("aria-label", name);
  if (role) element.setAttribute("role", role);
  document.body.append(element);
  return element;
}

let gate: PageGate;

function install(maxRecords = 100) {
  window.ResizeObserver = FakeObserver as unknown as typeof ResizeObserver;
  installPageGate({ stableFrames: 2, maxFrames: 8, maxRecords, watched: WATCHED_SURFACES });
  gate = (window as unknown as { __roGate: PageGate }).__roGate;
}

/** A new observer through the gate's wrapper, and the fake behind it. */
function observer(callback: ResizeObserverCallback = () => {}) {
  const wrapped = new window.ResizeObserver(callback);
  return { wrapped, fake: FakeObserver.instances.at(-1)! };
}

beforeEach(() => {
  document.body.replaceChildren();
  FakeObserver.instances = [];
});

describe("installPageGate", () => {
  it("passes callbacks through and tags them, and the loop error, with their frame", async () => {
    install();
    const picker = surface("Escolher reação");
    const callback = vi.fn();
    const { wrapped, fake } = observer(callback);
    wrapped.observe(picker);

    fake.deliver(picker);
    loopError();
    expect(callback).toHaveBeenCalledWith([{ target: picker }], fake);
    expect(gate.callbacks).toEqual([
      {
        observer: 0,
        update: 1,
        t: expect.any(Number),
        targets: ["Escolher reação"],
        watched: true,
      },
    ]);
    expect(gate.native).toEqual([
      { kind: "error", message: RESIZE_OBSERVER_LOOP, t: expect.any(Number), update: 1 },
    ]);

    // The frame is over at the next frame's animation callbacks.
    await frame();
    await frame();
    loopError();
    expect(gate.native[1].update).toBeNull();
    fake.deliver(picker);
    expect(gate.callbacks[1].update).toBe(2);
  });

  it("calls the callback once, with the observer as this and the entries untouched", () => {
    install();
    const picker = surface("Escolher reação");
    const calls: { self: unknown; entries: ResizeObserverEntry[]; observer: ResizeObserver }[] = [];
    const { wrapped, fake } = observer(function (this: unknown, entries, observer) {
      calls.push({ self: this, entries, observer });
    });
    wrapped.observe(picker);
    const entries = fake.deliver(picker);
    expect(calls).toHaveLength(1);
    expect(calls[0].self).toBe(wrapped);
    expect(calls[0].observer).toBe(wrapped);
    expect(calls[0].entries).toBe(entries);
    expect(gate.callbacks).toHaveLength(1);
  });

  it("lets the callback's exception through, after recording the callback", () => {
    install();
    const picker = surface("Escolher reação");
    const failure = new Error("do callback");
    const { wrapped, fake } = observer(() => {
      throw failure;
    });
    wrapped.observe(picker);
    expect(() => fake.deliver(picker)).toThrow(failure);
    expect(gate.callbacks).toMatchObject([{ observer: 0, targets: ["Escolher reação"] }]);
  });

  it("passes observe, unobserve and disconnect on to the native observer", () => {
    install();
    const spies = (["observe", "unobserve", "disconnect"] as const).map((name) =>
      vi.spyOn(FakeObserver.prototype, name),
    );
    const picker = surface("Escolher reação");
    const { wrapped } = observer();
    wrapped.observe(picker, { box: "border-box" });
    wrapped.unobserve(picker);
    wrapped.disconnect();
    expect(spies[0]).toHaveBeenCalledWith(picker, { box: "border-box" });
    expect(spies[1]).toHaveBeenCalledWith(picker);
    expect(spies[2]).toHaveBeenCalledOnce();
    spies.forEach((spy) => spy.mockRestore());
  });

  it("tells an observer of something else from the picker's, by its targets", () => {
    install();
    const { wrapped, fake } = observer();
    const list = document.createElement("section");
    wrapped.observe(list);
    fake.deliver(list);
    expect(gate.callbacks[0]).toMatchObject({ targets: ["SECTION"], watched: false });
  });

  it("records an unhandled rejection in the native channel", () => {
    install();
    const event = new Event("unhandledrejection") as Event & { reason: unknown };
    event.reason = new Error("falhou");
    window.dispatchEvent(event);
    expect(gate.native.at(-1)).toMatchObject({ kind: "unhandledrejection", message: "falhou" });
  });

  it("settles with the surfaces being observed, and closes with none", async () => {
    install();
    const picker = surface("Escolher reação", "dialog");
    const menu = surface("Reagir à mensagem", "toolbar");
    const { wrapped, fake } = observer();
    wrapped.observe(picker);
    wrapped.observe(menu);

    expect(await gate.settle(1)).toBe(true);
    expect(gate.markers.at(-1)).toMatchObject({
      type: "settled",
      stable: true,
      observing: ["Escolher reação", "Reagir à mensagem"],
    });
    // jsdom lays nothing out: the boxes are zero, but they are the surfaces'.
    const zero = { top: 0, bottom: 0, left: 0, right: 0 };
    expect(gate.geometry.at(-1)).toMatchObject({ opening: 1, picker: zero, sheet: zero });

    wrapped.disconnect();
    const closing = gate.closed(1);
    fake.deliver(menu); // a callback after closing is counted
    await closing;
    expect(gate.markers.at(-1)).toMatchObject({ type: "closed", callbacks: 1, observing: [] });
  });

  it("still sees a surface that left the page without being unobserved", async () => {
    install();
    const picker = surface("Escolher reação");
    const menu = surface("Reagir à mensagem");
    const { wrapped } = observer();
    wrapped.observe(picker);
    wrapped.observe(menu);
    wrapped.unobserve(menu);
    picker.remove(); // gone from the page, never unobserved: not cleaned up
    await gate.closed(1);
    expect(gate.markers.at(-1)).toMatchObject({ observing: ["Escolher reação"] });
  });

  it("gives up settling when something keeps changing, after the frame limit", async () => {
    install();
    let running = true;
    const churn = () => {
      loopError();
      if (running) requestAnimationFrame(churn);
    };
    requestAnimationFrame(churn);
    expect(await gate.settle(1)).toBe(false);
    running = false;
    expect(gate.markers.at(-1)).toMatchObject({ type: "settled", stable: false, frames: 8 });
  });

  it("stops recording callbacks past the limit, and says so", () => {
    install(1);
    const { wrapped, fake } = observer();
    const picker = surface("Escolher reação");
    wrapped.observe(picker);
    fake.deliver(picker);
    fake.deliver(picker);
    expect(gate.callbacks).toHaveLength(1);
    expect(gate.truncated).toBe(true);
  });
});
