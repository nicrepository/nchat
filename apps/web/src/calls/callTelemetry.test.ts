import { describe, expect, it, vi } from "vitest";

import { emitCallTechnicalEvent } from "./callTelemetry";

describe("call technical telemetry", () => {
  it("emits only the event name", () => {
    const listener = vi.fn();
    window.addEventListener("nchat:call-technical-event", listener);

    emitCallTechnicalEvent("handoff-success");

    expect(listener.mock.calls[0]?.[0]).toMatchObject({ detail: { event: "handoff-success" } });
    expect(Object.keys((listener.mock.calls[0]?.[0] as CustomEvent).detail)).toEqual(["event"]);
    window.removeEventListener("nchat:call-technical-event", listener);
  });
});
