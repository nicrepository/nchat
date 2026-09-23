import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { createCommandRegistry } from "./commandRegistry";
import KeyboardShortcutsDialog from "./KeyboardShortcutsDialog";
import { useShortcutManager } from "./useShortcutManager";

describe("useShortcutManager", () => {
  it("navigates from the composer without intercepting native undo", () => {
    const openSearch = vi.fn();
    const previousConversation = vi.fn();
    const nextConversation = vi.fn();
    const historyBack = vi.fn();
    const historyForward = vi.fn();
    const registry = createCommandRegistry({
      openSearch,
      openShortcutHelp: vi.fn(),
      previousConversation,
      nextConversation,
      historyBack,
      historyForward,
    });
    const { container } = render(<ShortcutHarness registry={registry} />);
    const composer = container.querySelector("textarea")!;
    const richComposer = container.querySelector("[contenteditable]")!;

    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    fireEvent.keyDown(composer, { key: "ArrowDown", altKey: true });
    fireEvent.keyDown(richComposer, { key: "ArrowUp", altKey: true });
    fireEvent.keyDown(composer, { key: "ArrowRight", altKey: true });
    fireEvent.keyDown(richComposer, { key: "ArrowLeft", altKey: true });
    const undoEvent = new KeyboardEvent("keydown", {
      key: "z",
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    });
    composer.dispatchEvent(undoEvent);
    const escapeEvent = new KeyboardEvent("keydown", {
      key: "Escape",
      bubbles: true,
      cancelable: true,
    });
    composer.dispatchEvent(escapeEvent);

    expect(openSearch).toHaveBeenCalledOnce();
    expect(nextConversation).toHaveBeenCalledOnce();
    expect(previousConversation).toHaveBeenCalledOnce();
    expect(historyBack).toHaveBeenCalledOnce();
    expect(historyForward).toHaveBeenCalledOnce();
    expect(undoEvent.defaultPrevented).toBe(false);
    expect(escapeEvent.defaultPrevented).toBe(false);
  });

  it("lets the overlay own Escape and restores focus without dispatching a global command", () => {
    const previousConversation = vi.fn();
    const nextConversation = vi.fn();
    const historyBack = vi.fn();
    const historyForward = vi.fn();
    const { getByRole } = render(
      <ShortcutDialogHarness
        previousConversation={previousConversation}
        nextConversation={nextConversation}
        historyBack={historyBack}
        historyForward={historyForward}
      />,
    );
    const composer = getByRole("textbox", { name: "Composer" });
    composer.focus();

    fireEvent.keyDown(composer, { key: "/", ctrlKey: true });
    expect(screen.getByRole("dialog", { name: "Atalhos de teclado" })).toBeInTheDocument();

    fireEvent.keyDown(screen.getByRole("button", { name: "Fechar atalhos" }), { key: "Escape" });

    expect(screen.queryByRole("dialog", { name: "Atalhos de teclado" })).not.toBeInTheDocument();
    expect(composer).toHaveFocus();
    expect(previousConversation).not.toHaveBeenCalled();
    expect(nextConversation).not.toHaveBeenCalled();
    expect(historyBack).not.toHaveBeenCalled();
    expect(historyForward).not.toHaveBeenCalled();
  });
});

function ShortcutHarness({ registry }: { registry: ReturnType<typeof createCommandRegistry> }) {
  useShortcutManager(registry, ["global"]);
  return (
    <>
      <textarea aria-label="Composer" />
      <div
        aria-label="Rich composer"
        contentEditable
        // ProseMirror may consume Alt+Arrow while maintaining its selection.
        // The command listener must still receive the registered shortcut
        // before editor-level bubble handlers run.
        onKeyDown={(event) => event.preventDefault()}
      />
    </>
  );
}

function ShortcutDialogHarness({
  previousConversation,
  nextConversation,
  historyBack,
  historyForward,
}: Readonly<{
  previousConversation: () => void;
  nextConversation: () => void;
  historyBack: () => void;
  historyForward: () => void;
}>) {
  const [open, setOpen] = useState(false);
  const registry = createCommandRegistry({
    openSearch: vi.fn(),
    openShortcutHelp: () => setOpen(true),
    previousConversation,
    nextConversation,
    historyBack,
    historyForward,
  });
  useShortcutManager(registry, ["global"]);

  return (
    <>
      <textarea aria-label="Composer" />
      {open && <KeyboardShortcutsDialog onClose={() => setOpen(false)} />}
    </>
  );
}
