import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import LeaveConversationDialog from "./LeaveConversationDialog";

/**
 * LeaveConversationDialog — the one destructive confirmation in the row menu
 * (issue #527).
 *
 * Two properties carry the weight here and both are about not lying to the
 * person: the consequence it states must match what leaving that kind of
 * conversation actually costs, and a departure must never appear to have
 * happened when the request refused it.
 */

function renderDialog(overrides: Partial<Parameters<typeof LeaveConversationDialog>[0]> = {}) {
  const onClose = vi.fn();
  const onConfirm = vi.fn().mockResolvedValue(undefined);
  const view = render(
    <LeaveConversationDialog
      kind="channel"
      name="Plataforma"
      onClose={onClose}
      onConfirm={onConfirm}
      {...overrides}
    />,
  );
  return { onClose, onConfirm, ...view };
}

const confirmButton = () => screen.getByRole("button", { name: /^Sair d/ });
const cancelButton = () => screen.getByRole("button", { name: "Cancelar" });

describe("LeaveConversationDialog", () => {
  // ── What leaving actually costs, per kind ──────────────────────────────────

  it("tells a public channel's members they can come back", () => {
    renderDialog();

    expect(screen.getByRole("dialog")).toHaveAccessibleName("Sair de Plataforma?");
    expect(screen.getByText(/Por ser público, você pode entrar novamente/)).toBeInTheDocument();
    expect(confirmButton()).toHaveTextContent("Sair do canal");
  });

  // A private channel is unreachable without a membership, so this is the one
  // case where leaving really does end access to the history — and it must not
  // be claimed for a public channel, where it would be a lie.
  it("warns a private channel's members that they lose the history", () => {
    renderDialog({ isPrivate: true });

    expect(screen.getByText(/perderá acesso ao canal e ao histórico/)).toBeInTheDocument();
    expect(screen.queryByText(/Por ser público/)).not.toBeInTheDocument();
  });

  it("states the group consequence and labels the action for a group", () => {
    renderDialog({ kind: "group", name: "Squad" });

    expect(screen.getByRole("dialog")).toHaveAccessibleName("Sair de Squad?");
    expect(screen.getByText(/deixará de participar do grupo/)).toBeInTheDocument();
    expect(confirmButton()).toHaveTextContent("Sair do grupo");
  });

  // ── Nothing destructive by accident ────────────────────────────────────────

  it("lands focus on the safe action, so a stray Enter cannot make someone leave", () => {
    renderDialog();

    expect(cancelButton()).toHaveFocus();
  });

  it("closes without leaving when Cancel is pressed", async () => {
    const user = userEvent.setup();
    const { onClose, onConfirm } = renderDialog();

    await user.click(cancelButton());

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("closes on Escape and on a click outside, and never on a click inside", async () => {
    const user = userEvent.setup();
    const { onClose, onConfirm } = renderDialog();

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);

    // A press that starts inside the dialog must not reach the backdrop: a
    // selection drag that ends outside would otherwise dismiss it.
    await user.click(screen.getByRole("dialog"));
    expect(onClose).toHaveBeenCalledTimes(1);

    await user.click(document.querySelector(".leave-conversation__backdrop")!);
    expect(onClose).toHaveBeenCalledTimes(2);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  // ── Confirming ────────────────────────────────────────────────────────────

  it("leaves once and closes on success", async () => {
    const user = userEvent.setup();
    const { onClose, onConfirm } = renderDialog({ kind: "group", name: "Squad" });

    await user.click(confirmButton());

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  // Both buttons are disabled while the request is in flight, so the dialog
  // cannot be dismissed out from under a departure that is already happening.
  it("shows the request in flight and refuses to be dismissed while it is", async () => {
    const user = userEvent.setup();
    let release!: () => void;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    const { onClose } = renderDialog({ onConfirm });

    const confirm = confirmButton();
    await user.click(confirm);

    expect(confirm).toHaveTextContent("Saindo…");
    expect(confirm).toHaveAttribute("aria-busy", "true");
    expect(confirm).toBeDisabled();
    expect(cancelButton()).toBeDisabled();

    // Escape and the backdrop are both inert for as long as the request runs.
    await user.keyboard("{Escape}");
    expect(onClose).not.toHaveBeenCalled();

    release();
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  // The disabled state is asynchronous; this ref-guarded path is what actually
  // makes a double-click one departure instead of two.
  it("sends one request for two clicks fired in the same tick", async () => {
    const onConfirm = vi.fn().mockResolvedValue(undefined);
    renderDialog({ onConfirm });

    const button = confirmButton();
    button.click();
    button.click();

    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
  });

  // ── Failure keeps the dialog usable ───────────────────────────────────────

  it.each([
    [new ApiRequestError(403, "forbidden", "no"), "Você não pode sair desta conversa."],
    [new ApiRequestError(404, "not_found", "no"), "Esta conversa não está mais disponível."],
    [
      new ApiRequestError(429, "rate_limited", "no"),
      "Muitas solicitações em sequência. Aguarde um momento e tente novamente.",
    ],
    [new ApiRequestError(0, "network", "no"), "Sem conexão. Verifique sua rede e tente novamente."],
    [new ApiRequestError(500, "internal", "no"), "Não foi possível sair. Tente novamente."],
    [new Error("boom"), "Não foi possível sair. Tente novamente."],
  ])("explains the refusal and stays open: %s", async (failure, expected) => {
    const user = userEvent.setup();
    const onConfirm = vi.fn().mockRejectedValue(failure);
    const { onClose } = renderDialog({ onConfirm });

    await user.click(confirmButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(expected);
    // The departure did not happen, so the dialog must not behave as if it had.
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("keeps the failed action focused and describable, and retries from there", async () => {
    const user = userEvent.setup();
    const onConfirm = vi
      .fn()
      .mockRejectedValueOnce(new ApiRequestError(429, "rate_limited", "no"))
      .mockResolvedValueOnce(undefined);
    const { onClose } = renderDialog({ onConfirm });

    await user.click(confirmButton());
    const alert = await screen.findByRole("alert");

    // Focus stays on the action that failed — it is re-enabled and now names the
    // error, so a screen reader on it hears why. Cancel is usable again too.
    expect(confirmButton()).toHaveFocus();
    expect(confirmButton()).toBeEnabled();
    expect(cancelButton()).toBeEnabled();
    expect(confirmButton()).toHaveAttribute("aria-describedby", alert.id);

    await user.click(confirmButton());

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(onConfirm).toHaveBeenCalledTimes(2);
  });

  // ── Focus stays in the dialog ─────────────────────────────────────────────

  it("wraps Tab and Shift+Tab between the two actions", async () => {
    const user = userEvent.setup();
    renderDialog();

    // Cancel holds focus at mount; Shift+Tab from the first control wraps to the
    // last one rather than escaping to the page behind the dialog.
    await user.tab({ shift: true });
    expect(confirmButton()).toHaveFocus();

    await user.tab();
    expect(cancelButton()).toHaveFocus();
  });

  it("ignores keys it does not handle", async () => {
    const user = userEvent.setup();
    const { onClose, onConfirm } = renderDialog();

    await user.keyboard("a");

    expect(onClose).not.toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
    expect(cancelButton()).toHaveFocus();
  });

  // While the request is in flight every button is disabled, so the trap has
  // nothing to move focus between — it must do nothing rather than throw.
  it("survives Tab while the request is in flight", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn(() => new Promise<void>(() => {}));
    renderDialog({ onConfirm });

    const confirm = confirmButton();
    await user.click(confirm);
    await user.keyboard("{Tab}");

    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  // ── Unmounting mid-flight ─────────────────────────────────────────────────
  //
  // The sidebar can drop this dialog while a departure is in flight — a refetch
  // that removed the conversation does exactly that. Nothing may be applied to a
  // component that is gone, and in particular onClose must not fire for a dialog
  // that no longer exists.
  it("applies nothing after it is unmounted", async () => {
    const user = userEvent.setup();
    let release!: () => void;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    const { onClose, unmount } = renderDialog({ onConfirm });

    await user.click(confirmButton());
    unmount();
    release();
    await Promise.resolve();

    expect(onClose).not.toHaveBeenCalled();
  });

  it("applies nothing after it is unmounted mid-failure", async () => {
    const user = userEvent.setup();
    let reject!: (reason: unknown) => void;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((_resolve, rejectPromise) => {
          reject = rejectPromise;
        }),
    );
    const { onClose, unmount } = renderDialog({ onConfirm });

    await user.click(confirmButton());
    unmount();
    reject(new ApiRequestError(403, "forbidden", "no"));
    await Promise.resolve();

    expect(onClose).not.toHaveBeenCalled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
