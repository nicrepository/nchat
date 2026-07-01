/**
 * ComposerToolbar tests — RF-11 TipTap toolbar
 *
 * Uses a mock Editor object to test that toolbar buttons call the correct
 * TipTap chain commands. Real editor not needed — behavior is: "button X
 * calls editor.chain().focus().commandY().run()".
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Editor } from "@tiptap/core";
import type { Editor as EditorType } from "@tiptap/core";
import { afterEach, describe, expect, it, vi } from "vitest";
import ComposerToolbar from "./ComposerToolbar";
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

function createMockEditor(): { editor: EditorType; chain: MockChain } {
  const chain = createMockChain();
  const editor = {
    chain: vi.fn().mockReturnValue(chain),
    isEmpty: false,
    isActive: vi.fn().mockReturnValue(false),
  } as unknown as EditorType;
  return { editor, chain };
}

function renderToolbar(editor: EditorType | null, disabled = false) {
  render(<ComposerToolbar editor={editor} disabled={disabled} />);
}

// ── Direct format buttons ─────────────────────────────────────────────────────

describe("ComposerToolbar — direct format buttons", () => {
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

// ── Dropdown format buttons ───────────────────────────────────────────────────

describe("ComposerToolbar — dropdown format buttons", () => {
  it("code button (via dropdown) calls toggleCode chain", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-format-btn"));
    await user.click(screen.getByTestId("fmt-code"));

    expect(chain.toggleCode).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });

  it("codeblock button (via dropdown) calls toggleCodeBlock chain", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-format-btn"));
    await user.click(screen.getByTestId("fmt-codeblock"));

    expect(chain.toggleCodeBlock).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });

  it("ol button (via dropdown) calls toggleOrderedList chain", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-format-btn"));
    await user.click(screen.getByTestId("fmt-ol"));

    expect(chain.toggleOrderedList).toHaveBeenCalled();
    expect(chain.run).toHaveBeenCalled();
  });

  it("closes format menu after inserting via dropdown", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-format-btn"));
    expect(screen.getByRole("menu")).toBeInTheDocument();

    await user.click(screen.getByTestId("fmt-code"));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("direct bold closes format dropdown if it is open", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-format-btn"));
    expect(screen.getByRole("menu")).toBeInTheDocument();

    await user.click(screen.getByTestId("fmt-bold"));

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});

// ── Keyboard accessibility ────────────────────────────────────────────────────

describe("ComposerToolbar — keyboard accessibility", () => {
  it("Escape closes format menu and returns focus to format button", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);
    const btn = screen.getByTestId("toolbar-format-btn");

    await user.click(btn);
    expect(screen.getByRole("menu")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(btn);
  });

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

  it("handleFormat via dropdown item closes emoji picker AND applies formatting (closeAll path)", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    // Open emoji picker first.
    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    // Open format dropdown while emoji picker is still open.
    await user.click(screen.getByTestId("toolbar-format-btn"));
    expect(screen.getByRole("menu")).toBeInTheDocument();

    // Click a dropdown format item — calls handleFormat → closeAll().
    await user.click(screen.getByTestId("fmt-code"));

    // closeAll() must have closed BOTH panels.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    // Formatting was applied.
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
  it("disables format, emoji and direct-format buttons when disabled=true", () => {
    const { editor } = createMockEditor();
    renderToolbar(editor, true);

    expect(screen.getByTestId("toolbar-format-btn")).toBeDisabled();
    expect(screen.getByTestId("toolbar-emoji-btn")).toBeDisabled();
    expect(screen.getByTestId("fmt-bold")).toBeDisabled();
    expect(screen.getByTestId("fmt-italic")).toBeDisabled();
    expect(screen.getByTestId("fmt-ul")).toBeDisabled();
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

  it("does not throw when editor is null and dropdown item is clicked", async () => {
    const user = userEvent.setup();
    renderToolbar(null);

    await user.click(screen.getByTestId("toolbar-format-btn"));
    await expect(user.click(screen.getByTestId("fmt-code"))).resolves.toBeUndefined();
  });
});

// ── Aria attributes ───────────────────────────────────────────────────────────

describe("ComposerToolbar — aria attributes", () => {
  it("format button has aria-label", () => {
    const { editor } = createMockEditor();
    renderToolbar(editor);
    expect(screen.getByTestId("toolbar-format-btn")).toHaveAttribute(
      "aria-label",
      "Mais formatações",
    );
  });

  it("format button aria-expanded reflects dropdown state", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);
    const btn = screen.getByTestId("toolbar-format-btn");

    expect(btn).toHaveAttribute("aria-expanded", "false");
    await user.click(btn);
    expect(btn).toHaveAttribute("aria-expanded", "true");
  });

  it("direct buttons have accessible aria-labels", () => {
    const { editor } = createMockEditor();
    renderToolbar(editor);
    expect(screen.getByTestId("fmt-bold")).toHaveAttribute("aria-label", "Negrito");
    expect(screen.getByTestId("fmt-italic")).toHaveAttribute("aria-label", "Itálico");
    expect(screen.getByTestId("fmt-ul")).toHaveAttribute("aria-label", "Lista");
  });

  it("first focus moves to first dropdown item after opening", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);

    await user.click(screen.getByTestId("toolbar-format-btn"));

    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByTestId("fmt-code"));
    });
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

  it("italic button applies italic mark to selected text in real editor", async () => {
    const user = userEvent.setup();
    const editor = createRealEditor();
    editor.commands.selectAll();

    render(<ComposerToolbar editor={editor} />);
    await user.click(screen.getByTestId("fmt-italic"));

    expect(editor.isActive("italic")).toBe(true);
  });
});
