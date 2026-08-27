import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

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
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({ displayName: "Ana Costa" })));
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
});
