/**
 * tiptapSerializer integration tests — uses a live TipTap Editor instance
 * configured IDENTICALLY to production (same createChatEditorExtensions()).
 *
 * These tests verify the full codec contract:
 *   editor input → tiptapDocToMarkdown → RichTextRenderer → visible DOM
 *
 * Diverging from the production extension config was Bug 3: tests passed with
 * false confidence because the editor under test was different from production.
 *
 * For pure TTNode unit tests (no runtime dependency), see
 * tiptapSerializer.unit.test.ts.
 */

import { afterEach, describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Editor } from "@tiptap/core";
import { createChatEditorExtensions } from "./useChatEditor";
import { tiptapDocToMarkdown } from "./tiptapSerializer";
import RichTextRenderer from "./RichTextRenderer";

// ── Editor lifecycle ──────────────────────────────────────────────────────────

let realEditor: Editor | null = null;

/** Creates an Editor with the SAME extensions used in production. */
function createEditor(content = "<p></p>") {
  const div = document.createElement("div");
  document.body.appendChild(div);
  realEditor = new Editor({
    element: div,
    extensions: createChatEditorExtensions(), // production config — not a bare StarterKit
    content,
  });
  return realEditor;
}

afterEach(() => {
  realEditor?.destroy();
  realEditor = null;
});

// ── Full codec round-trip: editor → Markdown → RichTextRenderer ───────────────
//
// These tests exercise the COMPLETE chain that a message takes:
//   1. User types/pastes in the TipTap composer (production config)
//   2. tiptapDocToMarkdown serializes to body_text
//   3. RichTextRenderer renders body_text in the message list
//
// A test that only checks the intermediate Markdown string gives false confidence
// if the renderer can't actually parse what the serializer wrote.

