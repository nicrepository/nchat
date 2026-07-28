import { describe, expect, it } from "vitest";

import { validateLimit } from "./antiSpamForm";

describe("validateLimit", () => {
  it.each([
    ["", "empty"],
    ["  ", "blank"],
    ["abc", "non-numeric"],
    ["1.5", "decimal"],
    ["-1", "negative"],
    ["0", "below minimum"],
    ["601", "above maximum"],
  ])("rejects %s (%s)", (raw) => {
    expect(validateLimit(raw, 1, 600)).not.toBeNull();
  });

  it.each(["1", "60", "600", " 60 "])("accepts %s", (raw) => {
    expect(validateLimit(raw, 1, 600)).toBeNull();
  });
});
