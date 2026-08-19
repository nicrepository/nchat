import { describe, expect, it } from "vitest";

import { classifySoundEvent, shouldPlayMessageSound } from "./soundRules";
import type { WSMessagePayload } from "./useChatWebSocket";

const currentUserId = "00000000-0000-4000-8000-000000000001";
const otherUserId = "00000000-0000-4000-8000-000000000002";

function payload(overrides: Partial<WSMessagePayload> = {}): WSMessagePayload {
  return {
    id: "msg-1",
    workspace_id: "ws-1",
    channel_id: "channel-1",
    sender_id: "other-1",
    sender_display_name: "Other",
    kind: "user",
    body_text: "hello",
    status: "sent",
    is_removed: false,
    created_at: "2026-08-19T12:00:00Z",
    updated_at: "2026-08-19T12:00:00Z",
    ...overrides,
  };
}

function mentionToken(userId: string, label = "Me") {
  return `@[${label}](mention:user:${userId})`;
}

describe("classifySoundEvent", () => {
  it("classifies a dm target as DM regardless of body content", () => {
    expect(
      classifySoundEvent(
        payload({ dm_conversation_id: "dm-1", channel_id: undefined }),
        "dm",
        currentUserId,
      ),
    ).toEqual({ category: "DM", isMentioned: false });
  });

  it("classifies a group dm target as DM too", () => {
    expect(classifySoundEvent(payload({}), "dm", currentUserId)).toEqual({
      category: "DM",
      isMentioned: false,
    });
  });

  it("classifies a channel message mentioning the current user as MENTION, with isMentioned true", () => {
    const body = `hey ${mentionToken(currentUserId)} check this`;
    expect(classifySoundEvent(payload({ body_text: body }), "channel", currentUserId)).toEqual({
      category: "MENTION",
      isMentioned: true,
    });
  });

  it("does not classify a mention of a different user as MENTION", () => {
    const body = `hey ${mentionToken(otherUserId)} check this`;
    expect(classifySoundEvent(payload({ body_text: body }), "channel", currentUserId)).toEqual({
      category: "STANDARD",
      isMentioned: false,
    });
  });

  it("does not treat literal '@name' text without the official token as a mention", () => {
    const body = "hey @me-1 check this";
    expect(classifySoundEvent(payload({ body_text: body }), "channel", currentUserId)).toEqual({
      category: "STANDARD",
      isMentioned: false,
    });
  });

  it("classifies a plain channel message as STANDARD", () => {
    expect(
      classifySoundEvent(payload({ body_text: "just chatting" }), "channel", currentUserId),
    ).toEqual({ category: "STANDARD", isMentioned: false });
  });

  it("classifies a system message as NONE even if it mentions the user", () => {
    const body = `system note ${mentionToken(currentUserId)}`;
    expect(
      classifySoundEvent(payload({ kind: "system", body_text: body }), "channel", currentUserId),
    ).toEqual({ category: "NONE", isMentioned: false });
  });

  it("prioritizes DM over MENTION in category, but keeps isMentioned true — a DM with a mention is category DM and isMentioned true", () => {
    const body = `hey ${mentionToken(currentUserId)}`;
    expect(classifySoundEvent(payload({ body_text: body }), "dm", currentUserId)).toEqual({
      category: "DM",
      isMentioned: true,
    });
  });

  it("a DM without a mention is category DM and isMentioned false", () => {
    expect(classifySoundEvent(payload({ body_text: "oi" }), "dm", currentUserId)).toEqual({
      category: "DM",
      isMentioned: false,
    });
  });

  it("finds a mention anywhere in the body, not only at the start", () => {
    const body = `some text before ${mentionToken(currentUserId)} and after`;
    expect(classifySoundEvent(payload({ body_text: body }), "channel", currentUserId)).toEqual({
      category: "MENTION",
      isMentioned: true,
    });
  });

  describe("@all", () => {
    it("counts a channel message with @all as MENTION/isMentioned, even without a personal mention", () => {
      expect(
        classifySoundEvent(
          payload({ body_text: "heads up @all, deploy at 5pm" }),
          "channel",
          currentUserId,
        ),
      ).toEqual({ category: "MENTION", isMentioned: true });
    });

    it("counts @all case-insensitively", () => {
      expect(
        classifySoundEvent(payload({ body_text: "@ALL please read" }), "channel", currentUserId),
      ).toEqual({ category: "MENTION", isMentioned: true });
    });

    it("counts the composer's structured @all token, regardless of the id in it", () => {
      const structuredAllToken = "@[all](mention:all:00000000-0000-0000-0000-000000000000)";
      expect(
        classifySoundEvent(
          payload({ body_text: `heads up ${structuredAllToken}` }),
          "channel",
          currentUserId,
        ),
      ).toEqual({ category: "MENTION", isMentioned: true });
    });

    it("does not count '@all' embedded in an email-like token", () => {
      expect(
        classifySoundEvent(
          payload({ body_text: "contact foo@allowed.com for access" }),
          "channel",
          currentUserId,
        ),
      ).toEqual({ category: "STANDARD", isMentioned: false });
    });

    it("does not count '@allison' as @all", () => {
      expect(
        classifySoundEvent(
          payload({ body_text: "hey @allison, you around?" }),
          "channel",
          currentUserId,
        ),
      ).toEqual({ category: "STANDARD", isMentioned: false });
    });

    it("does not classify @all in a system message as a mention", () => {
      expect(
        classifySoundEvent(
          payload({ kind: "system", body_text: "@all this is automated" }),
          "channel",
          currentUserId,
        ),
      ).toEqual({ category: "NONE", isMentioned: false });
    });

    it("keeps category DM (not MENTION) when @all appears in a DM, but isMentioned is true", () => {
      expect(classifySoundEvent(payload({ body_text: "@all here" }), "dm", currentUserId)).toEqual({
        category: "DM",
        isMentioned: true,
      });
    });
  });
});

