import { describe, expect, it } from "vitest";

import { initialPresentation, transition } from "./callPresentation";

describe("call presentation", () => {
  it("moves through incoming, connecting, and floating", () => {
    const incoming = transition(initialPresentation, { type: "INCOMING" });
    const connecting = transition(incoming, { type: "CONNECT" });

    expect(incoming).toEqual({ mode: "incoming" });
    expect(connecting).toEqual({ mode: "connecting" });
    expect(transition(connecting, { type: "CONNECTED", dedicated: false })).toEqual({
      mode: "active_floating",
    });
  });

  it("moves from floating through handoff to dedicated", () => {
    const handoff = transition({ mode: "active_floating" }, { type: "HANDOFF_START" });

    expect(handoff).toEqual({ mode: "handoff_to_tab" });
    expect(transition(handoff, { type: "HANDOFF_ACK" })).toEqual({
      mode: "active_dedicated_tab",
    });
  });

  it("recovers a lost dedicated owner to floating", () => {
    const recovering = transition({ mode: "active_dedicated_tab" }, { type: "OWNER_LOST" });

    expect(recovering).toEqual({ mode: "recovering_to_floating" });
    expect(transition(recovering, { type: "RECOVERED" })).toEqual({
      mode: "active_floating",
    });
  });

  it("rolls a timed-out handoff back through recovery", () => {
    const recovering = transition({ mode: "handoff_to_tab" }, { type: "HANDOFF_TIMEOUT" });

    expect(recovering).toEqual({ mode: "recovering_to_floating" });
    expect(transition(recovering, { type: "RECOVERED" })).toEqual({ mode: "active_floating" });
  });

  it("restores the active presentation after reconnecting", () => {
    const reconnecting = transition({ mode: "active_dedicated_tab" }, { type: "RECONNECTING" });

    expect(reconnecting).toEqual({
      mode: "reconnecting",
      resume: "active_dedicated_tab",
    });
    expect(transition(reconnecting, { type: "RECONNECTED" })).toEqual({
      mode: "active_dedicated_tab",
    });
  });

  it("rejects impossible transitions and only resets terminal states", () => {
    expect(transition(initialPresentation, { type: "HANDOFF_ACK" })).toBe(initialPresentation);
    expect(transition(initialPresentation, { type: "RESET" })).toBe(initialPresentation);
    expect(transition({ mode: "ended" }, { type: "RESET" })).toEqual({ mode: "idle" });
    expect(transition({ mode: "failed" }, { type: "RESET" })).toEqual({ mode: "idle" });
    const connecting = { mode: "connecting" } as const;
    const handoff = { mode: "handoff_to_tab" } as const;
    const recovering = { mode: "recovering_to_floating" } as const;
    expect(transition(connecting, { type: "INCOMING" })).toBe(connecting);
    expect(transition(handoff, { type: "INCOMING" })).toBe(handoff);
    expect(transition(recovering, { type: "INCOMING" })).toBe(recovering);
  });

  it("covers terminal, reconnect, and recovery guards deterministically", () => {
    expect(transition({ mode: "active_floating" }, { type: "END" })).toEqual({ mode: "ended" });
    expect(transition({ mode: "active_floating" }, { type: "FAIL" })).toEqual({ mode: "failed" });
    expect(transition(initialPresentation, { type: "END" })).toBe(initialPresentation);
    expect(transition(initialPresentation, { type: "FAIL" })).toBe(initialPresentation);
    expect(transition({ mode: "incoming" }, { type: "INCOMING" })).toEqual({ mode: "incoming" });
    expect(transition({ mode: "connecting" }, { type: "CONNECTED", dedicated: true })).toEqual({
      mode: "active_dedicated_tab",
    });
    expect(transition({ mode: "active_floating" }, { type: "RECONNECTING" })).toEqual({
      mode: "reconnecting",
      resume: "active_floating",
    });
    expect(transition({ mode: "handoff_to_tab" }, { type: "OWNER_LOST" })).toEqual({
      mode: "recovering_to_floating",
    });
    expect(transition({ mode: "recovering_to_floating" }, { type: "FAIL" })).toEqual({
      mode: "failed",
    });
    expect(
      transition({ mode: "reconnecting", resume: "active_dedicated_tab" }, { type: "OWNER_LOST" }),
    ).toEqual({ mode: "recovering_to_floating" });
    const floatingReconnect = { mode: "reconnecting", resume: "active_floating" } as const;
    expect(transition(floatingReconnect, { type: "OWNER_LOST" })).toBe(floatingReconnect);
    const missingResume = { mode: "reconnecting" } as const;
    expect(transition(missingResume, { type: "RECONNECTED" })).toBe(missingResume);
    expect(transition({ mode: "ended" }, { type: "END" })).toEqual({ mode: "ended" });
    expect(transition({ mode: "failed" }, { type: "FAIL" })).toEqual({ mode: "failed" });
  });
});
