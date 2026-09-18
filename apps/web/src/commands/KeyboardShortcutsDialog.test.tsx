import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import KeyboardShortcutsDialog from "./KeyboardShortcutsDialog";

describe("KeyboardShortcutsDialog", () => {
  it("opens the shortcut reference and closes it with Escape", () => {
    const opener = document.createElement("button");
    document.body.append(opener);
    opener.focus();
    const onClose = vi.fn();
    const { unmount } = render(<KeyboardShortcutsDialog onClose={onClose} />);

    expect(screen.getByRole("dialog", { name: "Atalhos de teclado" })).toBeInTheDocument();
    expect(screen.getByText("Abrir busca global")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Fechar atalhos" })).toHaveFocus();

    fireEvent.keyDown(screen.getByRole("button", { name: "Fechar atalhos" }), { key: "Escape" });

    expect(onClose).toHaveBeenCalledOnce();
    unmount();
    expect(opener).toHaveFocus();
    opener.remove();
  });
});
