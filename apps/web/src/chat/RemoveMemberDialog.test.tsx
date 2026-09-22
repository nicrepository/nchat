import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import RemoveMemberDialog from "./RemoveMemberDialog";

/**
 * RemoveMemberDialog — the confirmation between the row's minus button and the
 * DELETE (issue #469).
 *
 * Three properties carry the weight, and all three are about a destructive
 * action the person cannot undo from here: it says exactly who is being
 * removed and from where, it never runs twice for one intention, and a
 * refusal leaves the dialog open and recoverable instead of a panel that looks
 * like the removal happened.
 */

function renderDialog(overrides: Partial<Parameters<typeof RemoveMemberDialog>[0]> = {}) {
  const onClose = vi.fn();
  const onConfirm = vi.fn().mockResolvedValue(undefined);
  const view = render(
    <RemoveMemberDialog
      kind="channel"
      conversationName="Plataforma"
      member={{ userId: "u-2", displayName: "Fernanda Nicácio" }}
      onClose={onClose}
      onConfirm={onConfirm}
      {...overrides}
    />,
  );
  return { onClose, onConfirm, ...view };
}

const confirmButton = () => screen.getByRole("button", { name: "Remover membro" });
const cancelButton = () => screen.getByRole("button", { name: "Cancelar" });

describe("RemoveMemberDialog", () => {
  // ── What it says ───────────────────────────────────────────────────────────

  it("names the person and the conversation", () => {
    renderDialog();

    const dialog = screen.getByRole("dialog", { name: "Remover membro?" });
    expect(dialog).toHaveTextContent("Fernanda Nicácio");
    expect(dialog).toHaveTextContent("Plataforma");
  });

  it("states the private channel's consequence", () => {
    renderDialog({ isPrivateChannel: true });

    expect(screen.getByRole("dialog")).toHaveTextContent(/canal privado/);
  });

  // A public channel stays readable through workspace visibility, so the
  // dialog must not claim the person loses the messages.
  it("does not promise revoked reading in a public channel", () => {
    renderDialog({ isPrivateChannel: false });

    expect(screen.getByRole("dialog")).toHaveTextContent(/continua visível/);
  });

  it("uses the group's own wording", () => {
    renderDialog({ kind: "group", conversationName: "Time de Infra" });

    expect(screen.getByRole("dialog")).toHaveTextContent(/grupo/);
  });

  // ── Accessibility ──────────────────────────────────────────────────────────

  it("is a modal dialog with an accessible name and description", () => {
    renderDialog();

    const dialog = screen.getByRole("dialog", { name: "Remover membro?" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveAccessibleDescription(/Fernanda Nicácio/);
  });

  // The safe action holds focus: a stray Enter must not remove anyone.
  it("opens with focus on Cancelar", () => {
    renderDialog();

    expect(cancelButton()).toHaveFocus();
  });

  // The trap is what the dialog itself does at the two boundaries: forward
  // from the last control and backward from the first. Tabbing between the two
  // middle positions is the browser's job and proves nothing about it.
  it("wraps focus at both ends instead of leaving the dialog", async () => {
    const user = userEvent.setup();
    renderDialog();

    // Backwards from the first control.
    expect(cancelButton()).toHaveFocus();
    await user.tab({ shift: true });
    expect(confirmButton()).toHaveFocus();

    // Forwards from the last one.
    await user.tab();
    expect(cancelButton()).toHaveFocus();
  });

  // ── Cancelling ─────────────────────────────────────────────────────────────

  it("removes nothing when cancelled", async () => {
    const user = userEvent.setup();
    const { onClose, onConfirm } = renderDialog();

    await user.click(cancelButton());

    expect(onConfirm).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("removes nothing on Escape", async () => {
    const user = userEvent.setup();
    const { onClose, onConfirm } = renderDialog();

    await user.keyboard("{Escape}");

    expect(onConfirm).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("removes nothing when the backdrop is pressed", async () => {
    const user = userEvent.setup();
    const { onClose, onConfirm } = renderDialog();

    await user.click(document.querySelector(".remove-member__backdrop") as HTMLElement);

    expect(onConfirm).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // ── Confirming ─────────────────────────────────────────────────────────────

  it("confirms exactly once and leaves closing to the caller", async () => {
    const user = userEvent.setup();
    const { onClose, onConfirm } = renderDialog();

    await user.click(confirmButton());

    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    // The caller owns what happens next: the row is gone and focus has to land
    // somewhere that still exists.
    expect(onClose).not.toHaveBeenCalled();
  });

  // Two clicks in the same tick are one intention. React state alone cannot
  // stop the second, which is what the submitting ref is for.
  it("does not send a second request for a double click", async () => {
    const user = userEvent.setup();
    let release: (() => void) | undefined;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    renderDialog({ onConfirm });

    const confirm = confirmButton();
    await user.click(confirm);
    await user.click(confirm);

    expect(onConfirm).toHaveBeenCalledTimes(1);
    release?.();
  });

  it("disables both actions while the removal is in flight", async () => {
    const user = userEvent.setup();
    let release: (() => void) | undefined;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    const { onClose } = renderDialog({ onConfirm });

    await user.click(confirmButton());

    // The label changes while the write is in flight, which is itself part of
    // the state being announced.
    const pendingConfirm = await screen.findByRole("button", { name: "Removendo…" });
    await waitFor(() => expect(pendingConfirm).toBeDisabled());
    expect(cancelButton()).toBeDisabled();
    expect(pendingConfirm).toHaveAttribute("aria-busy", "true");
    // Escape must not produce an ambiguous state while the write is in flight.
    // Dispatched at the dialog rather than through the keyboard: both buttons
    // are disabled by now, so focus is no longer inside it and a keystroke
    // would never reach the handler being tested.
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    // The backdrop is refused for the same reason.
    fireEvent.mouseDown(document.querySelector(".remove-member__backdrop") as HTMLElement);
    expect(onClose).not.toHaveBeenCalled();
    release?.();
  });

  // ── Failing ────────────────────────────────────────────────────────────────

  it("keeps the dialog open and announces a refusal", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn().mockRejectedValue(new ApiRequestError(403, "forbidden", "forbidden"));
    const { onClose } = renderDialog({ onConfirm });

    await user.click(confirmButton());

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Você não tem permissão para remover esta pessoa.");
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("never shows the server's own message", async () => {
    const user = userEvent.setup();
    const onConfirm = vi
      .fn()
      .mockRejectedValue(
        new ApiRequestError(500, "internal", "relation chat.channel_members does not exist"),
      );
    renderDialog({ onConfirm });

    await user.click(confirmButton());

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Não foi possível remover. Tente novamente.");
    expect(alert).not.toHaveTextContent("chat.channel_members");
  });

  it("can be retried after a failure", async () => {
    const user = userEvent.setup();
    const onConfirm = vi
      .fn()
      .mockRejectedValueOnce(new ApiRequestError(0, "network", "offline"))
      .mockResolvedValueOnce(undefined);
    renderDialog({ onConfirm });

    await user.click(confirmButton());
    await screen.findByRole("alert");
    await user.click(confirmButton());

    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(2));
  });
});
