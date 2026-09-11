import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import {
  admitRealtimeMessage,
  REALTIME_LEDGER_CAPACITY,
  retainedRealtimeIdCount,
} from "./realtimeMessageLedger";

describe("realtimeMessageLedger", () => {
  beforeEach(() => {
    // A fresh session, through the same mechanism a login uses.
    setTokens("token-for-this-test");
  });

  afterEach(() => {
    vi.useRealTimers();
    clearTokens();
  });

  it("admits a message id the first time", () => {
    expect(admitRealtimeMessage("message-1")).toBe(true);
  });

  it("refuses a redelivery of the same id", () => {
    admitRealtimeMessage("message-1");

    expect(admitRealtimeMessage("message-1")).toBe(false);
  });

  it("keeps separate ids separate", () => {
    admitRealtimeMessage("message-1");

    expect(admitRealtimeMessage("message-2")).toBe(true);
  });

  // The identity is the id and only the id: a redelivery that differs in every
  // other respect is still the same message.
  it("refuses a redelivery however many times it arrives", () => {
    admitRealtimeMessage("message-1");

    for (let index = 0; index < 50; index += 1) {
      expect(admitRealtimeMessage("message-1")).toBe(false);
    }
  });

  /**
   * The defect the review found: a Set that grew for the life of the tab. The
   * ledger is bounded by capacity, so a session that receives far more messages
   * than it can remember still holds a fixed amount of state.
   */
  it("holds a bounded amount of state under traffic far beyond its capacity", () => {
    for (let index = 0; index < 25_000; index += 1) admitRealtimeMessage(`flood-${index}`);

    expect(retainedRealtimeIdCount()).toBeLessThanOrEqual(REALTIME_LEDGER_CAPACITY);
  });

  it("still admits a genuinely new message after that flood", () => {
    for (let index = 0; index < 25_000; index += 1) admitRealtimeMessage(`flood-${index}`);

    expect(admitRealtimeMessage("brand-new")).toBe(true);
  });

  it("keeps refusing recent redeliveries while bounded", () => {
    for (let index = 0; index < 25_000; index += 1) admitRealtimeMessage(`flood-${index}`);

    expect(admitRealtimeMessage("flood-24999")).toBe(false);
  });

  // The retention window is generous against a contract with no replay, but it
  // is a window: an id nobody has mentioned for far longer is not kept.
  it("forgets an id once its retention has elapsed", () => {
    vi.useFakeTimers();
    admitRealtimeMessage("message-1");

    vi.advanceTimersByTime(6 * 60_000);

    expect(admitRealtimeMessage("message-1")).toBe(true);
  });

  /**
   * The ledger is a statement about one reader's session. A different session —
   * a logout, a different account, a replaced token — starts empty, so nothing
   * the previous reader received can suppress anything for the next one.
   */
  it("starts empty for a new session", () => {
    admitRealtimeMessage("message-1");

    setTokens("token-for-the-next-session");

    expect(admitRealtimeMessage("message-1")).toBe(true);
  });

  it("starts empty after a logout", () => {
    admitRealtimeMessage("message-1");

    clearTokens();

    expect(admitRealtimeMessage("message-1")).toBe(true);
  });

  it("does not forget anything while the session is unchanged", () => {
    admitRealtimeMessage("message-1");

    expect(admitRealtimeMessage("message-1")).toBe(false);
    expect(admitRealtimeMessage("message-1")).toBe(false);
  });
});
