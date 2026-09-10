import { describe, expect, it } from "vitest";

import { buildMessagePreview, MESSAGE_PREVIEW_MAX_LENGTH } from "./messagePreview";

/**
 * The preview both announcing surfaces share (issue #749). What matters is that
 * a reader never sees the wire grammar: an internal id is not text, and a token
 * is not a name.
 */
describe("buildMessagePreview", () => {
  it("keeps a plain body as it is", () => {
    expect(buildMessagePreview("bom dia")).toBe("bom dia");
  });

  it("shows a mention as its label, never as its token", () => {
    const preview = buildMessagePreview(
      "@[Ana](mention:user:00000000-0000-4000-8000-0000000000f1) pode revisar?",
    );
    expect(preview).toBe("@Ana pode revisar?");
    expect(preview).not.toContain("mention:user:");
  });

  it("elides a body longer than the preview length", () => {
    const preview = buildMessagePreview("a".repeat(MESSAGE_PREVIEW_MAX_LENGTH + 50));
    expect(preview).toHaveLength(MESSAGE_PREVIEW_MAX_LENGTH);
    expect(preview.endsWith("…")).toBe(true);
  });

  it("is stable across calls, so two surfaces never disagree", () => {
    const body =
      "@[Ana](mention:user:00000000-0000-4000-8000-0000000000f1) e @[todos](mention:all:00000000-0000-0000-0000-000000000000)";
    expect(buildMessagePreview(body)).toBe(buildMessagePreview(body));
  });
});
