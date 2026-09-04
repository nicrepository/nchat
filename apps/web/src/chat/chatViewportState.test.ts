import { describe, it, expect } from "vitest";
import {
  isNearBottom,
  isEligibleUnreadMessage,
  countEligibleUnread,
  findFirstUnreadBoundary,
  formatPendingCount,
  goToBottomAccessibleName,
} from "./chatViewportState";

describe("isNearBottom", () => {
  it("is true within the default threshold", () => {
    expect(isNearBottom(1000, 860, 140)).toBe(true); // 1000-860-140=0
  });
  it("is false beyond the default threshold", () => {
    expect(isNearBottom(1000, 500, 140)).toBe(false); // distance 360
  });
  it("honors a custom threshold", () => {
    expect(isNearBottom(1000, 700, 140, 200)).toBe(true); // distance 160 <= 200
  });
});

describe("isEligibleUnreadMessage", () => {
  it("counts an active message from someone else", () => {
    expect(isEligibleUnreadMessage({ status: "active", senderId: "u2" }, "u1")).toBe(true);
  });
  it("excludes the caller's own message", () => {
    expect(isEligibleUnreadMessage({ status: "active", senderId: "u1" }, "u1")).toBe(false);
  });
  it("excludes a deleted message", () => {
    expect(isEligibleUnreadMessage({ status: "deleted", senderId: "u2" }, "u1")).toBe(false);
  });
  it("excludes a pending_link_scan message", () => {
    expect(isEligibleUnreadMessage({ status: "pending_link_scan", senderId: "u2" }, "u1")).toBe(
      false,
    );
  });
  it("counts a system message from someone else (no kind filter, matches backend SQL)", () => {
    expect(isEligibleUnreadMessage({ status: "active", senderId: "u2" }, "u1")).toBe(true);
  });
});

describe("countEligibleUnread", () => {
  it("counts only eligible messages", () => {
    const messages = [
      { status: "active", senderId: "u1" },
      { status: "active", senderId: "u2" },
      { status: "deleted", senderId: "u2" },
      { status: "active", senderId: "u2" },
    ];
    expect(countEligibleUnread(messages, "u1")).toBe(2);
  });
});

describe("findFirstUnreadBoundary", () => {
  const msg = (id: string, senderId: string, status = "active") => ({ id, status, senderId });

  it("returns the earliest of the last N eligible messages", () => {
    const messages = [
      msg("m1", "u1"), // own, ineligible
      msg("m2", "u2"),
      msg("m3", "u2"),
      msg("m4", "u2"),
    ];
    // unreadCount 2 -> last 2 eligible are m3, m4 -> boundary is m3
    expect(findFirstUnreadBoundary(messages, "u1", 2)).toEqual({ messageId: "m3", index: 2 });
  });

  it("skips own and non-active messages when counting backward", () => {
    const messages = [
      msg("m1", "u2"),
      msg("m2", "u1"), // own
      msg("m3", "u2", "deleted"),
      msg("m4", "u2"),
      msg("m5", "u2"),
    ];
    // unreadCount 2 -> eligible are m1, m4, m5 -> last 2 are m4, m5 -> boundary m4
    expect(findFirstUnreadBoundary(messages, "u1", 2)).toEqual({ messageId: "m4", index: 3 });
  });

  it("returns null when unreadCount is 0", () => {
    expect(findFirstUnreadBoundary([msg("m1", "u2")], "u1", 0)).toBeNull();
  });

  it("returns null when fewer eligible messages are loaded than unreadCount", () => {
    expect(findFirstUnreadBoundary([msg("m1", "u2")], "u1", 5)).toBeNull();
  });
});

describe("formatPendingCount", () => {
  it("shows the exact number under 100", () => {
    expect(formatPendingCount(3)).toBe("3");
    expect(formatPendingCount(99)).toBe("99");
  });
  it("caps at 99+", () => {
    expect(formatPendingCount(100)).toBe("99+");
    expect(formatPendingCount(500)).toBe("99+");
  });
});

describe("goToBottomAccessibleName", () => {
  it("names the plain action with no pending messages", () => {
    expect(goToBottomAccessibleName(0)).toBe("Ir para o final da conversa");
  });
  it("names the action with a pending count", () => {
    expect(goToBottomAccessibleName(3)).toBe("Ir para o final da conversa, 3 novas mensagens");
  });
  it("uses the capped display for a very high pending count", () => {
    expect(goToBottomAccessibleName(150)).toBe("Ir para o final da conversa, 99+ novas mensagens");
  });
});
