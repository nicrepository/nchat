import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  _resetListeners,
  clearTokens,
  getAccessToken,
  getRefreshToken,
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
  it("setTokens stores both tokens in sessionStorage", () => {
    setTokens("acc_abc", "ref_xyz");
    expect(getAccessToken()).toBe("acc_abc");
    expect(getRefreshToken()).toBe("ref_xyz");
  });

  it("getAccessToken returns null when not set", () => {
    expect(getAccessToken()).toBeNull();
  });

  it("getRefreshToken returns null when not set", () => {
    expect(getRefreshToken()).toBeNull();
  });

  it("clearTokens removes both tokens", () => {
    setTokens("acc_abc", "ref_xyz");
    clearTokens();
    expect(getAccessToken()).toBeNull();
    expect(getRefreshToken()).toBeNull();
  });

  it("isAuthenticated returns true when access token is set", () => {
    sessionStorage.setItem("nchat_at", "acc_abc");
    expect(isAuthenticated()).toBe(true);
  });

  it("isAuthenticated returns false when only refresh token is set", () => {
    sessionStorage.setItem("nchat_rt", "ref_xyz");
    expect(isAuthenticated()).toBe(false);
  });

  it("isAuthenticated returns false when no tokens are stored", () => {
    expect(isAuthenticated()).toBe(false);
  });

  it("setTokens overwrites existing tokens", () => {
    setTokens("acc_1", "ref_1");
    setTokens("acc_2", "ref_2");
    expect(getAccessToken()).toBe("acc_2");
    expect(getRefreshToken()).toBe("ref_2");
  });
});

describe("onAuthChange", () => {
  it("calls listener after setTokens", () => {
    const listener = vi.fn();
    const unsub = onAuthChange(listener);
    setTokens("at", "rt");
    expect(listener).toHaveBeenCalledTimes(1);
    unsub();
  });

  it("calls listener after clearTokens", () => {
    setTokens("at", "rt");
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
    setTokens("at", "rt");
    expect(listener).not.toHaveBeenCalled();
  });

  it("multiple listeners all receive the notification", () => {
    const a = vi.fn();
    const b = vi.fn();
    const unsubA = onAuthChange(a);
    const unsubB = onAuthChange(b);
    setTokens("at", "rt");
    expect(a).toHaveBeenCalledTimes(1);
    expect(b).toHaveBeenCalledTimes(1);
    unsubA();
    unsubB();
  });

  it("listener is called with no arguments (no token payload)", () => {
    const listener = vi.fn();
    const unsub = onAuthChange(listener);
    setTokens("at", "rt");
    expect(listener).toHaveBeenCalledWith();
    unsub();
  });
});
