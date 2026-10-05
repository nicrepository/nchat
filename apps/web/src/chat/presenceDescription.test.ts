import { describe, expect, it } from "vitest";

import type { PresenceDetail, PresenceInstant } from "./presence";
import { describePresence, formatLastSeen } from "./presenceDescription";

// Local wall-clock instants, so the expected "HH:MM" is what the formatter
// prints in whatever zone the suite runs in.
const NOW = new Date(2026, 9, 1, 16, 0, 0).getTime();

function instant(ms: number): PresenceInstant {
  const secondMs = Math.floor(ms / 1000) * 1000;
  return { secondMs, nanosecond: (ms - secondMs) * 1_000_000 };
}

function at(hours: number, minutes: number, dayOffset = 0): number {
  return new Date(2026, 9, 1 + dayOffset, hours, minutes, 0).getTime();
}

describe("describePresence", () => {
  it("names the activity behind busy and do not disturb", () => {
    expect(describePresence({ state: "busy", activity: "in_call" }, NOW)).toBe(
      "Ocupado · Em chamada",
    );
    expect(describePresence({ state: "dnd", activity: "presenting" })).toBe(
      "Não perturbe · Apresentando",
    );
    expect(describePresence({ state: "busy" }, NOW)).toBe("Ocupado");
  });

  it("says how long somebody has been away, within the day", () => {
    const away = (ms: number): PresenceDetail => ({ state: "away", updatedAt: instant(ms) });
    expect(describePresence(away(NOW - 12 * 60_000), NOW)).toBe("Ausente · há 12 min");
    expect(describePresence(away(NOW - 10_000), NOW)).toBe("Ausente · há 1 min");
    expect(describePresence({ state: "brb", updatedAt: instant(at(9, 5)) }, NOW)).toBe(
      "Volto já · desde 09:05",
    );
    expect(describePresence(away(at(9, 5, -1)), NOW)).toBe("Ausente");
  });

  it("says when an offline person was last seen", () => {
    const offline = (ms: number): PresenceDetail => ({ state: "offline", updatedAt: instant(ms) });
    expect(describePresence(offline(NOW - 18 * 60_000), NOW)).toBe("Offline · visto há 18 min");
    expect(describePresence(offline(at(15, 42) - 60 * 60_000), NOW)).toBe(
      "Offline · visto hoje às 14:42",
    );
    expect(describePresence(offline(at(15, 42, -1)), NOW)).toBe("Offline · visto ontem às 15:42");
    expect(describePresence(offline(at(15, 42, -5)), NOW)).toBe(
      "Offline · visto em 26/09 às 15:42",
    );
  });

  it("uses a last seen learned elsewhere when the store inferred the absence", () => {
    expect(describePresence({ state: "offline" }, NOW, NOW - 30 * 60_000)).toBe(
      "Offline · visto há 30 min",
    );
    expect(describePresence({ state: "offline" }, NOW)).toBe("Offline");
    // An unstamped entry is not an instant.
    expect(
      describePresence({ state: "offline", updatedAt: { secondMs: 0, nanosecond: 0 } }, NOW),
    ).toBe("Offline");
  });

  it("says nothing about elapsed time without a clock", () => {
    expect(describePresence({ state: "offline", updatedAt: instant(NOW - 60_000) })).toBe(
      "Offline",
    );
    expect(describePresence({ state: "away", updatedAt: instant(NOW - 60_000) })).toBe("Ausente");
    expect(describePresence({ state: "online" }, NOW)).toBe("Disponível");
    expect(describePresence({ state: "unknown" }, NOW)).toBe("Status indisponível");
  });
});

describe("formatLastSeen", () => {
  it("never reads a clock skew as the future", () => {
    expect(formatLastSeen(NOW + 60_000, NOW)).toBe("visto agora há pouco");
  });
});
