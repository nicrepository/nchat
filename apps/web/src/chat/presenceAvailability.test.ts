/**
 * Issue #798 additions to the presence store: the effective availability, the
 * activity behind it, and the detail selector the surfaces read. Pure — the
 * reducer and the selectors are functions of the frames they were given.
 */

import { describe, expect, it } from "vitest";

import {
  emptyPresenceState,
  presenceActivityLabels,
  presenceLabel,
  presenceTargetKey,
  reducePresence,
  selectPresence,
  selectPresenceDetail,
  type PresenceSnapshotState,
  type PresenceState,
} from "./presence";

const T1 = "2026-10-01T10:00:00.000Z";
const T2 = "2026-10-01T10:00:05.000Z";
const TARGET = presenceTargetKey("channel", "chan-1");

function update(presence: Record<string, unknown>, targetId = "chan-1") {
  return { type: "presence.updated", target_type: "channel", target_id: targetId, presence };
}

function state(...frames: Record<string, unknown>[]): PresenceSnapshotState {
  return frames.reduce(reducePresence, emptyPresenceState);
}

describe("effective availability on the wire", () => {
  it.each<[string, string, PresenceState]>([
    ["available", "online", "online"],
    ["busy", "online", "busy"],
    ["dnd", "online", "dnd"],
    ["brb", "away", "brb"],
    ["away", "away", "away"],
    ["offline", "offline", "offline"],
  ])("reads availability %s", (availability, legacy, expected) => {
    const s = state(update({ user_id: "u", state: legacy, availability, updated_at: T1 }));
    expect(selectPresence(s, "u", TARGET)).toBe(expected);
  });

  it("falls back to the legacy state for an availability it does not know", () => {
    const s = state(
      update({ user_id: "u", state: "away", availability: "napping", updated_at: T1 }),
    );
    expect(selectPresence(s, "u", TARGET)).toBe("away");
  });

  it("refuses an entry without a legacy state, whatever its availability says", () => {
    const s = state(update({ user_id: "u", availability: "busy", updated_at: T1 }));
    expect(selectPresence(s, "u", TARGET)).toBe("unknown");
  });

  it("keeps the activity behind busy and do not disturb", () => {
    const s = state(
      update({
        user_id: "u",
        state: "online",
        availability: "busy",
        activity: "in_call",
        updated_at: T1,
      }),
    );
    expect(selectPresenceDetail(s, "u", TARGET)).toMatchObject({
      state: "busy",
      activity: "in_call",
    });
  });

  it("drops an activity where it would not be public, or that it does not know", () => {
    const onAvailable = state(
      update({
        user_id: "u",
        state: "online",
        availability: "available",
        activity: "in_call",
        updated_at: T1,
      }),
    );
    expect(selectPresenceDetail(onAvailable, "u", TARGET).activity).toBeUndefined();
    const unknown = state(
      update({
        user_id: "u",
        state: "online",
        availability: "busy",
        activity: "gaming",
        updated_at: T1,
      }),
    );
    expect(selectPresenceDetail(unknown, "u", TARGET).activity).toBeUndefined();
  });

  it("applies a newer change of activity, and treats an exact repeat as one", () => {
    const busy = update({
      user_id: "u",
      state: "online",
      availability: "busy",
      activity: "in_call",
      updated_at: T1,
    });
    const once = state(busy);
    expect(reducePresence(once, busy)).toBe(once);

    const ended = state(
      busy,
      update({ user_id: "u", state: "online", availability: "busy", updated_at: T2 }),
    );
    expect(selectPresenceDetail(ended, "u", TARGET).activity).toBeUndefined();
  });

  it("does not let a conflicting claim at the same instant replace the first", () => {
    const s = state(
      update({
        user_id: "u",
        state: "online",
        availability: "busy",
        activity: "in_call",
        updated_at: T1,
      }),
      update({ user_id: "u", state: "online", availability: "busy", updated_at: T1 }),
    );
    expect(selectPresenceDetail(s, "u", TARGET).activity).toBe("in_call");
  });
});

describe("selectPresenceDetail", () => {
  it("returns the stored entry itself, so a consumer re-renders only on news", () => {
    const first = state(
      update({ user_id: "u", state: "online", availability: "dnd", updated_at: T1 }),
    );
    const unrelated = reducePresence(
      first,
      update({ user_id: "other", state: "online", updated_at: T2 }),
    );
    expect(selectPresenceDetail(unrelated, "u", TARGET)).toBe(
      selectPresenceDetail(first, "u", TARGET),
    );
  });

  it("is offline without an instant when absence was inferred", () => {
    const covered = reducePresence(emptyPresenceState, {
      type: "presence.snapshot",
      target_type: "channel",
      target_id: "chan-1",
      users: [],
      complete: true,
      taken_at: T1,
    });
    expect(selectPresenceDetail(covered, "u", TARGET)).toEqual({ state: "offline" });
  });

  it("is unknown without an id, and across conversations it takes the newest word", () => {
    expect(selectPresenceDetail(emptyPresenceState, "", TARGET).state).toBe("unknown");
    const s = state(
      update({ user_id: "u", state: "online", availability: "busy", updated_at: T1 }, "chan-1"),
      update({ user_id: "u", state: "online", availability: "dnd", updated_at: T2 }, "chan-2"),
    );
    expect(selectPresenceDetail(s, "u").state).toBe("dnd");
    expect(selectPresenceDetail(emptyPresenceState, "u").state).toBe("unknown");
  });
});

describe("labels", () => {
  it("names every state in Portuguese, and never offline for unknown", () => {
    expect(
      (["online", "busy", "dnd", "brb", "away", "offline", "unknown"] as const).map(presenceLabel),
    ).toEqual([
      "Disponível",
      "Ocupado",
      "Não perturbe",
      "Volto já",
      "Ausente",
      "Offline",
      "Status indisponível",
    ]);
    expect(presenceActivityLabels).toEqual({
      in_call: "Em chamada",
      in_meeting: "Em reunião",
      presenting: "Apresentando",
    });
  });
});
