import { describe, expect, it } from "vitest";

import { selectLatestPin } from "./selectLatestPin";
import type { Message, PinnedItem } from "./chatTypes";

function message(id: string, createdAt = "2026-07-15T10:00:00.000Z"): Message {
  return {
    id,
    senderId: "u1",
    senderDisplayName: "Ana",
    senderEmail: "ana@example.test",
    kind: "user",
    bodyText: `corpo ${id}`,
    bodyFormat: "v3",
    isRemoved: false,
    status: "active",
    createdAt,
    updatedAt: createdAt,
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
  };
}

function pin(id: string, pinnedAt: string, createdAt?: string): PinnedItem {
  return {
    message: message(id, createdAt),
    pinnedByUserId: "u2",
    pinnedAt,
  };
}

describe("selectLatestPin", () => {
  it("returns null when there is nothing pinned", () => {
    expect(selectLatestPin([])).toBeNull();
  });

  it("selects the greatest pinnedAt regardless of the response order", () => {
    const older = pin("m1", "2026-07-15T10:00:00.000Z");
    const newer = pin("m2", "2026-07-15T12:00:00.000Z");

    // The server order must not be what decides: both orders pick the same pin.
    expect(selectLatestPin([older, newer])?.message.id).toBe("m2");
    expect(selectLatestPin([newer, older])?.message.id).toBe("m2");
  });

  it("selects by pin time, not by when the message was written", () => {
    // An old message pinned today is the current pin.
    const oldMessagePinnedNow = pin("m1", "2026-07-15T12:00:00.000Z", "2020-01-01T00:00:00.000Z");
    const newMessagePinnedEarlier = pin(
      "m2",
      "2026-07-15T09:00:00.000Z",
      "2026-07-15T08:59:00.000Z",
    );

    expect(selectLatestPin([newMessagePinnedEarlier, oldMessagePinnedNow])?.message.id).toBe("m1");
  });

  it("falls back to the message timestamp when pinnedAt is missing or unusable", () => {
    const missing = {
      ...pin("m1", ""),
      message: message("m1", "2026-07-15T14:00:00.000Z"),
    };
    const malformed = {
      ...pin("m2", "not-a-date"),
      message: message("m2", "2026-07-15T09:00:00.000Z"),
    };

    // Both fall back to createdAt, so the later message wins — and neither is
    // silently dropped by a NaN comparison.
    expect(selectLatestPin([malformed, missing])?.message.id).toBe("m1");
  });

  it("still selects a pin whose timestamps are all unusable", () => {
    const unusable = {
      ...pin("m1", "not-a-date"),
      message: message("m1", "also-not-a-date"),
    };

    expect(selectLatestPin([unusable])?.message.id).toBe("m1");
  });

  it("prefers a usable pinnedAt over an unusable one", () => {
    const usable = pin("m1", "2020-01-01T00:00:00.000Z", "2020-01-01T00:00:00.000Z");
    const unusable = {
      ...pin("m2", "not-a-date"),
      message: message("m2", "also-not-a-date"),
    };

    expect(selectLatestPin([unusable, usable])?.message.id).toBe("m1");
  });

  it("breaks a tie deterministically by message id", () => {
    const sameInstant = "2026-07-15T12:00:00.000Z";
    const a = pin("aaa", sameInstant);
    const b = pin("bbb", sameInstant);

    // Same answer whichever order the two arrive in.
    expect(selectLatestPin([a, b])?.message.id).toBe("bbb");
    expect(selectLatestPin([b, a])?.message.id).toBe("bbb");
  });

  it("does not mutate or reorder the array it receives", () => {
    const older = pin("m1", "2026-07-15T10:00:00.000Z");
    const newer = pin("m2", "2026-07-15T12:00:00.000Z");
    const pins = [older, newer];
    const snapshot = [...pins];

    selectLatestPin(pins);

    expect(pins).toEqual(snapshot);
    expect(pins[0]).toBe(older);
    expect(pins[1]).toBe(newer);
  });
});