describe("shouldPlayMessageSound", () => {
  const base = {
    mode: "all" as const,
    isDuplicate: false,
    isOwnMessage: false,
    category: "STANDARD" as const,
    isMentioned: false,
    isActiveConversation: false,
    isWindowFocused: true,
  };

  it("plays for a background standard message in 'all' mode", () => {
    expect(shouldPlayMessageSound(base)).toBe(true);
  });

  it("never plays in 'off' mode, regardless of category", () => {
    expect(shouldPlayMessageSound({ ...base, mode: "off" })).toBe(false);
    expect(shouldPlayMessageSound({ ...base, mode: "off", category: "DM" })).toBe(false);
    expect(
      shouldPlayMessageSound({ ...base, mode: "off", category: "MENTION", isMentioned: true }),
    ).toBe(false);
  });

  it("does not play for a duplicate event, in any mode", () => {
    expect(shouldPlayMessageSound({ ...base, isDuplicate: true })).toBe(false);
    expect(shouldPlayMessageSound({ ...base, mode: "mentions", isDuplicate: true })).toBe(false);
  });

  it("does not play for the user's own message, in any mode", () => {
    expect(shouldPlayMessageSound({ ...base, isOwnMessage: true })).toBe(false);
    expect(shouldPlayMessageSound({ ...base, mode: "mentions", isOwnMessage: true })).toBe(false);
  });

  it("does not play for category NONE, in any mode", () => {
    expect(shouldPlayMessageSound({ ...base, category: "NONE" })).toBe(false);
    expect(shouldPlayMessageSound({ ...base, mode: "mentions", category: "NONE" })).toBe(false);
  });

  it("does not play for a standard message in the active, focused conversation", () => {
    expect(
      shouldPlayMessageSound({ ...base, isActiveConversation: true, isWindowFocused: true }),
    ).toBe(false);
  });

  it("does not play for a standard message in the active conversation even when unfocused (preserves prior behavior)", () => {
    expect(
      shouldPlayMessageSound({ ...base, isActiveConversation: true, isWindowFocused: false }),
    ).toBe(false);
  });

  it("plays for a DM in the active conversation when the window is unfocused, in 'all' mode", () => {
    expect(
      shouldPlayMessageSound({
        ...base,
        category: "DM",
        isActiveConversation: true,
        isWindowFocused: false,
      }),
    ).toBe(true);
  });

  it("does not play for a DM in the active, focused conversation", () => {
    expect(
      shouldPlayMessageSound({
        ...base,
        category: "DM",
        isActiveConversation: true,
        isWindowFocused: true,
      }),
    ).toBe(false);
  });

  it("plays for a background DM in 'all' mode", () => {
    expect(shouldPlayMessageSound({ ...base, category: "DM", isActiveConversation: false })).toBe(
      true,
    );
  });

  it("plays for a background MENTION in 'all' mode", () => {
    expect(
      shouldPlayMessageSound({
        ...base,
        category: "MENTION",
        isMentioned: true,
        isActiveConversation: false,
      }),
    ).toBe(true);
  });

  describe("'mentions' mode", () => {
    const mentionsMode = { ...base, mode: "mentions" as const };

    it("does not play for a background standard message", () => {
      expect(shouldPlayMessageSound({ ...mentionsMode, category: "STANDARD" })).toBe(false);
    });

    it("does not play for a background DM without a mention", () => {
      expect(shouldPlayMessageSound({ ...mentionsMode, category: "DM", isMentioned: false })).toBe(
        false,
      );
    });

    it("plays for a background DM that also contains a mention", () => {
      expect(shouldPlayMessageSound({ ...mentionsMode, category: "DM", isMentioned: true })).toBe(
        true,
      );
    });

    it("plays for a background channel mention", () => {
      expect(
        shouldPlayMessageSound({ ...mentionsMode, category: "MENTION", isMentioned: true }),
      ).toBe(true);
    });

    it("plays only once for a DM containing a mention (single call site, single decision)", () => {
      // Documents that DM+mention collapses to one decision, not two sounds —
      // the caller only ever calls shouldPlayMessageSound once per event.
      expect(shouldPlayMessageSound({ ...mentionsMode, category: "DM", isMentioned: true })).toBe(
        true,
      );
    });

    it("plays for a mentioned DM in the active conversation once the window loses focus", () => {
      expect(
        shouldPlayMessageSound({
          ...mentionsMode,
          category: "DM",
          isMentioned: true,
          isActiveConversation: true,
          isWindowFocused: false,
        }),
      ).toBe(true);
    });

    it("does not play for a mentioned DM in the active, focused conversation", () => {
      expect(
        shouldPlayMessageSound({
          ...mentionsMode,
          category: "DM",
          isMentioned: true,
          isActiveConversation: true,
          isWindowFocused: true,
        }),
      ).toBe(false);
    });

    it("still gates own message and duplicates even when mentioned", () => {
      expect(
        shouldPlayMessageSound({
          ...mentionsMode,
          category: "MENTION",
          isMentioned: true,
          isOwnMessage: true,
        }),
      ).toBe(false);
      expect(
        shouldPlayMessageSound({
          ...mentionsMode,
          category: "MENTION",
          isMentioned: true,
          isDuplicate: true,
        }),
      ).toBe(false);
    });
  });

  describe("'mentions_and_dms' mode", () => {
    const mentionsAndDmsMode = { ...base, mode: "mentions_and_dms" as const };

    it("does not play for a background standard message", () => {
      expect(shouldPlayMessageSound({ ...mentionsAndDmsMode, category: "STANDARD" })).toBe(false);
    });

    it("plays for a background DM without a mention — the whole point of this mode", () => {
      expect(
        shouldPlayMessageSound({
          ...mentionsAndDmsMode,
          category: "DM",
          isMentioned: false,
        }),
      ).toBe(true);
    });

    it("plays for a background DM that also contains a mention", () => {
      expect(
        shouldPlayMessageSound({ ...mentionsAndDmsMode, category: "DM", isMentioned: true }),
      ).toBe(true);
    });

    it("plays for a background channel mention", () => {
      expect(
        shouldPlayMessageSound({ ...mentionsAndDmsMode, category: "MENTION", isMentioned: true }),
      ).toBe(true);
    });

    it("plays for a DM without a mention in the active conversation once the window loses focus", () => {
      expect(
        shouldPlayMessageSound({
          ...mentionsAndDmsMode,
          category: "DM",
          isMentioned: false,
          isActiveConversation: true,
          isWindowFocused: false,
        }),
      ).toBe(true);
    });

    it("does not play for a DM in the active, focused conversation", () => {
      expect(
        shouldPlayMessageSound({
          ...mentionsAndDmsMode,
          category: "DM",
          isMentioned: false,
          isActiveConversation: true,
          isWindowFocused: true,
        }),
      ).toBe(false);
    });

    it("does not play for a standard channel message in the active conversation even unfocused", () => {
      expect(
        shouldPlayMessageSound({
          ...mentionsAndDmsMode,
          category: "STANDARD",
          isActiveConversation: true,
          isWindowFocused: false,
        }),
      ).toBe(false);
    });

    it("still gates own message and duplicates", () => {
      expect(
        shouldPlayMessageSound({
          ...mentionsAndDmsMode,
          category: "DM",
          isMentioned: false,
          isOwnMessage: true,
        }),
      ).toBe(false);
      expect(
        shouldPlayMessageSound({
          ...mentionsAndDmsMode,
          category: "DM",
          isMentioned: false,
          isDuplicate: true,
        }),
      ).toBe(false);
    });
  });
});
