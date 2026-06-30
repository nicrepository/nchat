/**
 * ComposerToolbar tests — RF-11 toolbar
 *
 * Uses userEvent for realistic interaction simulation (focus, keyboard, pointer).
 * TestWrapper provides a controlled textarea + setDraft to test insertion logic.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type RefObject, useRef, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import ComposerToolbar from "./ComposerToolbar";

// ── Test wrapper ──────────────────────────────────────────────────────────────

function TestWrapper({ initialDraft = "" }: { initialDraft?: string }) {
  const [draft, setDraft] = useState(initialDraft);
  const ref = useRef<HTMLTextAreaElement>(null) as RefObject<HTMLTextAreaElement | null>;
  return (
    <div>
      <textarea
        ref={ref}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        data-testid="ta"
      />
      <ComposerToolbar textareaRef={ref} setDraft={setDraft} />
    </div>
  );
}

// ── Format insertion (inline markers) ────────────────────────────────────────

describe("ComposerToolbar — inline format insertion", () => {
  it("wraps selection with ** for Negrito", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="texto" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(0, 5);
    await user.click(screen.getByTestId("fmt-bold"));

    expect(ta).toHaveValue("**texto**");
  });

  it("wraps selection with * for Itálico", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="texto" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(0, 5);
    await user.click(screen.getByTestId("fmt-italic"));

    expect(ta).toHaveValue("*texto*");
  });

  it("wraps selection with backtick for Código", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="hello" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(0, 5);
    await user.click(screen.getByTestId("toolbar-format-btn"));
    await user.click(screen.getByTestId("fmt-code"));

    expect(ta).toHaveValue("`hello`");
  });

  it("cursor vazio — coloca cursor entre marcadores ao inserir negrito sem seleção", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="texto" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(2, 2); // cursor at pos 2, no selection
    await user.click(screen.getByTestId("fmt-bold"));

    expect(ta).toHaveValue("te****xto"); // ** + empty + ** inserted at pos 2
    // Cursor must be collapsed between the markers (pos 4), not selecting them.
    await waitFor(() => {
      expect(ta.selectionStart).toBe(4);
      expect(ta.selectionEnd).toBe(4);
    });
  });

  it("cursor posicionado sobre texto envolvido após negrito com seleção", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="texto" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(0, 5);
    await user.click(screen.getByTestId("fmt-bold"));

    // selectionStart/End should be on the wrapped content, not the ** markers.
    await waitFor(() => {
      expect(ta.selectionStart).toBe(2); // after opening **
      expect(ta.selectionEnd).toBe(7); // before closing **
    });
  });

  it("closes format menu after inserting via dropdown", async () => {
    const user = userEvent.setup();
    render(<TestWrapper />);
    await user.click(screen.getByTestId("toolbar-format-btn"));
    expect(screen.getByRole("menu")).toBeInTheDocument();

    await user.click(screen.getByTestId("fmt-code"));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});

// ── Block format insertion ────────────────────────────────────────────────────

describe("ComposerToolbar — block format insertion", () => {
  it("wraps multiline selection in code fence", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft={"line1\nline2\nline3"} />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(0, 17); // select all
    await user.click(screen.getByTestId("toolbar-format-btn"));
    await user.click(screen.getByTestId("fmt-codeblock"));

    expect(ta).toHaveValue("```\nline1\nline2\nline3\n```");
  });

  it("cursor posicionado dentro do bloco de código (após a cerca de abertura)", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="hello" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(0, 5);
    await user.click(screen.getByTestId("toolbar-format-btn"));
    await user.click(screen.getByTestId("fmt-codeblock"));

    expect(ta).toHaveValue("```\nhello\n```");
    // Cursor must be inside the fence (after "```\n"), not selecting the markers.
    await waitFor(() => {
      expect(ta.selectionStart).toBe(4);
      expect(ta.selectionEnd).toBe(4);
    });
  });

  it("expande até limites de linha — inserção no meio da linha (ul)", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="prefix hello suffix" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(7, 12); // select "hello" — mid-line
    await user.click(screen.getByTestId("fmt-ul"));

    // Entire line must be prefixed, not just "hello".
    expect(ta).toHaveValue("- prefix hello suffix");
  });

  it("prefixes each selected line with '- ' for ul", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft={"item1\nitem2\nitem3"} />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(0, 17);
    await user.click(screen.getByTestId("fmt-ul"));

    expect(ta).toHaveValue("- item1\n- item2\n- item3");
  });

  it("prefixes each selected line with numbered prefix for ol", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft={"alpha\nbeta"} />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(0, 10);
    await user.click(screen.getByTestId("toolbar-format-btn"));
    await user.click(screen.getByTestId("fmt-ol"));

    expect(ta).toHaveValue("1. alpha\n2. beta");
  });
});

// ── Keyboard accessibility ────────────────────────────────────────────────────

describe("ComposerToolbar — keyboard accessibility", () => {
  it("Escape closes format menu and returns focus to format button", async () => {
    const user = userEvent.setup();
    render(<TestWrapper />);
    const btn = screen.getByTestId("toolbar-format-btn");

    await user.click(btn);
    expect(screen.getByRole("menu")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(btn);
  });

  it("Escape closes emoji picker and returns focus to emoji button", async () => {
    const user = userEvent.setup();
    render(<TestWrapper />);
    const btn = screen.getByTestId("toolbar-emoji-btn");

    await user.click(btn);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(btn);
  });

  it("seleção obsoleta — reabrir menu usa posição atual do cursor, não posição antiga", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="abcde" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    // Open format dropdown with "abc" selected (tests savedSel for dropdown).
    ta.setSelectionRange(0, 3);
    await user.click(screen.getByTestId("toolbar-format-btn"));

    // Close via Escape (clears savedSel).
    await user.keyboard("{Escape}");

    // Move cursor to "de" and click direct bold button.
    // onPointerDown on fmt-bold snapshots fresh selection {s:3,e:5}.
    ta.setSelectionRange(3, 5);
    await user.click(screen.getByTestId("fmt-bold"));

    // Must have wrapped "de", not "abc".
    expect(ta).toHaveValue("abc**de**");
  });

  it("direct bold closes format dropdown if it is open", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="texto" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    // Open the format dropdown.
    await user.click(screen.getByTestId("toolbar-format-btn"));
    expect(screen.getByRole("menu")).toBeInTheDocument();

    // Click the direct bold button — must close the dropdown AND apply bold.
    ta.setSelectionRange(0, 5);
    await user.click(screen.getByTestId("fmt-bold"));

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(ta).toHaveValue("**texto**");
  });

  it("direct bold closes emoji picker if it is open", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="texto" />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    // Open the emoji picker.
    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    // Click the direct bold button — must close the picker AND apply bold.
    ta.setSelectionRange(0, 5);
    await user.click(screen.getByTestId("fmt-bold"));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(ta).toHaveValue("**texto**");
  });
});

// ── Emoji insertion ───────────────────────────────────────────────────────────

describe("ComposerToolbar — emoji insertion", () => {
  it("inserts emoji at cursor position", async () => {
    const user = userEvent.setup();
    render(<TestWrapper initialDraft="Olá " />);
    const ta = screen.getByTestId("ta") as HTMLTextAreaElement;

    ta.setSelectionRange(4, 4);
    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    await user.click(screen.getByText("😀"));

    expect(ta).toHaveValue("Olá 😀");
  });

  it("closes emoji picker after inserting", async () => {
    const user = userEvent.setup();
    render(<TestWrapper />);
    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.click(screen.getByText("😀"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

// ── Disabled state ────────────────────────────────────────────────────────────

describe("ComposerToolbar — disabled state", () => {
  it("disables format, emoji and direct-format buttons when disabled=true", () => {
    render(
      <div>
        <textarea ref={() => {}} data-testid="ta" />
        <ComposerToolbar
          textareaRef={{ current: null } as unknown as RefObject<HTMLTextAreaElement | null>}
          setDraft={() => {}}
          disabled
        />
      </div>,
    );

    expect(screen.getByTestId("toolbar-format-btn")).toBeDisabled();
    expect(screen.getByTestId("toolbar-emoji-btn")).toBeDisabled();
    expect(screen.getByTestId("fmt-bold")).toBeDisabled();
    expect(screen.getByTestId("fmt-italic")).toBeDisabled();
    expect(screen.getByTestId("fmt-ul")).toBeDisabled();
    // ponytail: link/attach/mic removed — add back when the backing RF lands.
  });
});

// ── Null textareaRef defensive paths ─────────────────────────────────────────

describe("ComposerToolbar — null textareaRef", () => {
  // Covers: `if (ta)` false branch in snapSel, `if (!ta) return` true in insert.
  it("does not throw and does not call setDraft when textareaRef is null on format", async () => {
    const user = userEvent.setup();
    const mockSetDraft = vi.fn();
    render(
      <div>
        <ComposerToolbar
          textareaRef={{ current: null } as RefObject<HTMLTextAreaElement | null>}
          setDraft={mockSetDraft}
          disabled={false}
        />
      </div>,
    );

    // Direct bold button — no dropdown needed.
    await user.click(screen.getByTestId("fmt-bold"));
    // insert() returns early — setDraft never called.
    expect(mockSetDraft).not.toHaveBeenCalled();
  });

  // Covers: `if (!ta) return` true branch in handleEmoji.
  it("does not throw and does not call setDraft when textareaRef is null on emoji", async () => {
    const user = userEvent.setup();
    const mockSetDraft = vi.fn();
    render(
      <div>
        <ComposerToolbar
          textareaRef={{ current: null } as RefObject<HTMLTextAreaElement | null>}
          setDraft={mockSetDraft}
          disabled={false}
        />
      </div>,
    );

    await user.click(screen.getByTestId("toolbar-emoji-btn"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.click(screen.getByText("😀"));
    // handleEmoji returns early — setDraft never called.
    expect(mockSetDraft).not.toHaveBeenCalled();
  });
});
