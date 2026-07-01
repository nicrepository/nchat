/**
 * ComposerToolbar tests — RF-11 TipTap toolbar
 *
 * Mock editors verify command routing; real editor tests verify the complete
 * toolbar → TipTap JSON → storage codec → rendered message flow.
 */

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Editor } from "@tiptap/core";
import type { Editor as EditorType } from "@tiptap/core";
import { afterEach, describe, expect, it, vi } from "vitest";
import ComposerToolbar from "./ComposerToolbar";
import RichTextRenderer from "./RichTextRenderer";
import { tiptapDocToMarkdown } from "./tiptapSerializer";
import { createChatEditorExtensions } from "./useChatEditor";

// ── Mock editor factory ───────────────────────────────────────────────────────

type MockChain = {
  focus: ReturnType<typeof vi.fn>;
  toggleBold: ReturnType<typeof vi.fn>;
  toggleItalic: ReturnType<typeof vi.fn>;
  toggleCode: ReturnType<typeof vi.fn>;
  toggleCodeBlock: ReturnType<typeof vi.fn>;
  toggleBulletList: ReturnType<typeof vi.fn>;
  toggleOrderedList: ReturnType<typeof vi.fn>;
  insertContent: ReturnType<typeof vi.fn>;
  run: ReturnType<typeof vi.fn>;
};

function createMockChain(): MockChain {
  const chain: MockChain = {
    focus: vi.fn(),
    toggleBold: vi.fn(),
    toggleItalic: vi.fn(),
    toggleCode: vi.fn(),
    toggleCodeBlock: vi.fn(),
    toggleBulletList: vi.fn(),
    toggleOrderedList: vi.fn(),
    insertContent: vi.fn(),
    run: vi.fn().mockReturnValue(true),
  };
  // Each method returns the chain for chaining.
  for (const key of Object.keys(chain) as (keyof MockChain)[]) {
    if (key !== "run") {
      chain[key].mockReturnValue(chain);
    }
  }
  return chain;
}

function createMockEditor(activeFormat?: string): { editor: EditorType; chain: MockChain } {
  const chain = createMockChain();
  const editor = {
    chain: vi.fn().mockReturnValue(chain),
    isEmpty: false,
    isActive: vi.fn((name: string) => name === activeFormat),
  } as unknown as EditorType;
  return { editor, chain };
}

function renderToolbar(editor: EditorType | null, disabled = false) {
  render(<ComposerToolbar editor={editor} disabled={disabled} />);
}

// ── Direct format buttons ─────────────────────────────────────────────────────

describe("ComposerToolbar — direct format buttons", () => {
  it("renders every format action directly without a format dropdown", () => {
    const { editor } = createMockEditor();
    renderToolbar(editor);

    for (const testId of [
      "fmt-bold",
      "fmt-italic",
      "fmt-code",
      "fmt-codeblock",
      "fmt-ul",
      "fmt-ol",
    ]) {
      expect(screen.getByTestId(testId)).toBeVisible();
    }
    expect(screen.queryByTestId("toolbar-format-btn")).not.toBeInTheDocument();
    expect(screen.queryByTestId("toolbar-format-menu")).not.toBeInTheDocument();
  });

  it("bold button calls toggleBold chain", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("fmt-bold"));

    expect(chain.focus).toHaveBeenCalled();
    expect(chain.toggleBold).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });

  it("italic button calls toggleItalic chain", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("fmt-italic"));

    expect(chain.toggleItalic).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });

  it("ul button calls toggleBulletList chain", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("fmt-ul"));

    expect(chain.toggleBulletList).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });
});

describe("ComposerToolbar — remaining direct format buttons", () => {
  it("code button calls toggleCode chain", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("fmt-code"));

    expect(chain.toggleCode).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });

  it("codeblock button calls toggleCodeBlock chain", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("fmt-codeblock"));

    expect(chain.toggleCodeBlock).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });

  it("ol button calls toggleOrderedList chain", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("fmt-ol"));

    expect(chain.toggleOrderedList).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });
});

// ── Keyboard accessibility ────────────────────────────────────────────────────

describe("ComposerToolbar — keyboard accessibility", () => {
  it("Escape closes emoji picker and returns focus to emoji button", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);
    const btn = screen.getByTestId("toolbar-emoji-btn");

    await user.click(btn);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(btn);
  });

  it("direct bold closes emoji picker if it is open", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.click(screen.getByTestId("fmt-bold"));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("direct code closes the emoji picker and applies formatting", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.click(screen.getByTestId("fmt-code"));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(chain.toggleCode).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });
});

// ── Emoji insertion ───────────────────────────────────────────────────────────

