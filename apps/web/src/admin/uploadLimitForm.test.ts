import { describe, expect, it } from "vitest";

import {
  BYTES_PER_MIB,
  bytesToMiB,
  isEditablePolicy,
  isWholeMiB,
  mibToBytes,
  validateUploadLimitMiB,
} from "./uploadLimitForm";

// Server bounds, as the endpoint echoes them: 1 MiB to 512 MiB.
const MIN = 1 * BYTES_PER_MIB;
const MAX = 512 * BYTES_PER_MIB;

describe("byte and MiB conversion", () => {
  it("uses binary MiB, so 250 MiB is the RF-32 default of 262144000 bytes", () => {
    expect(mibToBytes(250)).toBe(262144000);
    expect(bytesToMiB(262144000)).toBe(250);
  });

  it("round-trips every bound exactly", () => {
    for (const bytes of [MIN, MAX, mibToBytes(250)]) {
      expect(mibToBytes(bytesToMiB(bytes))).toBe(bytes);
    }
  });

  it("recognises which byte counts the policy can hold", () => {
    for (const bytes of [MIN, MAX, mibToBytes(250)]) {
      expect(isWholeMiB(bytes)).toBe(true);
    }
    // 1572864 = 1.5 MiB: the value the review flagged as being rounded to 2.
    for (const bytes of [0, -MIN, 1572864, MIN + 1, MIN - 1, 1.5, Number.NaN]) {
      expect(isWholeMiB(bytes)).toBe(false);
    }
  });
});

describe("isEditablePolicy", () => {
  it("refuses to treat a fractional-MiB policy as editable", () => {
    expect(isEditablePolicy(1572864)).toBe(false);
    expect(isEditablePolicy(mibToBytes(250))).toBe(true);
  });
});

describe("validateUploadLimitMiB", () => {
  it("accepts the bounds themselves", () => {
    expect(validateUploadLimitMiB("1", MIN, MAX)).toBeNull();
    expect(validateUploadLimitMiB("512", MIN, MAX)).toBeNull();
    expect(validateUploadLimitMiB(" 250 ", MIN, MAX)).toBeNull();
  });

  it("rejects an empty value", () => {
    expect(validateUploadLimitMiB("", MIN, MAX)).toBe("Informe um valor.");
    expect(validateUploadLimitMiB("   ", MIN, MAX)).toBe("Informe um valor.");
  });

  it("rejects anything that is not a whole positive number of MiB", () => {
    // Each of these is something Number() would happily coerce.
    for (const raw of ["1.5", "250.5", "-1", "+250", "1e3", "0x10", "250 500", "abc"]) {
      expect(validateUploadLimitMiB(raw, MIN, MAX)).toBe("Use apenas números inteiros de MiB.");
    }
  });

  it("rejects zero and anything outside the server's bounds, quoting them", () => {
    const expected = "O limite deve ser um número inteiro entre 1 e 512 MiB.";
    expect(validateUploadLimitMiB("0", MIN, MAX)).toBe(expected);
    expect(validateUploadLimitMiB("513", MIN, MAX)).toBe(expected);
    expect(validateUploadLimitMiB("99999", MIN, MAX)).toBe(expected);
  });

  it("rejects a value large enough to leave the safe-integer range", () => {
    expect(validateUploadLimitMiB("99999999999999", MIN, MAX)).not.toBeNull();
  });

  it("quotes whatever bounds the server sent, not bounds of its own", () => {
    expect(validateUploadLimitMiB("300", MIN, mibToBytes(100))).toBe(
      "O limite deve ser um número inteiro entre 1 e 100 MiB.",
    );
  });
});
