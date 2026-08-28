import { describe, expect, it } from "vitest";

import type { PolicyBounds } from "../api/managementApi";
import {
  rateLimitWarning,
  uploadWarning,
  validateRateLimit,
  validateUploadMiB,
} from "./policyForm";
import { BYTES_PER_MIB } from "./units";

const RATE_BOUNDS: PolicyBounds = { min: 1, max: 600, default: 60, unit: "messages_per_minute" };
const UPLOAD_BOUNDS: PolicyBounds = {
  min: BYTES_PER_MIB,
  max: 512 * BYTES_PER_MIB,
  default: 250 * BYTES_PER_MIB,
  unit: "bytes",
  step: BYTES_PER_MIB,
};

describe("validateRateLimit", () => {
  it("accepts an integer inside the server's bounds", () => {
    expect(validateRateLimit("30", RATE_BOUNDS)).toBeNull();
    expect(validateRateLimit("1", RATE_BOUNDS)).toBeNull();
    expect(validateRateLimit("600", RATE_BOUNDS)).toBeNull();
  });

  // Zero is not a special case and not "disabled": it is below the minimum,
  // which is 1 by design so an anti-spam control never doubles as a mute.
  it("refuses zero, negatives and anything past the ceiling", () => {
    expect(validateRateLimit("0", RATE_BOUNDS)).toContain("entre 1 e 600");
    expect(validateRateLimit("-1", RATE_BOUNDS)).toContain("inteiros");
    expect(validateRateLimit("601", RATE_BOUNDS)).toContain("entre 1 e 600");
  });

  it("refuses input Number() would coerce into something plausible", () => {
    for (const raw of ["1.5", "1e3", " ", "abc", "30px", "+30", "0x1e"]) {
      expect(validateRateLimit(raw, RATE_BOUNDS)).not.toBeNull();
    }
  });

  it("quotes the server's numbers rather than restating limits", () => {
    const narrower: PolicyBounds = { ...RATE_BOUNDS, min: 5, max: 50 };
    expect(validateRateLimit("60", narrower)).toContain("entre 5 e 50");
  });
});

describe("rateLimitWarning", () => {
  it("warns at the ceiling and far above the default", () => {
    expect(rateLimitWarning(600, RATE_BOUNDS)).toContain("teto");
    expect(rateLimitWarning(300, RATE_BOUNDS)).toContain("acima do padrão");
    expect(rateLimitWarning(60, RATE_BOUNDS)).toBeNull();
  });
});

describe("validateUploadMiB", () => {
  it("accepts whole MiB inside the bounds", () => {
    expect(validateUploadMiB("100", UPLOAD_BOUNDS)).toBeNull();
    expect(validateUploadMiB("1", UPLOAD_BOUNDS)).toBeNull();
    expect(validateUploadMiB("512", UPLOAD_BOUNDS)).toBeNull();
  });

  it("refuses values outside the bounds", () => {
    expect(validateUploadMiB("0", UPLOAD_BOUNDS)).toContain("entre 1 e 512");
    expect(validateUploadMiB("513", UPLOAD_BOUNDS)).toContain("entre 1 e 512");
  });

  it("refuses fractions rather than rounding them", () => {
    expect(validateUploadMiB("1.5", UPLOAD_BOUNDS)).toContain("inteiros");
    expect(validateUploadMiB("", UPLOAD_BOUNDS)).toBe("Informe um valor.");
  });

  it("refuses a value it could not convert without losing precision", () => {
    expect(validateUploadMiB(String(Number.MAX_SAFE_INTEGER), UPLOAD_BOUNDS)).toContain(
      "grande demais",
    );
  });
});

describe("uploadWarning", () => {
  it("warns only at the ceiling", () => {
    expect(uploadWarning(UPLOAD_BOUNDS.max, UPLOAD_BOUNDS)).toContain("teto");
    expect(uploadWarning(UPLOAD_BOUNDS.default, UPLOAD_BOUNDS)).toBeNull();
  });
});
