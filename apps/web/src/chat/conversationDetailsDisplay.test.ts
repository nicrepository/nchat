import { describe, expect, it } from "vitest";

import { formatFileSize } from "./channelDetailsDisplay";

describe("formatFileSize", () => {
  it("keeps bytes whole below one kibibyte", () => {
    expect(formatFileSize(0)).toBe("0 B");
    expect(formatFileSize(1)).toBe("1 B");
    expect(formatFileSize(1023)).toBe("1023 B");
  });

  it("rolls over at each unit boundary instead of showing 1024 of the smaller one", () => {
    expect(formatFileSize(1024)).toBe("1,0 KB");
    expect(formatFileSize(1024 * 1024)).toBe("1,0 MB");
    expect(formatFileSize(1024 * 1024 * 1024)).toBe("1,0 GB");
  });

  it("drops the decimal above ten and uses the pt-BR separator", () => {
    expect(formatFileSize(2.4 * 1024 * 1024)).toBe("2,4 MB");
    expect(formatFileSize(128 * 1024 * 1024)).toBe("128 MB");
  });

  it("stops at the largest unit rather than dividing past it", () => {
    expect(formatFileSize(4096 * 1024 ** 4)).toBe("4096 TB");
  });

  it("returns nothing for a value that is not a size", () => {
    expect(formatFileSize(-1)).toBe("");
    expect(formatFileSize(Number.NaN)).toBe("");
    expect(formatFileSize(Number.POSITIVE_INFINITY)).toBe("");
  });
});
