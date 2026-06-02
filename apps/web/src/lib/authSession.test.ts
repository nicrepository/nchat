import { beforeEach, describe, expect, it } from "vitest";

import {
  clearTokens,
  getAccessToken,
  getRefreshToken,
  isAuthenticated,
  setTokens,
} from "./authSession";

beforeEach(() => {
  sessionStorage.clear();
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

  it("isAuthenticated returns true when both tokens are set", () => {
    setTokens("acc_abc", "ref_xyz");
    expect(isAuthenticated()).toBe(true);
  });

  it("isAuthenticated returns true when only refresh token present (page reload scenario)", () => {
    sessionStorage.setItem("nchat_rt", "ref_xyz");
    expect(isAuthenticated()).toBe(true);
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
