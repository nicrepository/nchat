import { act, fireEvent, render, screen } from "@testing-library/react";
import type { ComponentProps } from "react";
import { describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import GroupIdentityDialog from "./GroupIdentityDialog";

vi.mock("./emoji/EmojiPicker", () => ({
  default: ({ onSelect }: { onSelect: (emoji: string) => void }) => (
    <button type="button" onClick={() => onSelect("🎉")}>
      🎉
    </button>
  ),
}));

function renderDialog(overrides: Partial<ComponentProps<typeof GroupIdentityDialog>> = {}) {
  const props = {
    groupId: "dm-1",
    name: "Equipe Infra",
    avatarEmoji: "👩‍💻" as string | undefined,
    currentUserId: "user-1",
    onClose: vi.fn(),
    onSave: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
  render(<GroupIdentityDialog {...props} />);
  return props;
}

async function settle() {
  await act(async () => {
    await vi.dynamicImportSettled();
  });
}

const save = () => screen.getByRole("button", { name: "Salvar" });

describe("GroupIdentityDialog (issue #1026)", () => {
  it("opens on the group's current identity with nothing to save yet", async () => {
    renderDialog();
    await settle();

    expect(screen.getByRole("dialog", { name: "Identidade do grupo" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Emoji" })).toBeChecked();
    expect(screen.getByRole("img", { name: "Prévia da identidade: emoji 👩‍💻" })).toBeInTheDocument();
    expect(save()).toBeDisabled();
  });

  it("returns to Automático by saving no emoji, then closes", async () => {
    const props = renderDialog();
    await settle();

    fireEvent.click(screen.getByRole("radio", { name: "Automático" }));
    expect(
      screen.getByRole("img", { name: "Prévia da identidade: iniciais EI" }),
    ).toBeInTheDocument();
    expect(props.onSave).not.toHaveBeenCalled();
    fireEvent.click(save());
    await act(async () => Promise.resolve());

    expect(props.onSave).toHaveBeenCalledWith("dm-1", undefined);
    expect(props.onClose).toHaveBeenCalledTimes(1);
  });

  it("sets an emoji on an Automático group, once even on a double click", async () => {
    const props = renderDialog({ avatarEmoji: undefined });
    expect(screen.getByRole("radio", { name: "Automático" })).toHaveFocus();

    fireEvent.click(screen.getByRole("radio", { name: "Emoji" }));
    expect(save()).toBeDisabled();
    await settle();
    fireEvent.click(screen.getByRole("button", { name: "🎉" }));
    await act(async () => {
      save().click();
      save().click();
    });

    expect(props.onSave).toHaveBeenCalledTimes(1);
    expect(props.onSave).toHaveBeenCalledWith("dm-1", "🎉");
  });

  it.each([
    [400, "Escolha um emoji válido."],
    [403, "Você não pode alterar a identidade deste grupo."],
    [404, "Este grupo não está mais disponível."],
    [429, "Muitas alterações em sequência."],
    [0, "Sem conexão."],
    [500, "Não foi possível alterar a identidade."],
  ])("keeps the dialog open with a stable message on %s", async (status, message) => {
    const props = renderDialog({
      onSave: vi.fn().mockRejectedValue(new ApiRequestError(status, "x", "private detail")),
    });
    await settle();

    fireEvent.click(screen.getByRole("radio", { name: "Automático" }));
    fireEvent.click(save());
    await act(async () => Promise.resolve());

    expect(screen.getByRole("alert")).toHaveTextContent(message);
    expect(screen.queryByText(/private detail/)).not.toBeInTheDocument();
    expect(props.onClose).not.toHaveBeenCalled();
    // Changing the draft clears the stale refusal.
    fireEvent.click(screen.getByRole("radio", { name: "Emoji" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("maps an unknown failure to the generic message", async () => {
    renderDialog({ onSave: vi.fn().mockRejectedValue(new Error("boom")) });
    await settle();
    fireEvent.click(screen.getByRole("radio", { name: "Automático" }));
    fireEvent.click(save());
    await act(async () => Promise.resolve());
    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível alterar a identidade.");
  });

  it("closes on Escape, Cancelar and the backdrop, but not while saving", async () => {
    let finish: () => void = () => {};
    const props = renderDialog({
      onSave: vi.fn(() => new Promise<void>((resolve) => (finish = resolve))),
    });
    await settle();

    fireEvent.click(screen.getByRole("radio", { name: "Automático" }));
    fireEvent.click(save());
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    fireEvent.mouseDown(document.querySelector(".rename-channel__backdrop") as Element);
    expect(props.onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Salvando…" })).toHaveAttribute("aria-busy", "true");
    await act(async () => finish());
    expect(props.onClose).toHaveBeenCalledTimes(1);
  });

  it("closes on Escape and Cancelar when idle", async () => {
    const props = renderDialog();
    await settle();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: "Cancelar" }));
    expect(props.onClose).toHaveBeenCalledTimes(2);
  });

  // A radio group is one Tab stop: its checked radio. With Emoji selected the
  // unchecked Automático is not a stop, so Shift+Tab from Emoji must wrap to
  // the last stop instead of leaving the dialog (the reported escape).
  it.each([
    ["Automático", undefined],
    ["Emoji", "🎉"],
  ])("wraps Tab at the edges with %s checked", async (checked, avatarEmoji) => {
    renderDialog({ avatarEmoji });
    await settle();
    const dialog = screen.getByRole("dialog");
    const radio = screen.getByRole("radio", { name: checked });
    // Salvar is disabled (nothing changed), so Cancelar is the last stop.
    const cancel = screen.getByRole("button", { name: "Cancelar" });

    radio.focus();
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(cancel).toHaveFocus();
    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(radio).toHaveFocus();
  });

  it("leaves Tab to the browser away from the edges, and ignores other keys", async () => {
    renderDialog({ avatarEmoji: "🎉" });
    await settle();
    const dialog = screen.getByRole("dialog");
    const emojiCell = screen.getByRole("button", { name: "🎉" });

    emojiCell.focus();
    fireEvent.keyDown(dialog, { key: "Tab" });
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    fireEvent.keyDown(dialog, { key: "a" });
    expect(emojiCell).toHaveFocus();
  });

  // Salvar (disabled) is never the last stop — the tests above wrap from
  // Cancelar — and neither is a control taken out of the Tab order: with
  // Cancelar at tabIndex=-1 the emoji cell becomes the last stop.
  it("never treats a tabIndex=-1 control as a stop", async () => {
    renderDialog({ avatarEmoji: "🎉" });
    await settle();
    const dialog = screen.getByRole("dialog");
    screen.getByRole("button", { name: "Cancelar" }).tabIndex = -1;
    const emojiCell = screen.getByRole("button", { name: "🎉" });

    emojiCell.focus();
    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(screen.getByRole("radio", { name: "Emoji" })).toHaveFocus();
  });
});