describe("ComposerToolbar — emoji insertion", () => {
  it("inserts emoji via editor.chain().insertContent()", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    await user.click(screen.getByText("😀"));

    expect(chain.insertContent).toHaveBeenCalledWith("😀");
    expect(chain.run).toHaveBeenCalled();
  });

  it("closes emoji picker after inserting", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.click(screen.getByText("😀"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

// ── Disabled state ────────────────────────────────────────────────────────────

describe("ComposerToolbar — disabled state", () => {
  it("disables emoji and every format button when disabled=true", () => {
    const { editor } = createMockEditor();
    renderToolbar(editor, true);

    expect(screen.getByTestId("toolbar-emoji-btn")).toBeDisabled();
    for (const testId of [
      "fmt-bold",
      "fmt-italic",
      "fmt-code",
      "fmt-codeblock",
      "fmt-ul",
      "fmt-ol",
    ]) {
      expect(screen.getByTestId(testId)).toBeDisabled();
    }
  });
});

// ── Null editor defensive paths ───────────────────────────────────────────────

describe("ComposerToolbar — null editor", () => {
  it("does not throw when editor is null and bold button is clicked", async () => {
    const user = userEvent.setup();
    renderToolbar(null);

    // Should not throw — editor?.chain() safely short-circuits
    await expect(user.click(screen.getByTestId("fmt-bold"))).resolves.toBeUndefined();
  });

  it("does not throw when editor is null and emoji is inserted", async () => {
    const user = userEvent.setup();
    renderToolbar(null);

    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    await expect(user.click(screen.getByText("😀"))).resolves.toBeUndefined();
  });

  it("does not throw when editor is null and code is clicked", async () => {
    const user = userEvent.setup();
    renderToolbar(null);

    await expect(user.click(screen.getByTestId("fmt-code"))).resolves.toBeUndefined();
  });
});

// ── Aria attributes ───────────────────────────────────────────────────────────

describe("ComposerToolbar — aria attributes", () => {
  it("direct buttons have accessible aria-labels", () => {
    const { editor } = createMockEditor();
    renderToolbar(editor);
    expect(screen.getByTestId("fmt-bold")).toHaveAttribute("aria-label", "Negrito");
    expect(screen.getByTestId("fmt-italic")).toHaveAttribute("aria-label", "Itálico");
    expect(screen.getByTestId("fmt-code")).toHaveAttribute("aria-label", "Código");
    expect(screen.getByTestId("fmt-codeblock")).toHaveAttribute("aria-label", "Bloco de código");
    expect(screen.getByTestId("fmt-ul")).toHaveAttribute("aria-label", "Lista não ordenada");
    expect(screen.getByTestId("fmt-ol")).toHaveAttribute("aria-label", "Lista ordenada");
  });

  it.each([
    ["fmt-bold", "bold"],
    ["fmt-italic", "italic"],
    ["fmt-code", "code"],
    ["fmt-codeblock", "codeBlock"],
    ["fmt-ul", "bulletList"],
    ["fmt-ol", "orderedList"],
  ])("%s reflects the active %s format", (testId, formatName) => {
    const { editor } = createMockEditor(formatName);
    renderToolbar(editor);

    expect(screen.getByTestId(testId)).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByTestId(testId)).toHaveClass("composer-toolbar__btn--active");
  });
});

// ── Real editor integration ───────────────────────────────────────────────────
//
// These tests use an actual TipTap Editor instance in JSDOM to verify that
// toolbar buttons genuinely apply marks/nodes to the document — not just that
// they call the right chain methods.

describe("ComposerToolbar — real editor integration", () => {
  let realEditor: InstanceType<typeof Editor>;
  let mountDiv: HTMLDivElement;

  afterEach(() => {
    realEditor?.destroy();
    mountDiv?.remove();
  });

  function createRealEditor(content = "<p>hello</p>") {
    mountDiv = document.createElement("div");
    document.body.appendChild(mountDiv);
    realEditor = new Editor({
      element: mountDiv,
      extensions: createChatEditorExtensions(), // production config
      content,
    });
    return realEditor;
  }

  it("bold button applies bold mark to selected text in real editor", async () => {
    const user = userEvent.setup();
    const editor = createRealEditor();
    editor.commands.selectAll();

    render(<ComposerToolbar editor={editor} />);
    await user.click(screen.getByTestId("fmt-bold"));

    expect(editor.isActive("bold")).toBe(true);
  });

  it("reports a formatted selection as pressed", () => {
    const editor = createRealEditor("<p><strong>hello</strong></p>");
    editor.commands.selectAll();

    render(<ComposerToolbar editor={editor} />);

    expect(screen.getByTestId("fmt-bold")).toHaveAttribute("aria-pressed", "true");
  });

  it("italic button applies italic mark to selected text in real editor", async () => {
    const user = userEvent.setup();
    const editor = createRealEditor();
    editor.commands.selectAll();

    render(<ComposerToolbar editor={editor} />);
    await user.click(screen.getByTestId("fmt-italic"));

    expect(editor.isActive("italic")).toBe(true);
    const stored = tiptapDocToMarkdown(editor.getJSON());
    const { container } = render(<RichTextRenderer text={stored} bodyFormat="v2" />);
    expect(stored).toBe("*hello*");
    expect(container.querySelector("em")?.textContent).toBe("hello");
  });

  it("bold and italic buttons survive storage and render both marks", async () => {
    const user = userEvent.setup();
    const editor = createRealEditor();
    editor.commands.selectAll();

    render(<ComposerToolbar editor={editor} />);
    await user.click(screen.getByTestId("fmt-bold"));
    await user.click(screen.getByTestId("fmt-italic"));

    const stored = tiptapDocToMarkdown(editor.getJSON());
    const { container } = render(<RichTextRenderer text={stored} bodyFormat="v2" />);
    expect(stored).toBe("***hello***");
    expect(container.querySelector("strong > em")?.textContent).toBe("hello");
  });
});
