import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import ConfirmDialog from "./ConfirmDialog";

function base(overrides: Partial<Parameters<typeof ConfirmDialog>[0]> = {}) {
  return {
    title: "Desativar esta conta?",
    description: "Ana deixará de conseguir entrar.",
    impact: "Todas as sessões ativas são encerradas.",
    confirmLabel: "Desativar",
    pending: false,
    onConfirm: vi.fn(),
    onCancel: vi.fn(),
    ...overrides,
  };
}

describe("ConfirmDialog", () => {
  it("is a labelled modal dialog naming the action and its impact", () => {
    render(<ConfirmDialog {...base()} />);
    const dialog = screen.getByRole("dialog");

    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(screen.getByRole("heading", { name: "Desativar esta conta?" })).toBeInTheDocument();
    expect(screen.getByText(/Todas as sessões ativas são encerradas/)).toBeInTheDocument();
  });

  it("focuses the confirming action on open", () => {
    render(<ConfirmDialog {...base()} />);
    expect(screen.getByRole("button", { name: "Desativar" })).toHaveFocus();
  });

  // Without this, keyboard and screen-reader users land back at the top of the
  // document after every action.
  it("returns focus to whatever opened it", async () => {
    function Host() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            abrir
          </button>
          {open && <ConfirmDialog {...base({ onCancel: () => setOpen(false) })} />}
        </>
      );
    }
    render(<Host />);
    const opener = screen.getByRole("button", { name: "abrir" });
    await userEvent.click(opener);
    await userEvent.click(screen.getByRole("button", { name: "Cancelar" }));

    expect(opener).toHaveFocus();
  });

  it("cancels on Escape", async () => {
    const onCancel = vi.fn();
    render(<ConfirmDialog {...base({ onCancel })} />);
    await userEvent.keyboard("{Escape}");
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  // A pending mutation must not be cancelled or re-submitted from under itself.
  it("locks both actions while the mutation is running", async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(<ConfirmDialog {...base({ pending: true, onConfirm, onCancel })} />);

    expect(screen.getByRole("button", { name: "Aplicando…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancelar" })).toBeDisabled();
    await userEvent.keyboard("{Escape}");
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("confirms once when clicked", async () => {
    const onConfirm = vi.fn();
    render(<ConfirmDialog {...base({ onConfirm })} />);
    await userEvent.click(screen.getByRole("button", { name: "Desativar" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("omits the impact line when there is nothing extra to warn about", () => {
    render(<ConfirmDialog {...base({ impact: undefined })} />);
    expect(screen.queryByText(/Impacto/)).not.toBeInTheDocument();
  });
});
