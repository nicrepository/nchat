/**
 * ComposerToolbar tests — RF-11 TipTap toolbar
 *
 * Mock editors verify command routing; real editor tests verify the complete
 * toolbar → TipTap JSON → storage codec → rendered message flow.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Editor } from "@tiptap/core";
import type { Editor as EditorType } from "@tiptap/core";
import { afterEach, describe, expect, it, vi } from "vitest";
import ComposerToolbar from "./ComposerToolbar";
import { emptyEmojiUsage } from "./emoji/emojiUsage";
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

/**
 * Opens the picker and waits for it to be usable.
 *
 * The picker and its catalog arrive through a dynamic import, so the panel is a
 * Suspense fallback for a tick after the click. Waiting on the search field
 * rather than on a timer is what makes these tests deterministic — and waiting
 * on its *focus* specifically, because the picker moves focus there on mount
 * and a test that focuses something else first would lose it a tick later.
 */
async function openPicker(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId("toolbar-emoji-btn"));
  const search = await screen.findByRole("searchbox", { name: "Buscar emoji" });
  await waitFor(() => expect(search).toHaveFocus());
  return search;
}

/** Picks an emoji by the name a screen reader would announce for its cell. */
async function pickEmoji(user: ReturnType<typeof userEvent.setup>, label: string) {
  await user.click(screen.getByRole("button", { name: label }));
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

// ── Emoji insertion (issue #496) ─────────────────────────────────────────────

describe("ComposerToolbar — emoji insertion", () => {
  it("opens the full picker, not a fixed list", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);

    await openPicker(user);

    // The three things the old twenty-emoji panel could not do.
    expect(screen.getByRole("searchbox", { name: "Buscar emoji" })).toBeInTheDocument();
    expect(screen.getByRole("tablist", { name: "Categorias de emoji" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /rosto/i }).length).toBeGreaterThan(20);
  });

  it("inserts the chosen emoji through the editor's own command", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await openPicker(user);
    await pickEmoji(user, "rosto risonho");

    expect(chain.insertContent).toHaveBeenCalledWith("😀");
    expect(chain.run).toHaveBeenCalled();
  });

  // A messenger lets you pick three emoji in a row. Closing after each one
  // would mean three round trips through the button for one "😀😂🎉".
  it("stays open across selections", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await openPicker(user);
    await pickEmoji(user, "rosto risonho");
    await pickEmoji(user, "rosto chorando de rir");

    expect(chain.insertContent).toHaveBeenNthCalledWith(1, "😀");
    expect(chain.insertContent).toHaveBeenNthCalledWith(2, "😂");
    expect(screen.getByTestId("toolbar-emoji-picker")).toBeInTheDocument();
  });

  it("records what was inserted, so it reaches the shared Recentes", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    const onUsed = vi.fn();
    render(
      <ComposerToolbar
        editor={editor}
        emoji={{ usage: emptyEmojiUsage, onToneChange: vi.fn(), onUsed }}
      />,
    );

    await openPicker(user);
    await pickEmoji(user, "rosto risonho");

    expect(onUsed).toHaveBeenCalledWith("😀");
  });

  // The tone palette belongs to the picker, so it works here for free — this
  // test exists to prove the composer did not lose it by portalling the picker
  // somewhere the palette's outside-click test would fire on.
  it("inserts the skin tone the reader picks", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    const onToneChange = vi.fn();
    render(
      <ComposerToolbar
        editor={editor}
        emoji={{ usage: emptyEmojiUsage, onToneChange, onUsed: vi.fn() }}
      />,
    );

    await openPicker(user);
    // Pasted, not typed: one input event, one re-filter of the whole catalog.
    await user.paste("polegar para cima");
    await pickEmoji(user, "polegar para cima");
    await pickEmoji(user, "polegar para cima — Morena escura");

    expect(chain.insertContent).toHaveBeenCalledWith("👍🏾");
    expect(onToneChange).toHaveBeenCalledWith(4);
    expect(screen.getByTestId("toolbar-emoji-picker")).toBeInTheDocument();
  });

  it("closes on Escape and gives the editor its focus back", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    renderToolbar(editor);

    await openPicker(user);
    await user.keyboard("{Escape}");

    expect(screen.queryByTestId("toolbar-emoji-picker")).not.toBeInTheDocument();
    // Issue #493: the caret goes back where the reader left it.
    expect(chain.focus).toHaveBeenCalled();
  });

  it("closes on a click outside, leaving focus where the click put it", async () => {
    const user = userEvent.setup();
    const { editor, chain } = createMockEditor();
    render(
      <div>
        <ComposerToolbar editor={editor} />
        <button type="button">Fora</button>
      </div>,
    );

    await openPicker(user);
    chain.focus.mockClear();
    await user.click(screen.getByRole("button", { name: "Fora" }));

    expect(screen.queryByTestId("toolbar-emoji-picker")).not.toBeInTheDocument();
    expect(chain.focus).not.toHaveBeenCalled();
  });

  it("toggles shut when the button is pressed again", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);

    await openPicker(user);
    await user.click(screen.getByTestId("toolbar-emoji-btn"));

    expect(screen.queryByTestId("toolbar-emoji-picker")).not.toBeInTheDocument();
  });

  it("lets its owner close it — a send, or a change of conversation", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    const onPickerOpenChange = vi.fn();
    const { rerender } = render(
      <ComposerToolbar editor={editor} pickerOpen onPickerOpenChange={onPickerOpenChange} />,
    );
    expect(await screen.findByRole("searchbox", { name: "Buscar emoji" })).toBeInTheDocument();

    rerender(
      <ComposerToolbar
        editor={editor}
        pickerOpen={false}
        onPickerOpenChange={onPickerOpenChange}
      />,
    );

    expect(screen.queryByTestId("toolbar-emoji-picker")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    expect(onPickerOpenChange).toHaveBeenCalledWith(true);
  });

  it("announces the panel it controls", async () => {
    const user = userEvent.setup();
    const { editor } = createMockEditor();
    renderToolbar(editor);
    const button = screen.getByTestId("toolbar-emoji-btn");
    expect(button).toHaveAttribute("aria-expanded", "false");

    await openPicker(user);

    expect(button).toHaveAttribute("aria-expanded", "true");
    const panel = screen.getByTestId("toolbar-emoji-picker");
    expect(button).toHaveAttribute("aria-controls", panel.id);
    // The composer inserts text; nothing here may claim a reaction is happening.
    expect(panel).toHaveAccessibleName("Inserir emoji");
    expect(panel.textContent).not.toMatch(/reação/i);
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

    await openPicker(user);
    await expect(pickEmoji(user, "rosto risonho")).resolves.toBeUndefined();
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
    const stored = tiptapDocToMarkdown(editor.getJSON(), "v2");
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

    const stored = tiptapDocToMarkdown(editor.getJSON(), "v2");
    const { container } = render(<RichTextRenderer text={stored} bodyFormat="v2" />);
    expect(stored).toBe("***hello***");
    expect(container.querySelector("strong > em")?.textContent).toBe("hello");
  });
});
