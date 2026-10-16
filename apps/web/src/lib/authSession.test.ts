import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  _resetListeners,
  clearFreshLogin,
  clearTokens,
  getAccessToken,
  isAuthenticated,
  isFreshLogin,
  markFreshLogin,
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

  it("listener is told only why the session changed, never the token", () => {
    const listener = vi.fn();
    const unsub = onAuthChange(listener);
    setTokens("at");
    setTokens("rotated", "refresh");
    clearTokens();
    expect(listener.mock.calls).toEqual([["session"], ["refresh"], ["session"]]);
    unsub();
  });
});

/** Issue #1123: the entry intention lives exactly as long as the session that set it. */
describe("markFreshLogin", () => {
  afterEach(() => {
    clearFreshLogin();
  });

  it.each(["/", "/chat", "/chat/"])("sign-in into the chat's home (%s) is a fresh login", (to) => {
    setTokens("at-a");
    markFreshLogin(to);
    expect(isFreshLogin()).toBe(true);
  });

  it.each([
    "/call/c-1",
    "/admin/users",
    "/profile",
    "/chat/dm/d-1",
    "/chat/channel/c-1",
    "/chat/search",
  ])("sign-in with an explicit destination (%s) leaves no intention", (to) => {
    setTokens("at-a");
    markFreshLogin(to);
    expect(isFreshLogin()).toBe(false);
  });

  it("logout and a silent recovery of another session do not inherit it", () => {
    setTokens("at-a");
    markFreshLogin("/");
    clearTokens();
    expect(isFreshLogin()).toBe(false);
    setTokens("at-b");
    expect(isFreshLogin()).toBe(false);
  });

  it("a session installed over it does not inherit it", () => {
    setTokens("at-a");
    markFreshLogin("/");
    setTokens("at-b");
    expect(isFreshLogin()).toBe(false);
  });

  it("a token rotation spends it too", () => {
    setTokens("at-a");
    markFreshLogin("/");
    setTokens("at-a2", "refresh");
    expect(isFreshLogin()).toBe(false);
  });

  it("marked before the session is installed, it never takes effect", () => {
    markFreshLogin("/");
    setTokens("at-a");
    expect(isFreshLogin()).toBe(false);
  });

  it("is spent once", () => {
    setTokens("at-a");
    markFreshLogin("/");
    clearFreshLogin();
    expect(isFreshLogin()).toBe(false);
  });
});
