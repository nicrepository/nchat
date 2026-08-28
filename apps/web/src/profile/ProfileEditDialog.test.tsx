import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import ProfileEditDialog from "./ProfileEditDialog";
import * as profileApi from "./profileApi";
import type { SelfProfile } from "./profileApi";

vi.mock("./profileApi", async (importOriginal) => ({
  ...(await importOriginal<typeof profileApi>()),
  updateProfile: vi.fn(),
}));

const profile: SelfProfile = {
  id: "u1",
  displayName: "Ana",
  jobTitle: "Eng",
  bio: "",
  timezone: "",
  customStatus: "",
};

describe("ProfileEditDialog", () => {
  beforeEach(() => {
    vi.mocked(profileApi.updateProfile).mockClear();
  });

  it("disables Salvar alterações until a field changes", async () => {
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    expect(screen.getByRole("button", { name: /salvar alterações/i })).toBeDisabled();
    await userEvent.setup().type(screen.getByLabelText("Nome de exibição"), "!");
    expect(screen.getByRole("button", { name: /salvar alterações/i })).toBeEnabled();
  });

  it("blocks save and shows inline error on an invalid name", async () => {
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    const nameInput = screen.getByLabelText("Nome de exibição");
    await user.clear(nameInput);
    await user.click(screen.getByRole("button", { name: /salvar alterações/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/informe um nome/i);
    expect(profileApi.updateProfile).not.toHaveBeenCalled();
  });

  it("saves once even on a double click, and calls onSaved+onClose with the server response", async () => {
    let resolveUpdate!: (p: SelfProfile) => void;
    vi.mocked(profileApi.updateProfile).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveUpdate = resolve;
      }),
    );
    const onSaved = vi.fn();
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={onClose} onSaved={onSaved} />);
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    const saveButton = screen.getByRole("button", { name: /salvar alterações/i });
    await user.click(saveButton);
    await user.click(saveButton);
    expect(profileApi.updateProfile).toHaveBeenCalledTimes(1);
    resolveUpdate({ ...profile, displayName: "Ana Costa" });
    await waitFor(() =>
      expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({ displayName: "Ana Costa" })),
    );
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("keeps the dialog open and the draft intact on a network error", async () => {
    vi.mocked(profileApi.updateProfile).mockRejectedValueOnce(
      new profileApi.UpdateProfileError("unknown", "Não foi possível atualizar o perfil."),
    );
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={onClose} onSaved={vi.fn()} />);
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    await user.click(screen.getByRole("button", { name: /salvar alterações/i }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(/não foi possível/i));
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Nome de exibição")).toHaveValue("Ana Costa");
  });

  it("updates job title, timezone, custom status and bio from their own fields", async () => {
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    await user.type(screen.getByLabelText("Cargo"), " Sênior");
    fireEvent.change(screen.getByLabelText("Fuso horário"), { target: { value: "UTC" } });
    await user.type(screen.getByLabelText("Status customizado"), "Fora do escritório");
    await user.type(screen.getByLabelText("Biografia"), "Olá!");
    expect(screen.getByLabelText("Cargo")).toHaveValue("Eng Sênior");
    expect(screen.getByLabelText("Fuso horário")).toHaveValue("UTC");
    expect(screen.getByLabelText("Status customizado")).toHaveValue("Fora do escritório");
    expect(screen.getByLabelText("Biografia")).toHaveValue("Olá!");
    expect(screen.getByRole("button", { name: /salvar alterações/i })).toBeEnabled();
  });

  it("blocks save and flags Cargo when it exceeds the max length", async () => {
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Cargo"), { target: { value: "a".repeat(81) } });
    expect(screen.getByLabelText("Cargo")).toHaveAttribute("aria-invalid", "true");
    fireEvent.click(screen.getByRole("button", { name: /salvar alterações/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/cargo deve ter no máximo/i);
    expect(profileApi.updateProfile).not.toHaveBeenCalled();
  });

  it("blocks save and flags Status customizado when it exceeds the max length", async () => {
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Status customizado"), {
      target: { value: "a".repeat(81) },
    });
    expect(screen.getByLabelText("Status customizado")).toHaveAttribute("aria-invalid", "true");
    fireEvent.click(screen.getByRole("button", { name: /salvar alterações/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/status deve ter no máximo/i);
    expect(profileApi.updateProfile).not.toHaveBeenCalled();
  });

  it("blocks save and flags Biografia when it exceeds the max length", async () => {
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    fireEvent.change(screen.getByLabelText("Biografia"), { target: { value: "a".repeat(501) } });
    expect(screen.getByLabelText("Biografia")).toHaveAttribute("aria-invalid", "true");
    fireEvent.click(screen.getByRole("button", { name: /salvar alterações/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/biografia deve ter no máximo/i);
    expect(profileApi.updateProfile).not.toHaveBeenCalled();
  });

  it("wraps focus forward from the last focusable element back to the first on Tab", () => {
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    // The dialog's focus trap re-queries
    // "button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled)"
    // on every Tab press. In jsdom (nwsapi) a grouped selector's matches come
    // back concatenated per sub-selector rather than in document order, so
    // "first"/"last" here are whatever that query actually returns first/last
    // in this test environment (verified via querySelectorAll below), not the
    // visual top-to-bottom field order a real browser would produce.
    const dialog = screen.getByRole("dialog");
    const focusable = dialog.querySelectorAll<HTMLElement>(
      "button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled)",
    );
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    last.focus();
    expect(last).toHaveFocus();
    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(first).toHaveFocus();
  });

  it("wraps focus backward from the first focusable element to the last on Shift+Tab", () => {
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    const dialog = screen.getByRole("dialog");
    const focusable = dialog.querySelectorAll<HTMLElement>(
      "button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled)",
    );
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    first.focus();
    expect(first).toHaveFocus();
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(last).toHaveFocus();
  });

  it("does nothing on Tab when focus is on neither the first nor the last focusable element", () => {
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    const middle = screen.getByLabelText("Nome de exibição");
    middle.focus();
    expect(middle).toHaveFocus();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Tab" });
    // Neither wrap branch matches an interior field, so the trap leaves focus
    // exactly where it was instead of forcing it to the first or last element.
    expect(middle).toHaveFocus();
  });

  it("does nothing on Tab when every field is disabled during a pending save", async () => {
    vi.mocked(profileApi.updateProfile).mockReturnValueOnce(new Promise<SelfProfile>(() => {}));
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    await user.click(screen.getByRole("button", { name: /salvar alterações/i }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Cancelar" })).toBeDisabled());
    expect(() => fireEvent.keyDown(screen.getByRole("dialog"), { key: "Tab" })).not.toThrow();
  });

  it("closes via the Cancelar button", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={onClose} onSaved={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Cancelar" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("ignores Escape while a save is in flight", async () => {
    const onClose = vi.fn();
    vi.mocked(profileApi.updateProfile).mockReturnValueOnce(new Promise<SelfProfile>(() => {}));
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={onClose} onSaved={vi.fn()} />);
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    await user.click(screen.getByRole("button", { name: /salvar alterações/i }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Cancelar" })).toBeDisabled());
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
  });

  it("ignores a second submit fired before the disabled attribute commits", async () => {
    let resolveUpdate!: (p: SelfProfile) => void;
    vi.mocked(profileApi.updateProfile).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveUpdate = resolve;
      }),
    );
    const onSaved = vi.fn();
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={onSaved} />);
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    const saveButton = screen.getByRole("button", { name: /salvar alterações/i });
    // Two submits dispatched inside one `act` batch land before React commits
    // the `disabled` attribute from the first click's setPending(true); only
    // the submittingRef guard in submit() stops the re-entrant second call.
    act(() => {
      fireEvent.click(saveButton);
      fireEvent.click(saveButton);
    });
    resolveUpdate({ ...profile, displayName: "Ana Costa" });
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(profileApi.updateProfile).toHaveBeenCalledTimes(1);
  });

  it("does not throw when the save resolves after unmount", async () => {
    let resolveUpdate!: (p: SelfProfile) => void;
    vi.mocked(profileApi.updateProfile).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveUpdate = resolve;
      }),
    );
    const onSaved = vi.fn();
    const user = userEvent.setup();
    const { unmount } = render(
      <ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={onSaved} />,
    );
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    await user.click(screen.getByRole("button", { name: /salvar alterações/i }));
    unmount();
    expect(() => resolveUpdate({ ...profile, displayName: "Ana Costa" })).not.toThrow();
    await waitFor(() => expect(profileApi.updateProfile).toHaveBeenCalledTimes(1));
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("does not throw when the save rejects after unmount", async () => {
    let rejectUpdate!: (error: Error) => void;
    vi.mocked(profileApi.updateProfile).mockReturnValueOnce(
      new Promise((_resolve, reject) => {
        rejectUpdate = reject;
      }),
    );
    const user = userEvent.setup();
    const { unmount } = render(
      <ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />,
    );
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    await user.click(screen.getByRole("button", { name: /salvar alterações/i }));
    unmount();
    expect(() => rejectUpdate(new Error("boom"))).not.toThrow();
    await waitFor(() => expect(profileApi.updateProfile).toHaveBeenCalledTimes(1));
  });

  it("falls back to a generic error message when the rejection is not an UpdateProfileError", async () => {
    vi.mocked(profileApi.updateProfile).mockRejectedValueOnce(new Error("network down"));
    const user = userEvent.setup();
    render(<ProfileEditDialog profile={profile} onClose={vi.fn()} onSaved={vi.fn()} />);
    await user.type(screen.getByLabelText("Nome de exibição"), " Costa");
    await user.click(screen.getByRole("button", { name: /salvar alterações/i }));
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(/não foi possível salvar o perfil/i),
    );
  });

  it("treats missing optional profile fields as empty strings", () => {
    const bareProfile: SelfProfile = { id: "u2", displayName: "Bea" };
    render(<ProfileEditDialog profile={bareProfile} onClose={vi.fn()} onSaved={vi.fn()} />);
    expect(screen.getByLabelText("Cargo")).toHaveValue("");
    expect(screen.getByLabelText("Fuso horário")).toHaveValue("");
    expect(screen.getByLabelText("Status customizado")).toHaveValue("");
    expect(screen.getByLabelText("Biografia")).toHaveValue("");
    // Still disabled: nothing is dirty relative to the (undefined) source values.
    expect(screen.getByRole("button", { name: /salvar alterações/i })).toBeDisabled();
  });
});
