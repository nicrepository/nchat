import { afterEach, describe, expect, it, vi } from "vitest";

import { randomId } from "./randomId";

describe("randomId", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("uses crypto.randomUUID when available", () => {
    const spy = vi
      .spyOn(crypto, "randomUUID")
      .mockReturnValue("11111111-1111-4111-8111-111111111111");
    expect(randomId()).toBe("11111111-1111-4111-8111-111111111111");
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("falls back to crypto.getRandomValues when randomUUID is unavailable (insecure context)", () => {
    vi.stubGlobal("crypto", {
      randomUUID: undefined,
      getRandomValues: crypto.getRandomValues.bind(crypto),
    });

    const id = randomId();
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });

  it("produces distinct ids across calls in the fallback path", () => {
    vi.stubGlobal("crypto", {
      randomUUID: undefined,
      getRandomValues: crypto.getRandomValues.bind(crypto),
    });

    const ids = new Set(Array.from({ length: 50 }, () => randomId()));
    expect(ids.size).toBe(50);
  });
});
