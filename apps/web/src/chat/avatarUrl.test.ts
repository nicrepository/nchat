import { describe, expect, it } from "vitest";

import { safeAvatarUrl } from "./avatarUrl";

describe("safeAvatarUrl", () => {
  it.each([
    undefined,
    null,
    "",
    "   ",
    "javascript:alert(1)",
    "data:image/png;base64,AA==",
    "blob:http://localhost/id",
    "https://evil.test/avatar.png",
    "http://user:password@localhost/avatar.png",
    "/\\evil.test/avatar.png",
    "/media/avatar\n.png",
    "\n/media/avatar.png",
    "/media/avatar.png\t",
    "http://user:password@localhost:3000/avatar.png",
    "http://[not-valid-host",
  ])("rejects an unsafe avatar URL: %s", (value) => {
    expect(safeAvatarUrl(value)).toBeUndefined();
  });

  it.each(["/media/avatar.png", "media/avatar.png", "http://localhost:3000/avatar.png"])(
    "keeps a valid same-origin HTTP(S) URL unchanged: %s",
    (value) => {
      expect(safeAvatarUrl(value)).toBe(value);
    },
  );
});
