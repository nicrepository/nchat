import { describe, expect, it } from "vitest";

import { localParticipantDisplayName } from "./messageDisplay";

describe("localParticipantDisplayName", () => {
  it("appends (você) to a real display name", () => {
    expect(localParticipantDisplayName("Caio Almeida")).toBe("Caio Almeida (você)");
  });

  it("falls back to a bare Você when there is no usable name", () => {
    expect(localParticipantDisplayName("")).toBe("Você");
    expect(localParticipantDisplayName("   ")).toBe("Você");
  });
});
