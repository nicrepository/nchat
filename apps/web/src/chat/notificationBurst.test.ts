import { describe, expect, it } from "vitest";

import {
  createBurstGate,
  PRESENTATION_MEMORY_CAPACITY,
  SOUND_COOLDOWN_MS,
} from "./notificationBurst";

/** A clock the test moves, so no window is ever waited out for real. */
function fakeClock(start = 1_000) {
  let current = start;
  return {
    now: () => current,
    advance(ms: number) {
      current += ms;
    },
  };
}

describe("notificationBurst — the memory of what was announced", () => {
  it("remembers an event id it was told about", () => {
    const gate = createBurstGate({ now: fakeClock().now });

    expect(gate.hasPresented("message-1")).toBe(false);
    gate.markPresented("message-1");
    expect(gate.hasPresented("message-1")).toBe(true);
  });

  it("keeps each event id separate", () => {
    const gate = createBurstGate({ now: fakeClock().now });

    gate.markPresented("message-1");

    expect(gate.hasPresented("message-2")).toBe(false);
  });

  it("forgets an event id once its retention elapsed", () => {
    const clock = fakeClock();
    const gate = createBurstGate({ now: clock.now, memoryTtlMs: 60_000 });

    gate.markPresented("message-1");
    clock.advance(59_999);
    expect(gate.hasPresented("message-1")).toBe(true);

    clock.advance(2);
    expect(gate.hasPresented("message-1")).toBe(false);
  });

  it("evicts the oldest id when it is full, and keeps the newest", () => {
    const gate = createBurstGate({ now: fakeClock().now, memoryCapacity: 3 });

    gate.markPresented("oldest");
    gate.markPresented("middle");
    gate.markPresented("newest");
    gate.markPresented("newer-still");

    expect(gate.hasPresented("oldest")).toBe(false);
    expect(gate.hasPresented("middle")).toBe(true);
    expect(gate.hasPresented("newer-still")).toBe(true);
  });

  it("holds a bounded amount of state under a flood far larger than its capacity", () => {
    const gate = createBurstGate({ now: fakeClock().now });

    for (let index = 0; index < 10_000; index += 1) gate.markPresented(`message-${index}`);

    expect(gate.retainedKeyCount()).toBeLessThanOrEqual(PRESENTATION_MEMORY_CAPACITY);
  });

  /**
   * Identity is the id alone. Nothing about the message is retained or
   * compared, which is what makes the store safe to hold: two deliveries of one
   * id are one event whatever their bodies say.
   */
  it("treats one id as one event whatever the delivery carried", () => {
    const gate = createBurstGate({ now: fakeClock().now });

    gate.markPresented("message-1");

    expect(gate.hasPresented("message-1")).toBe(true);
    expect(gate.retainedKeyCount()).toBe(1);
  });
});

describe("notificationBurst — the sound cooldown", () => {
  it("lets the first event of a window chime", () => {
    const gate = createBurstGate({ now: fakeClock().now });

    expect(gate.allowSound("channel:general:room")).toBe(true);
  });

  it("silences the rest of the burst", () => {
    const gate = createBurstGate({ now: fakeClock().now });

    gate.allowSound("channel:general:room");

    for (let index = 0; index < 99; index += 1) {
      expect(gate.allowSound("channel:general:room")).toBe(false);
    }
  });

  it("still silences at the last instant of the window, and chimes at its end", () => {
    const clock = fakeClock();
    const gate = createBurstGate({ now: clock.now });

    expect(gate.allowSound("channel:general:room")).toBe(true);

    clock.advance(SOUND_COOLDOWN_MS - 1);
    expect(gate.allowSound("channel:general:room")).toBe(false);

    clock.advance(1);
    expect(gate.allowSound("channel:general:room")).toBe(true);
  });

  it("does not spend the window on an event it refused", () => {
    const clock = fakeClock();
    const gate = createBurstGate({ now: clock.now });

    gate.allowSound("channel:general:room");
    clock.advance(SOUND_COOLDOWN_MS - 1);
    // Refused, and refusing must not restart the window: otherwise a fast
    // enough burst would silence the conversation for as long as it lasted.
    gate.allowSound("channel:general:room");

    clock.advance(1);
    expect(gate.allowSound("channel:general:room")).toBe(true);
  });

  it("keeps conversations independent of one another", () => {
    const gate = createBurstGate({ now: fakeClock().now });

    expect(gate.allowSound("channel:general:room")).toBe(true);
    expect(gate.allowSound("channel:random:room")).toBe(true);
    expect(gate.allowSound("dm:ana:room")).toBe(true);
  });

  it("gives a message that names the reader its own budget in the same room", () => {
    const gate = createBurstGate({ now: fakeClock().now });

    expect(gate.allowSound("channel:general:room")).toBe(true);
    expect(gate.allowSound("channel:general:named")).toBe(true);
  });

  it("holds a bounded number of cooldown keys under high-cardinality traffic", () => {
    const gate = createBurstGate({ now: fakeClock().now, soundCooldownCapacity: 8 });

    for (let index = 0; index < 5_000; index += 1) gate.allowSound(`channel:${index}:room`);

    expect(gate.retainedKeyCount()).toBeLessThanOrEqual(8);
  });
});
