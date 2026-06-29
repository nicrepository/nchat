/**
 * RichTextRenderer unit tests — RF-11
 *
 * Covers: bold, italic, inline code, code block, unordered list, ordered list,
 * XSS resistance, and plain text pass-through.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import RichTextRenderer from "./RichTextRenderer";

describe("RichTextRenderer", () => {
  it("renders bold text correctly", () => {
    const { container } = render(<RichTextRenderer text="Hello **world**!" />);
    const strong = container.querySelector("strong");
    expect(strong).not.toBeNull();
    expect(strong?.textContent).toBe("world");
    expect(screen.getByText("world")).toBeInTheDocument();
  });

  it("renders italic text correctly", () => {
    const { container } = render(<RichTextRenderer text="Hello *world*!" />);
    const em = container.querySelector("em");
    expect(em).not.toBeNull();
    expect(em?.textContent).toBe("world");
  });

  it("renders inline code correctly", () => {
    const { container } = render(<RichTextRenderer text="Run `npm install` now" />);
    const code = container.querySelector("code");
    expect(code).not.toBeNull();
    expect(code?.textContent).toBe("npm install");
    expect(code?.className).toContain("rtr-inline-code");
  });

  it("renders a fenced code block correctly", () => {
    const text = "```js\nconsole.log('hello');\n```";
    const { container } = render(<RichTextRenderer text={text} />);
    const pre = container.querySelector("pre");
    expect(pre).not.toBeNull();
    expect(pre?.className).toContain("rtr-code-block");
    expect(pre?.textContent).toContain("console.log");
  });

  it("renders unordered list correctly", () => {
    const text = "- apple\n- banana\n- cherry";
    const { container } = render(<RichTextRenderer text={text} />);
    const ul = container.querySelector("ul");
    expect(ul).not.toBeNull();
    expect(ul?.className).toContain("rtr-list");
    const items = ul?.querySelectorAll("li");
    expect(items?.length).toBe(3);
    expect(items?.[0].textContent).toBe("apple");
    expect(items?.[1].textContent).toBe("banana");
    expect(items?.[2].textContent).toBe("cherry");
  });

  it("renders ordered list correctly", () => {
    const text = "1. first\n2. second\n3. third";
    const { container } = render(<RichTextRenderer text={text} />);
    const ol = container.querySelector("ol");
    expect(ol).not.toBeNull();
    expect(ol?.className).toContain("rtr-list");
    const items = ol?.querySelectorAll("li");
    expect(items?.length).toBe(3);
    expect(items?.[0].textContent).toBe("first");
  });

  it("blocks XSS: script input does not execute and no script element is injected", () => {
    const xss = "<script>window.__xss_rf11 = true;</script>";
    const { container } = render(<RichTextRenderer text={xss} />);
    expect(container.querySelector("script")).toBeNull();
    expect((window as unknown as Record<string, unknown>).__xss_rf11).toBeUndefined();
    // The raw string appears as literal text, not as HTML
    expect(document.body.textContent).toContain("<script>");
  });

  it("renders plain text without modification", () => {
    const { container } = render(<RichTextRenderer text="Just a plain message." />);
    expect(screen.getByText("Just a plain message.")).toBeInTheDocument();
    expect(container.querySelector("strong")).toBeNull();
    expect(container.querySelector("em")).toBeNull();
    expect(container.querySelector("code")).toBeNull();
  });

  // ponytail: whitespace preservation is a CSS concern (white-space: pre-wrap on bubble).
  // We test that the renderer emits <br> elements between paragraph lines — the React
  // behaviour that works *with* the CSS — rather than textContent (always true).
  it("inserts <br> between paragraph lines", () => {
    const { container } = render(<RichTextRenderer text={"line1\nline2"} />);
    expect(container.querySelector("br")).not.toBeNull();
    expect(container.textContent).toContain("line1");
    expect(container.textContent).toContain("line2");
  });

  // Branch: `if (!text) return null`
  it("returns null for empty string", () => {
    const { container } = render(<RichTextRenderer text="" />);
    expect(container.firstChild).toBeNull();
  });

  // Branch: `if (i < lines.length) i++` false — unclosed fence has no closing ```.
  it("handles unclosed code fence gracefully", () => {
    const { container } = render(<RichTextRenderer text={"```\ncode without closing"} />);
    const pre = container.querySelector("pre");
    expect(pre).not.toBeNull();
    expect(pre?.textContent).toContain("code without closing");
  });

  // Branch: `if (!chunk) return []` true — split produces empty strings when marker is at
  // the very start/end of text (e.g. "**bold**" → ["", "**bold**", ""]).
  it("renders inline bold with no surrounding text (marker at boundaries)", () => {
    const { container } = render(<RichTextRenderer text="**bold**" />);
    expect(container.querySelector("strong")?.textContent).toBe("bold");
  });

  // Branch: `para.lines.some(l => l.length > 0)` false — blank-only input produces an
  // all-empty paragraph that must be skipped (not pushed to blocks).
  it("renders nothing for blank-only input", () => {
    const { container } = render(<RichTextRenderer text={"\n\n"} />);
    // No block or inline elements — just an empty fragment.
    expect(container.querySelector("strong, em, code, pre, ul, ol")).toBeNull();
  });
});
