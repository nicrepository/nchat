import { describe, expect, it } from "vitest";

import { buildMessageSnippet, escapeRegExp, splitHighlightSegments } from "./searchHighlight";

describe("escapeRegExp", () => {
  it("escapes every regex special character", () => {
    expect(escapeRegExp("a+b")).toBe("a\\+b");
    expect(escapeRegExp("(abc)")).toBe("\\(abc\\)");
    expect(escapeRegExp(".*")).toBe("\\.\\*");
  });
});

describe("splitHighlightSegments", () => {
  it("returns the whole text unmatched when the query is empty", () => {
    expect(splitHighlightSegments("orion", "")).toEqual([{ value: "orion", matched: false }]);
    expect(splitHighlightSegments("orion", "   ")).toEqual([{ value: "orion", matched: false }]);
  });

  it("matches case-insensitively", () => {
    expect(splitHighlightSegments("Orion Rising", "ORION")).toEqual([
      { value: "Orion", matched: true },
      { value: " Rising", matched: false },
    ]);
  });

  it("finds multiple occurrences", () => {
    expect(splitHighlightSegments("orion and orion", "orion")).toEqual([
      { value: "orion", matched: true },
      { value: " and ", matched: false },
      { value: "orion", matched: true },
    ]);
  });

  it("preserves accented characters", () => {
    expect(splitHighlightSegments("uma ação importante", "ação")).toEqual([
      { value: "uma ", matched: false },
      { value: "ação", matched: true },
      { value: " importante", matched: false },
    ]);
  });

  it("treats regex-special-character queries as literal text", () => {
    expect(splitHighlightSegments("a+b=c", "a+b")).toEqual([
      { value: "a+b", matched: true },
      { value: "=c", matched: false },
    ]);
    expect(splitHighlightSegments("(abc) def", "(abc)")).toEqual([
      { value: "(abc)", matched: true },
      { value: " def", matched: false },
    ]);
    expect(splitHighlightSegments("a.b.c", ".")).toEqual([
      { value: "a", matched: false },
      { value: ".", matched: true },
      { value: "b", matched: false },
      { value: ".", matched: true },
      { value: "c", matched: false },
    ]);
    expect(splitHighlightSegments("a*b*c", "*")).toEqual([
      { value: "a", matched: false },
      { value: "*", matched: true },
      { value: "b", matched: false },
      { value: "*", matched: true },
      { value: "c", matched: false },
    ]);
  });

  it("never alters the original text content when segments are rejoined", () => {
    const text = "The (quick) a+b=c fox.jumps* ação Orion 42";
    const segments = splitHighlightSegments(text, "o");
    expect(segments.map((segment) => segment.value).join("")).toBe(text);
  });

  it("returns no match when the query is not present", () => {
    expect(splitHighlightSegments("hello world", "xyz")).toEqual([
      { value: "hello world", matched: false },
    ]);
  });
});

describe("buildMessageSnippet", () => {
  it("returns the original text untouched when it already fits", () => {
    expect(buildMessageSnippet("short message", "short")).toBe("short message");
  });

  it("truncates around the match with ellipses on both sides", () => {
    const text = "a".repeat(200) + "orion" + "b".repeat(200);
    const snippet = buildMessageSnippet(text, "orion", 20);
    expect(snippet.startsWith("…")).toBe(true);
    expect(snippet.endsWith("…")).toBe(true);
    expect(snippet).toContain("orion");
  });

  it("falls back to a leading truncation when the query has no match", () => {
    const text = "x".repeat(500);
    const snippet = buildMessageSnippet(text, "not-present", 20);
    expect(snippet.endsWith("…")).toBe(true);
    expect(snippet.startsWith("…")).toBe(false);
  });
});