describe("full codec round-trip: editor → Markdown → RichTextRenderer", () => {
  it("bold: editor → **text** → <strong>", () => {
    const editor = createEditor("<p><strong>hello</strong></p>");
    const md = tiptapDocToMarkdown(editor.getJSON());
    expect(md).toBe("**hello**");
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);
    expect(container.querySelector("strong")?.textContent).toBe("hello");
  });

  it("italic: editor → *text* → <em>", () => {
    const editor = createEditor("<p><em>world</em></p>");
    const md = tiptapDocToMarkdown(editor.getJSON());
    expect(md).toBe("*world*");
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);
    expect(container.querySelector("em")?.textContent).toBe("world");
  });

  it("inline code: editor → `code` → <code>", () => {
    const editor = createEditor("<p><code>npm i</code></p>");
    const md = tiptapDocToMarkdown(editor.getJSON());
    expect(md).toBe("`npm i`");
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);
    expect(container.querySelector("code")?.textContent).toBe("npm i");
  });

  it("code block: editor → ```fence``` → <pre>", () => {
    const editor = createEditor("<pre><code>const x = 1;</code></pre>");
    const md = tiptapDocToMarkdown(editor.getJSON());
    expect(md).toBe("```\nconst x = 1;\n```");
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);
    expect(container.querySelector("pre")?.textContent).toContain("const x = 1;");
  });

  it("bullet list: editor → - items → <ul>", () => {
    const editor = createEditor("<ul><li><p>alpha</p></li><li><p>beta</p></li></ul>");
    const md = tiptapDocToMarkdown(editor.getJSON());
    expect(md).toBe("- alpha\n- beta");
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);
    const items = container.querySelectorAll("li");
    expect(items.length).toBe(2);
    expect(items[0].textContent).toBe("alpha");
    expect(items[1].textContent).toBe("beta");
  });

  it("ordered list: editor → 1. items → <ol>", () => {
    const editor = createEditor("<ol><li><p>first</p></li><li><p>second</p></li></ol>");
    const md = tiptapDocToMarkdown(editor.getJSON());
    expect(md).toBe("1. first\n2. second");
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);
    expect(container.querySelector("ol")).not.toBeNull();
    expect(container.querySelectorAll("li").length).toBe(2);
  });

  it("plain text passes through unchanged", () => {
    const editor = createEditor("<p>just text</p>");
    const md = tiptapDocToMarkdown(editor.getJSON());
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);
    expect(container.textContent).toContain("just text");
    expect(container.querySelector("strong, em, code, pre, ul, ol")).toBeNull();
  });

  it("bold+italic survives storage and renders both marks", () => {
    const editor = createEditor("<p><strong><em>hello</em></strong></p>");
    const md = tiptapDocToMarkdown(editor.getJSON());
    expect(md).toBe("***hello***");
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);
    expect(container.querySelector("strong > em")?.textContent).toBe("hello");
  });

  it.each([
    "*nao_italico*",
    "**nao_bold**",
    "`nao_codigo`",
    "```",
    "- nao_lista",
    "1. nao_ordenada",
  ])("literal marker round-trips unchanged: %s", (literal) => {
    const editor = createEditor(`<p>${literal}</p>`);
    const md = tiptapDocToMarkdown(editor.getJSON());
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);

    expect(container.textContent).toBe(literal);
    expect(container.querySelector("strong, em, code, pre, ul, ol")).toBeNull();
  });

  it("nested pasted-list structure and content survive the round-trip", () => {
    const editor = createEditor(
      "<ul><li><p>parent</p><ol><li><p>child</p><ul><li><p>grandchild</p></li></ul></li></ol></li></ul>",
    );
    const md = tiptapDocToMarkdown(editor.getJSON());
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);

    expect(md).toBe("- parent\n  1. child\n    - grandchild");
    expect(container.querySelector("ul > li > ol > li > ul > li")?.textContent).toBe("grandchild");
  });

  it("preserves an ordered list starting at 3 through storage and rendering", () => {
    const editor = createEditor('<ol start="3"><li><p>third</p></li><li><p>fourth</p></li></ol>');
    const md = tiptapDocToMarkdown(editor.getJSON());
    const { container } = render(<RichTextRenderer text={md} bodyFormat="v2" />);

    expect(md).toBe("3. third\n4. fourth");
    expect(container.querySelector("ol")).toHaveAttribute("start", "3");
  });

  it("normalizes pasted code blocks out of list items without losing their text", () => {
    const editor = createEditor("<ul><li><p>item</p><pre><code>a\nb</code></pre></li></ul>");
    const json = editor.getJSON();
    const listItem = json.content?.[0]?.content?.[0];

    expect(listItem?.content?.some((node) => node.type === "codeBlock")).toBe(false);
    expect(editor.getText()).toContain("a\nb");
  });
});

// ── Backward compatibility: legacy messages ───────────────────────────────────
//
// Messages stored BEFORE the TipTap migration used the textarea-based composer
// which also produced this Markdown format. They must continue to render correctly.

describe("backward compatibility: legacy Markdown messages", () => {
  it("renders legacy **bold** message correctly", () => {
    const { container } = render(<RichTextRenderer text="**bold text**" />);
    expect(container.querySelector("strong")?.textContent).toBe("bold text");
  });

  it("renders legacy *italic* message correctly", () => {
    const { container } = render(<RichTextRenderer text="*italic text*" />);
    expect(container.querySelector("em")?.textContent).toBe("italic text");
  });

  it("renders legacy `code` message correctly", () => {
    const { container } = render(<RichTextRenderer text="`some code`" />);
    expect(container.querySelector("code")?.textContent).toBe("some code");
  });

  it("renders legacy bullet list correctly", () => {
    const { container } = render(<RichTextRenderer text={"- item1\n- item2"} />);
    expect(container.querySelector("ul")).not.toBeNull();
    expect(container.querySelectorAll("li").length).toBe(2);
  });

  it("renders legacy ordered list correctly", () => {
    const { container } = render(<RichTextRenderer text={"1. first\n2. second"} />);
    expect(container.querySelector("ol")).not.toBeNull();
  });

  it("renders legacy code block correctly", () => {
    const { container } = render(<RichTextRenderer text={"```\nconsole.log('hi');\n```"} />);
    expect(container.querySelector("pre")).not.toBeNull();
  });
});
