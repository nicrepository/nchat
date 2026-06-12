import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  _resetListeners,
  clearTokens,
  getAccessToken,
  isAuthenticated,
  onAuthChange,
  setTokens,
} from "./authSession";

beforeEach(() => {
  sessionStorage.clear();
  _resetListeners();
});

afterEach(() => {
  _resetListeners();
});

describe("authSession", () => {
  it("setTokens stores access token in sessionStorage", () => {
    setTokens("acc_abc");
    expect(getAccessToken()).toBe("acc_abc");
  });

  it("setTokens does not persist refresh token to sessionStorage", () => {
    setTokens("acc_abc");
    expect(sessionStorage.getItem("nchat_rt")).toBeNull();
  });

  it("getAccessToken returns null when not set", () => {
    expect(getAccessToken()).toBeNull();
  });

  it("clearTokens removes access token", () => {
    setTokens("acc_abc");
    clearTokens();
    expect(getAccessToken()).toBeNull();
  });

  it("isAuthenticated returns true when access token is set", () => {
    sessionStorage.setItem("nchat_at", "acc_abc");
    expect(isAuthenticated()).toBe(true);
  });

  it("isAuthenticated returns false when no access token is stored", () => {
    expect(isAuthenticated()).toBe(false);
  });

  it("setTokens overwrites existing access token", () => {
    setTokens("acc_1");
    setTokens("acc_2");
    expect(getAccessToken()).toBe("acc_2");
  });
});

describe("onAuthChange", () => {
  it("calls listener after setTokens", () => {
    const listener = vi.fn();
    const unsub = onAuthChange(listener);
    setTokens("at");
    expect(listener).toHaveBeenCalledTimes(1);
    unsub();
  });

  it("calls listener after clearTokens", () => {
    setTokens("at");
    const listener = vi.fn();
    const unsub = onAuthChange(listener);
    clearTokens();
    expect(listener).toHaveBeenCalledTimes(1);
    unsub();
  });

  it("unsubscribe stops future notifications", () => {
    const listener = vi.fn();
    const unsub = onAuthChange(listener);
    unsub();
    setTokens("at");
    expect(listener).not.toHaveBeenCalled();
  });

  it("multiple listeners all receive the notification", () => {
    const a = vi.fn();
    const b = vi.fn();
    const unsubA = onAuthChange(a);
    const unsubB = onAuthChange(b);
    setTokens("at");
    expect(a).toHaveBeenCalledTimes(1);
    expect(b).toHaveBeenCalledTimes(1);
    unsubA();
    unsubB();
  });

  it("listener is called with no arguments (no token payload)", () => {
    const listener = vi.fn();
    const unsub = onAuthChange(listener);
    setTokens("at");
    expect(listener).toHaveBeenCalledWith();
    unsub();
  });
});
