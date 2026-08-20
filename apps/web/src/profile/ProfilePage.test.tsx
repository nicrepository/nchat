import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import ProfilePage from "./ProfilePage";
import { AvatarUploadError, UpdateDisplayNameError, UpdateProfileFieldsError } from "./profileApi";
import { _resetSelfProfile, useSelfProfile } from "./selfProfile";
import { getSoundNotificationMode } from "../chat/soundPreference";

const { mockUpload, mockRemove, mockFetchProfile, mockUpdateDisplayName, mockUpdateProfileFields } =
  vi.hoisted(() => ({
    mockUpload: vi.fn(),
    mockRemove: vi.fn(),
    mockFetchProfile: vi.fn(),
    mockUpdateDisplayName: vi.fn(),
    mockUpdateProfileFields: vi.fn(),
  }));

vi.mock("./profileApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./profileApi")>();
  return {
    ...actual,
    uploadAvatar: (file: File) => mockUpload(file),
    removeAvatar: () => mockRemove(),
    fetchMyProfile: (signal?: AbortSignal) => mockFetchProfile(signal),
    updateDisplayName: (displayName: string, signal?: AbortSignal) =>
      mockUpdateDisplayName(displayName, signal),
    updateProfileFields: (
      fields: { jobTitle: string; bio: string; timezone: string; customStatus: string },
      signal?: AbortSignal,
    ) => mockUpdateProfileFields(fields, signal),
  };
});

// jsdom lacks object URL support; provide deterministic, counted stubs so
// revoke/leak behaviour can be asserted precisely.
let created = 0;
let revoked: string[] = [];
beforeEach(() => {
  created = 0;
  revoked = [];
  vi.clearAllMocks();
  URL.createObjectURL = vi.fn(() => `blob:preview-${++created}`);
  URL.revokeObjectURL = vi.fn((url: string) => {
    revoked.push(url);
  });
  // Default: a user with no persisted avatar.
  mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana", avatarUrl: undefined });
  _resetSelfProfile();
});
afterEach(() => {
  vi.clearAllMocks();
  _resetSelfProfile();
});

function renderPage() {
  return render(
    <MemoryRouter>
      <ProfilePage />
    </MemoryRouter>,
  );
}

function pngFile(name = "me.png", bytes: number[] = [1, 2, 3]) {
  return new File([new Uint8Array(bytes)], name, { type: "image/png" });
}
function gifFile(name = "x.gif") {
  return new File([new Uint8Array([1])], name, { type: "image/gif" });
}

const fileInput = () => screen.getByLabelText(/escolher imagem/i) as HTMLInputElement;
const uploadBtn = () => screen.getByRole("button", { name: /enviar avatar/i });
const removeBtn = () => screen.getByRole("button", { name: /remover avatar/i });

// Scoped to the name card (aria-label "Nome de exibição") so its "Cancelar"
// control never collides with the details card's once both can be dirty.
const nameSection = () => screen.getByRole("region", { name: /^nome de exibição$/i });
const nameInput = () =>
  within(nameSection()).getByRole("textbox", { name: /nome de exibição/i }) as HTMLInputElement;
const saveNameBtn = () => within(nameSection()).getByRole("button", { name: /salvar nome/i });
const cancelNameBtn = () => within(nameSection()).queryByRole("button", { name: /^cancelar$/i });

// Scoped to the details card (aria-label "Detalhes do perfil") so its own
// "Cancelar"/"Salvar alterações" controls never collide with the name card's.
const detailsSection = () => screen.getByRole("region", { name: /detalhes do perfil/i });
const jobTitleInput = () =>
  within(detailsSection()).getByRole("textbox", { name: /cargo/i }) as HTMLInputElement;
const timezoneSelect = () =>
  within(detailsSection()).getByRole("combobox", { name: /fuso horário/i }) as HTMLSelectElement;
const customStatusInput = () =>
  within(detailsSection()).getByRole("textbox", {
    name: /status customizado/i,
  }) as HTMLInputElement;
const bioInput = () =>
  within(detailsSection()).getByRole("textbox", { name: /biografia/i }) as HTMLTextAreaElement;
const saveDetailsBtn = () =>
  within(detailsSection()).getByRole("button", { name: /salvar alterações/i });
const cancelDetailsBtn = () =>
  within(detailsSection()).queryByRole("button", { name: /^cancelar$/i });

// Wait for the initial profile load to settle so the input is enabled.
async function settled() {
  await waitFor(() => expect(mockFetchProfile).toHaveBeenCalled());
  await waitFor(() => expect(fileInput()).not.toBeDisabled());
}

describe("ProfilePage — initial load", () => {
  it("shows the persisted avatar after (re)load and enables removal", async () => {
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/saved.png",
    });
    renderPage();
    await settled();

    const img = await screen.findByAltText(/pré-visualização/i);
    expect(img).toHaveAttribute("src", "/api/auth/avatars/saved.png");
    expect(removeBtn()).toBeEnabled();
    expect(uploadBtn()).toBeDisabled();
  });

  it("shows the placeholder and disables removal when there is no avatar", async () => {
    renderPage();
    await settled();
    expect(screen.queryByAltText(/pré-visualização/i)).not.toBeInTheDocument();
    expect(removeBtn()).toBeDisabled();
  });

  it("does not enable removal or show 'no avatar' while still loading", async () => {
    mockFetchProfile.mockReturnValue(new Promise(() => {}));
    renderPage();
    expect(removeBtn()).toBeDisabled();
    expect(screen.queryByText("?")).not.toBeInTheDocument();
    expect(screen.getByRole("status", { name: /carregando/i })).toBeInTheDocument();
  });

  it("surfaces a load error with retry, not a silent 'no avatar'", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockRejectedValueOnce(new Error("network"));
    renderPage();
    expect(await screen.findByText(/não foi possível carregar o perfil/i)).toBeInTheDocument();
    expect(removeBtn()).toBeDisabled();

    mockFetchProfile.mockResolvedValueOnce({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/saved.png",
    });
    await user.click(screen.getByRole("button", { name: /tentar novamente/i }));
    expect(await screen.findByAltText(/pré-visualização/i)).toHaveAttribute(
      "src",
      "/api/auth/avatars/saved.png",
    );
  });

  it("drops a cross-origin avatar coming from the profile (never rendered)", async () => {
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana", avatarUrl: undefined });
    renderPage();
    await settled();
    expect(screen.queryByAltText(/pré-visualização/i)).not.toBeInTheDocument();
  });
});

// The selection flow uses userEvent.upload, which drives the real <input
// type="file"> semantics: setting input.value clears its FileList. This is what
// catches the regression where the handler cleared the input before reading the
// selected file.
describe("ProfilePage — selection (native input via userEvent.upload)", () => {
  it("Teste 1 — a valid native selection previews and uploads the SAME File", async () => {
    const user = userEvent.setup();
    mockUpload.mockResolvedValue("/api/auth/avatars/deadbeef.png");
    renderPage();
    await settled();

    const file = pngFile("me.png", [9, 9, 9, 9]);
    await user.upload(fileInput(), file);

    // Preview appears and upload becomes enabled — proving the file survived.
    expect(await screen.findByAltText(/pré-visualização/i)).toHaveAttribute(
      "src",
      "blob:preview-1",
    );
    expect(uploadBtn()).toBeEnabled();

    await user.click(uploadBtn());
    await waitFor(() => expect(mockUpload).toHaveBeenCalledTimes(1));
    // Exactly the captured File object is uploaded.
    const sent = mockUpload.mock.calls[0][0] as File;
    expect(sent).toBe(file);
    expect(sent.name).toBe("me.png");
    expect(sent.type).toBe("image/png");
    expect(sent.size).toBe(4);
  });

  it("Teste 2 — valid then invalid discards the previous file and blocks upload", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    const good = pngFile("a.png");
    await user.upload(fileInput(), good);
    expect(await screen.findByAltText(/pré-visualização/i)).toHaveAttribute(
      "src",
      "blob:preview-1",
    );

    // The previously valid file was staged through the REAL input (userEvent),
    // which is what the regression would have lost. The invalid pick uses
    // fireEvent.change because userEvent honours the `accept` filter and cannot
    // model a user choosing "all files" and picking a disallowed type.
    fireEvent.change(fileInput(), { target: { files: [gifFile("b.gif")] } });

    expect(await screen.findByText(/escolha uma imagem jpeg ou png/i)).toBeInTheDocument();
    expect(screen.queryByAltText(/pré-visualização/i)).not.toBeInTheDocument();
    expect(uploadBtn()).toBeDisabled();
    expect(revoked).toContain("blob:preview-1");

    // The previous valid file must not be hidden and uploadable.
    await user.click(uploadBtn());
    expect(mockUpload).not.toHaveBeenCalled();
  });

  it("Teste 3 — valid then valid revokes the first preview and stages only the second", async () => {
    const user = userEvent.setup();
    mockUpload.mockResolvedValue("/api/auth/avatars/second.png");
    renderPage();
    await settled();

    await user.upload(fileInput(), pngFile("a.png"));
    await user.upload(fileInput(), pngFile("b.png"));

    expect(revoked).toContain("blob:preview-1");
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute("src", "blob:preview-2");

    await user.click(uploadBtn());
    await waitFor(() => expect(mockUpload).toHaveBeenCalledTimes(1));
    expect((mockUpload.mock.calls[0][0] as File).name).toBe("b.png");
  });

  it("Teste 4 — the same file can be selected again after cancelling", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    const file = pngFile("same.png");
    await user.upload(fileInput(), file);
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute("src", "blob:preview-1");

    await user.click(screen.getByRole("button", { name: /cancelar nova imagem/i }));
    expect(screen.queryByAltText(/pré-visualização/i)).not.toBeInTheDocument();

    // Re-selecting the very same file must run the handler again (the input was
    // zeroed, so the change fires) and produce a fresh preview.
    await user.upload(fileInput(), file);
    expect(await screen.findByAltText(/pré-visualização/i)).toHaveAttribute(
      "src",
      "blob:preview-2",
    );
    expect(uploadBtn()).toBeEnabled();
  });

  it("Teste 5 — invalid then valid clears the error and allows upload", async () => {
    mockUpload.mockResolvedValue("/api/auth/avatars/ok.png");
    renderPage();
    await settled();

    // This transition is about the error state, not native file capture (which
    // Teste 1/3/4/6 cover via userEvent). Both picks use fireEvent so the invalid
    // accept-bypass and the recovery run through the same, consistent mechanism.
    fireEvent.change(fileInput(), { target: { files: [gifFile("x.gif")] } });
    expect(await screen.findByText(/escolha uma imagem jpeg ou png/i)).toBeInTheDocument();

    fireEvent.change(fileInput(), { target: { files: [pngFile("good.png")] } });
    expect(screen.queryByText(/escolha uma imagem jpeg ou png/i)).not.toBeInTheDocument();
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute("src", "blob:preview-1");
    expect(uploadBtn()).toBeEnabled();
  });

  it("Teste 6 — cleanup: revokes on swap, after success, and on unmount; no leak", async () => {
    const user = userEvent.setup();
    mockUpload.mockResolvedValue("/api/auth/avatars/final.png");
    const { unmount } = renderPage();
    await settled();

    // Swap: first preview revoked when the second is chosen.
    await user.upload(fileInput(), pngFile("a.png"));
    await user.upload(fileInput(), pngFile("b.png"));
    expect(revoked).toEqual(["blob:preview-1"]);

    // Success: the second preview is revoked once the server URL takes over.
    await user.click(uploadBtn());
    await waitFor(() => expect(screen.getByText(/avatar atualizado/i)).toBeInTheDocument());
    expect(revoked).toEqual(["blob:preview-1", "blob:preview-2"]);

    // Unmount with no live preview must not revoke anything more.
    unmount();
    expect(revoked).toEqual(["blob:preview-1", "blob:preview-2"]);
  });

  it("an oversized valid-type file is rejected before upload", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();
    const big = new File([new Uint8Array(6 * 1024 * 1024)], "big.png", { type: "image/png" });
    await user.upload(fileInput(), big);
    expect(await screen.findByText(/muito grande/i)).toBeInTheDocument();
    expect(uploadBtn()).toBeDisabled();
    expect(mockUpload).not.toHaveBeenCalled();
  });

  it("a cancelled selector (no file) stages nothing", async () => {
    renderPage();
    await settled();
    // No native equivalent for "opened then cancelled"; a change with empty
    // files models it and must not resurrect a previous selection.
    fireEvent.change(fileInput(), { target: { files: [] } });
    expect(mockUpload).not.toHaveBeenCalled();
    expect(uploadBtn()).toBeDisabled();
  });

  it("cancel-new-image falls back to the persisted avatar, no server call", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/saved.png",
    });
    renderPage();
    await settled();

    await user.upload(fileInput(), pngFile("a.png"));
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute("src", "blob:preview-1");

    await user.click(screen.getByRole("button", { name: /cancelar nova imagem/i }));
    expect(mockRemove).not.toHaveBeenCalled();
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute(
      "src",
      "/api/auth/avatars/saved.png",
    );
    expect(revoked).toContain("blob:preview-1");
  });
});

describe("ProfilePage — upload", () => {
  it("persists the server URL, revokes the preview and updates the shown avatar", async () => {
    const user = userEvent.setup();
    mockUpload.mockResolvedValue("/api/auth/avatars/deadbeef.png");
    renderPage();
    await settled();
    await user.upload(fileInput(), pngFile());
    await user.click(uploadBtn());

    await waitFor(() => expect(screen.getByText(/avatar atualizado/i)).toBeInTheDocument());
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute(
      "src",
      "/api/auth/avatars/deadbeef.png",
    );
    expect(revoked).toContain("blob:preview-1");
    expect(removeBtn()).toBeEnabled();
  });

  it("keeps the selection and persisted avatar on upload error, allowing retry", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/old.png",
    });
    mockUpload
      .mockRejectedValueOnce(new AvatarUploadError("unknown", "Falhou."))
      .mockResolvedValueOnce("/api/auth/avatars/new.png");
    renderPage();
    await settled();

    await user.upload(fileInput(), pngFile());
    await user.click(uploadBtn());
    expect(await screen.findByText(/falhou/i)).toBeInTheDocument();
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute("src", "blob:preview-1");
    expect(uploadBtn()).toBeEnabled();

    await user.click(uploadBtn());
    await waitFor(() =>
      expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute(
        "src",
        "/api/auth/avatars/new.png",
      ),
    );
  });
});

describe("ProfilePage — removal", () => {
  it("removes a persisted avatar (that survived reload) and shows the placeholder", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/saved.png",
    });
    mockRemove.mockResolvedValue(undefined);
    renderPage();
    await settled();
    expect(screen.getByAltText(/pré-visualização/i)).toBeInTheDocument();

    await user.click(removeBtn());
    await waitFor(() => expect(mockRemove).toHaveBeenCalledTimes(1));
    expect(screen.queryByAltText(/pré-visualização/i)).not.toBeInTheDocument();
    expect(screen.getByText(/avatar removido/i)).toBeInTheDocument();
  });

  it("keeps the avatar visible when DELETE fails", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/saved.png",
    });
    mockRemove.mockRejectedValue(new AvatarUploadError("unknown", "Erro ao remover."));
    renderPage();
    await settled();

    await user.click(removeBtn());
    expect(await screen.findByText(/erro ao remover/i)).toBeInTheDocument();
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute(
      "src",
      "/api/auth/avatars/saved.png",
    );
  });
});

// ── Display name (ID 7 — cronograma 19/08) ─────────────────────────────────────

describe("ProfilePage — display name: initial state and disabled controls", () => {
  it("shows the persisted display name once loaded", async () => {
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana Lima", avatarUrl: undefined });
    renderPage();
    await settled();
    expect(nameInput().value).toBe("Ana Lima");
  });

  it("disables the name input and Save while the profile is still loading", async () => {
    mockFetchProfile.mockReturnValue(new Promise(() => {}));
    renderPage();
    expect(nameInput()).toBeDisabled();
    expect(saveNameBtn()).toBeDisabled();
  });

  it("hides Cancel and disables Save when the draft has not changed", async () => {
    renderPage();
    await settled();
    expect(cancelNameBtn()).not.toBeInTheDocument();
    expect(saveNameBtn()).toBeDisabled();
  });
});

describe("ProfilePage — display name: editing", () => {
  it("typing enables Save and reveals Cancel", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.clear(nameInput());
    await user.type(nameInput(), "Nova Ana");

    expect(saveNameBtn()).toBeEnabled();
    expect(cancelNameBtn()).toBeInTheDocument();
  });

  it("shows a validation error past the limit and blocks Save, without calling the server", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.clear(nameInput());
    await user.type(nameInput(), "a".repeat(81));

    expect(await screen.findByText(/no máximo 80 caracteres/i)).toBeInTheDocument();
    expect(nameInput()).toHaveAttribute("aria-invalid", "true");
    expect(saveNameBtn()).toBeDisabled();

    // A disabled button cannot be clicked via userEvent; fireEvent on the form's
    // submit still must not reach the service, proving onSaveName re-validates
    // rather than trusting the disabled attribute alone.
    fireEvent.submit(nameInput().closest("form")!);
    expect(mockUpdateDisplayName).not.toHaveBeenCalled();
  });

  it("Cancel resets the draft to the persisted value with no server call", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana", avatarUrl: undefined });
    renderPage();
    await settled();

    await user.clear(nameInput());
    await user.type(nameInput(), "Outro Nome");
    await user.click(cancelNameBtn()!);

    expect(nameInput().value).toBe("Ana");
    expect(cancelNameBtn()).not.toBeInTheDocument();
    expect(mockUpdateDisplayName).not.toHaveBeenCalled();
  });
});

describe("ProfilePage — display name: save", () => {
  it("saves the trimmed value and reflects exactly what the server returned", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana", avatarUrl: undefined });
    mockUpdateDisplayName.mockResolvedValue({ id: "u1", displayName: "Ana Lima Souza" });
    renderPage();
    await settled();

    await user.clear(nameInput());
    await user.type(nameInput(), "  Ana Lima Souza  ");
    await user.click(saveNameBtn());

    await waitFor(() => expect(mockUpdateDisplayName).toHaveBeenCalledTimes(1));
    expect(mockUpdateDisplayName.mock.calls[0][0]).toBe("Ana Lima Souza");
    await waitFor(() => expect(screen.getByText(/nome atualizado/i)).toBeInTheDocument());
    expect(nameInput().value).toBe("Ana Lima Souza");
    // The value now matches what was persisted, so there is nothing left to save.
    expect(cancelNameBtn()).not.toBeInTheDocument();
  });

  it("shows aria-busy and disables the input while saving", async () => {
    const user = userEvent.setup();
    let resolveSave!: (value: { id: string; displayName: string }) => void;
    mockUpdateDisplayName.mockReturnValue(new Promise((resolve) => (resolveSave = resolve)));
    renderPage();
    await settled();

    await user.clear(nameInput());
    await user.type(nameInput(), "Ana Lima");
    const btn = saveNameBtn(); // the label changes to "Salvando…" once pending
    await user.click(btn);

    expect(btn).toHaveAttribute("aria-busy", "true");
    expect(nameInput()).toBeDisabled();

    resolveSave({ id: "u1", displayName: "Ana Lima" });
    await waitFor(() => expect(screen.getByText(/nome atualizado/i)).toBeInTheDocument());
  });

  it("a duplicate submit in the same tick reaches the service exactly once", async () => {
    mockUpdateDisplayName.mockResolvedValue({ id: "u1", displayName: "Ana Lima" });
    renderPage();
    await settled();

    fireEvent.change(nameInput(), { target: { value: "Ana Lima" } });
    const form = nameInput().closest("form")!;
    // Two submits fired before either promise settles: the synchronous
    // savingNameRef guard, not just the (async) disabled attribute, is what
    // keeps this to one call.
    fireEvent.submit(form);
    fireEvent.submit(form);

    await waitFor(() => expect(mockUpdateDisplayName).toHaveBeenCalledTimes(1));
  });

  it("keeps the draft and shows a message on failure, allowing retry", async () => {
    const user = userEvent.setup();
    mockUpdateDisplayName
      .mockRejectedValueOnce(new UpdateDisplayNameError("unknown", "Falhou."))
      .mockResolvedValueOnce({ id: "u1", displayName: "Ana Lima" });
    renderPage();
    await settled();

    await user.clear(nameInput());
    await user.type(nameInput(), "Ana Lima");
    await user.click(saveNameBtn());

    expect(await screen.findByText(/falhou/i)).toBeInTheDocument();
    expect(nameInput().value).toBe("Ana Lima"); // draft preserved for retry
    expect(saveNameBtn()).toBeEnabled();

    await user.click(saveNameBtn());
    await waitFor(() => expect(screen.getByText(/nome atualizado/i)).toBeInTheDocument());
  });

  it("a generic (non-typed) failure still shows a message instead of leaving the screen blank", async () => {
    const user = userEvent.setup();
    mockUpdateDisplayName.mockRejectedValue(new Error("boom"));
    renderPage();
    await settled();

    await user.clear(nameInput());
    await user.type(nameInput(), "Ana Lima");
    await user.click(saveNameBtn());

    expect(await screen.findByText(/não foi possível atualizar o nome/i)).toBeInTheDocument();
  });
});

describe("ProfilePage — profile fields: initial state and disabled controls", () => {
  it("shows the persisted values once loaded", async () => {
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      jobTitle: "Engenheira",
      bio: "Focada em backend.",
      timezone: "America/Sao_Paulo",
      customStatus: "Em reunião",
    });
    renderPage();
    await settled();
    expect(jobTitleInput().value).toBe("Engenheira");
    expect(bioInput().value).toBe("Focada em backend.");
    expect(timezoneSelect().value).toBe("America/Sao_Paulo");
    expect(customStatusInput().value).toBe("Em reunião");
  });

  it("disables the fields and Save while the profile is still loading", async () => {
    mockFetchProfile.mockReturnValue(new Promise(() => {}));
    renderPage();
    expect(jobTitleInput()).toBeDisabled();
    expect(saveDetailsBtn()).toBeDisabled();
  });

  it("hides Cancel and disables Save when no draft has changed", async () => {
    renderPage();
    await settled();
    expect(cancelDetailsBtn()).not.toBeInTheDocument();
    expect(saveDetailsBtn()).toBeDisabled();
  });

  it("gives each new profile control a stable name and valid autocomplete behavior", async () => {
    renderPage();
    await settled();

    expect(jobTitleInput()).toHaveAttribute("name", "job_title");
    expect(jobTitleInput()).toHaveAttribute("autocomplete", "organization-title");
    expect(timezoneSelect()).toHaveAttribute("name", "timezone");
    expect(timezoneSelect()).toHaveAttribute("autocomplete", "off");
    expect(customStatusInput()).toHaveAttribute("name", "custom_status");
    expect(customStatusInput()).toHaveAttribute("autocomplete", "off");
    expect(bioInput()).toHaveAttribute("name", "bio");
    expect(bioInput()).toHaveAttribute("autocomplete", "off");
  });
});

describe("ProfilePage — profile fields: editing", () => {
  it("typing in any field enables Save and reveals Cancel, independently of the name card", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.type(jobTitleInput(), "Engenheira");

    expect(saveDetailsBtn()).toBeEnabled();
    expect(cancelDetailsBtn()).toBeInTheDocument();
    // The name card's own Cancel must not appear from an unrelated edit.
    expect(cancelNameBtn()).not.toBeInTheDocument();
  });

  it("selecting a timezone enables Save", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.selectOptions(timezoneSelect(), "America/Sao_Paulo");

    expect(timezoneSelect().value).toBe("America/Sao_Paulo");
    expect(saveDetailsBtn()).toBeEnabled();
  });

  it("shows a validation error past the limit for a short field and blocks Save", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.type(jobTitleInput(), "a".repeat(81));

    expect(await screen.findByText(/cargo deve ter no máximo 80 caracteres/i)).toBeInTheDocument();
    expect(jobTitleInput()).toHaveAttribute("aria-invalid", "true");
    expect(saveDetailsBtn()).toBeDisabled();

    fireEvent.submit(jobTitleInput().closest("form")!);
    expect(mockUpdateProfileFields).not.toHaveBeenCalled();
    expect(jobTitleInput()).toHaveFocus();
  });

  it("shows a validation error past the limit for bio and blocks Save", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.click(bioInput());
    await user.paste("a".repeat(501));

    expect(
      await screen.findByText(/biografia deve ter no máximo 500 caracteres/i),
    ).toBeInTheDocument();
    expect(saveDetailsBtn()).toBeDisabled();
  });

  it("shows a validation error past the limit for custom status and blocks Save", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.type(customStatusInput(), "a".repeat(81));

    expect(await screen.findByText(/status deve ter no máximo 80 caracteres/i)).toBeInTheDocument();
    expect(saveDetailsBtn()).toBeDisabled();
  });

  it("focuses the first invalid field in the visual form order on submit", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.type(customStatusInput(), "a".repeat(81));
    await user.click(bioInput());
    await user.paste("a".repeat(501));

    fireEvent.submit(customStatusInput().closest("form")!);

    expect(customStatusInput()).toHaveFocus();
    expect(mockUpdateProfileFields).not.toHaveBeenCalled();
  });

  it("Cancel resets every draft to the persisted values with no server call", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      jobTitle: "Engenheira",
      bio: "",
      timezone: "",
      customStatus: "",
    });
    renderPage();
    await settled();

    await user.clear(jobTitleInput());
    await user.type(jobTitleInput(), "Outro cargo");
    await user.click(cancelDetailsBtn()!);

    expect(jobTitleInput().value).toBe("Engenheira");
    expect(cancelDetailsBtn()).not.toBeInTheDocument();
    expect(mockUpdateProfileFields).not.toHaveBeenCalled();
  });
});

describe("ProfilePage — profile fields: save", () => {
  it("saves the trimmed values and never sends display_name", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana" });
    mockUpdateProfileFields.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      jobTitle: "Engenheira",
      bio: "Focada em backend.",
      timezone: "America/Sao_Paulo",
      customStatus: "Em reunião",
    });
    renderPage();
    await settled();

    await user.type(jobTitleInput(), "  Engenheira  ");
    await user.type(bioInput(), "Focada em backend.");
    await user.selectOptions(timezoneSelect(), "America/Sao_Paulo");
    await user.type(customStatusInput(), "Em reunião");
    await user.click(saveDetailsBtn());

    await waitFor(() => expect(mockUpdateProfileFields).toHaveBeenCalledTimes(1));
    expect(mockUpdateProfileFields.mock.calls[0]).toEqual([
      {
        jobTitle: "Engenheira",
        bio: "Focada em backend.",
        timezone: "America/Sao_Paulo",
        customStatus: "Em reunião",
      },
      undefined,
    ]);
  });

  it("reflects exactly what the server returned and clears Cancel", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana" });
    mockUpdateProfileFields.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      jobTitle: "Engenheira Sênior",
      bio: "",
      timezone: "",
      customStatus: "",
    });
    renderPage();
    await settled();

    await user.type(jobTitleInput(), "Engenheira");
    await user.click(saveDetailsBtn());

    await waitFor(() => expect(jobTitleInput().value).toBe("Engenheira Sênior"));
    expect(cancelDetailsBtn()).not.toBeInTheDocument();
    expect(screen.getByText(/perfil atualizado/i)).toBeInTheDocument();
  });

  it("a typed failure shows its message and preserves the drafts for retry", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana" });
    mockUpdateProfileFields.mockRejectedValue(
      new UpdateProfileFieldsError("invalid", "Dados inválidos."),
    );
    renderPage();
    await settled();

    await user.type(jobTitleInput(), "Engenheira");
    await user.click(saveDetailsBtn());

    expect(await screen.findByText(/dados inválidos/i)).toBeInTheDocument();
    expect(jobTitleInput().value).toBe("Engenheira");
  });

  it("a generic (non-typed) failure still shows a message instead of leaving the screen blank", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana" });
    mockUpdateProfileFields.mockRejectedValue(new Error("boom"));
    renderPage();
    await settled();

    await user.type(jobTitleInput(), "Engenheira");
    await user.click(saveDetailsBtn());

    expect(await screen.findByText(/não foi possível atualizar o perfil/i)).toBeInTheDocument();
  });

  // The bug this guards against: saving the name (its own form/button) must
  // never call updateProfileFields, and saving the details (its own
  // form/button) must never call updateDisplayName — each screen sends only
  // the fields it owns.
  it("saving the name does not call updateProfileFields, and saving details does not call updateDisplayName", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana" });
    mockUpdateDisplayName.mockResolvedValue({ id: "u1", displayName: "Ana Lima" });
    mockUpdateProfileFields.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      jobTitle: "Engenheira",
    });
    renderPage();
    await settled();

    await user.clear(nameInput());
    await user.type(nameInput(), "Ana Lima");
    await user.click(saveNameBtn());
    await waitFor(() => expect(mockUpdateDisplayName).toHaveBeenCalledTimes(1));
    expect(mockUpdateProfileFields).not.toHaveBeenCalled();

    await user.type(jobTitleInput(), "Engenheira");
    await user.click(saveDetailsBtn());
    await waitFor(() => expect(mockUpdateProfileFields).toHaveBeenCalledTimes(1));
    expect(mockUpdateDisplayName).toHaveBeenCalledTimes(1); // still just the one call from before
  });
});

// ID 13 (20/08) owns propagating a saved name to the sidebar and elsewhere via
// refreshSelfProfile; ID 7 must NOT do that itself. This guards the boundary:
// the shared self-profile cache (what the sidebar reads) must stay exactly
// what it was before the save.
describe("ProfilePage — display name: does not sync globally (reserved for ID 13)", () => {
  function SelfNameProbe() {
    const state = useSelfProfile();
    return (
      <span data-testid="self-name">
        {state.status === "ready" ? state.profile.displayName : state.status}
      </span>
    );
  }
  function renderPageWithNameProbe() {
    return render(
      <MemoryRouter>
        <ProfilePage />
        <SelfNameProbe />
      </MemoryRouter>,
    );
  }
  const selfName = () => screen.getByTestId("self-name").textContent;

  it("does not refresh the shared self-profile after a successful save", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana", avatarUrl: undefined });
    mockUpdateDisplayName.mockResolvedValue({ id: "u1", displayName: "Ana Lima Souza" });
    renderPageWithNameProbe();
    await settled();
    await waitFor(() => expect(selfName()).toBe("Ana"));
    // ProfilePage's own load and the shared self-profile cache's load are two
    // independent GETs at mount; what matters below is that this count does
    // not grow after the save.
    const callsAtMount = mockFetchProfile.mock.calls.length;

    // If the server were asked again after save, it would answer with the new
    // name — which is exactly what must NOT happen here.
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana Lima Souza",
      avatarUrl: undefined,
    });

    await user.clear(nameInput());
    await user.type(nameInput(), "Ana Lima Souza");
    await user.click(saveNameBtn());
    await waitFor(() => expect(screen.getByText(/nome atualizado/i)).toBeInTheDocument());

    // The page itself shows the new name...
    expect(nameInput().value).toBe("Ana Lima Souza");
    // ...but the shared cache other screens read from is untouched.
    expect(selfName()).toBe("Ana");
    expect(mockFetchProfile).toHaveBeenCalledTimes(callsAtMount);
  });
});

// ── Shared self-profile ───────────────────────────────────────────────────────

/** Stands in for any other screen showing the profile (the sidebar footer). */
function SelfProbe() {
  const state = useSelfProfile();
  return (
    <span data-testid="self">
      {state.status === "ready" ? (state.profile.avatarUrl ?? "none") : state.status}
    </span>
  );
}

function renderPageWithProbe() {
  return render(
    <MemoryRouter>
      <ProfilePage />
      <SelfProbe />
    </MemoryRouter>,
  );
}

const self = () => screen.getByTestId("self").textContent;

describe("ProfilePage — shared self-profile", () => {
  it("publishes a confirmed upload so other screens follow without a reload", async () => {
    const user = userEvent.setup();
    mockUpload.mockResolvedValue("/api/auth/avatars/new.png");
    renderPageWithProbe();
    await settled();
    await waitFor(() => expect(self()).toBe("none"));

    // What the sidebar will read next is the server's answer, not the upload's.
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/new.png",
    });
    await user.upload(fileInput(), pngFile());
    await user.click(uploadBtn());

    await waitFor(() => expect(self()).toBe("/api/auth/avatars/new.png"));
  });

  it("publishes a confirmed removal", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/saved.png",
    });
    mockRemove.mockResolvedValue(undefined);
    renderPageWithProbe();
    await settled();
    await waitFor(() => expect(self()).toBe("/api/auth/avatars/saved.png"));

    mockFetchProfile.mockResolvedValue({ id: "u1", displayName: "Ana", avatarUrl: undefined });
    await user.click(removeBtn());

    await waitFor(() => expect(self()).toBe("none"));
  });

  it("publishes nothing the server did not confirm", async () => {
    const user = userEvent.setup();
    mockFetchProfile.mockResolvedValue({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/old.png",
    });
    mockUpload.mockRejectedValue(new AvatarUploadError("unknown", "Falhou."));
    renderPageWithProbe();
    await settled();
    await waitFor(() => expect(self()).toBe("/api/auth/avatars/old.png"));

    await user.upload(fileInput(), pngFile());
    await user.click(uploadBtn());
    expect(await screen.findByText(/falhou/i)).toBeInTheDocument();

    // One load (the mount), never a second: a failure publishes nothing.
    expect(self()).toBe("/api/auth/avatars/old.png");
  });
});

describe("ProfilePage — sound notification mode", () => {
  const offOption = () => screen.getByLabelText(/desativado/i) as HTMLInputElement;
  const allOption = () => screen.getByLabelText(/todas as mensagens/i) as HTMLInputElement;
  const mentionsOption = () => screen.getByLabelText(/somente menções/i) as HTMLInputElement;

  beforeEach(() => {
    localStorage.clear();
  });
  afterEach(() => {
    localStorage.clear();
  });

  it("defaults to 'all' checked when nothing is persisted", async () => {
    renderPage();
    await settled();
    expect(allOption().checked).toBe(true);
    expect(offOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(false);
  });

  it("reflects a previously persisted 'off' mode on mount", async () => {
    localStorage.setItem("nchat.notifications.sound.mode", "off");
    renderPage();
    await settled();
    expect(offOption().checked).toBe(true);
  });

  it("reflects a previously persisted 'mentions' mode on mount", async () => {
    localStorage.setItem("nchat.notifications.sound.mode", "mentions");
    renderPage();
    await settled();
    expect(mentionsOption().checked).toBe(true);
  });

  it("migrates the legacy boolean preference (false -> off) when no mode is persisted yet", async () => {
    localStorage.setItem("nchat.notifications.sound.enabled", "false");
    renderPage();
    await settled();
    expect(offOption().checked).toBe(true);
  });

  it("selecting 'off' persists the mode", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.click(offOption());

    expect(offOption().checked).toBe(true);
    expect(getSoundNotificationMode()).toBe("off");
  });

  it("selecting 'mentions' persists the mode", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.click(mentionsOption());

    expect(mentionsOption().checked).toBe(true);
    expect(getSoundNotificationMode()).toBe("mentions");
  });

  it("selecting 'all' back persists the mode", async () => {
    const user = userEvent.setup();
    localStorage.setItem("nchat.notifications.sound.mode", "off");
    renderPage();
    await settled();

    await user.click(allOption());

    expect(allOption().checked).toBe(true);
    expect(getSoundNotificationMode()).toBe("all");
  });

  it("is reachable and selectable via each option's associated label", async () => {
    renderPage();
    await settled();

    // getByLabelText already proves the <label htmlFor> association; this
    // proves clicking the label text (not just the input) selects the option.
    await userEvent.click(screen.getByText("Somente menções"));

    expect(mentionsOption().checked).toBe(true);
    expect(allOption().checked).toBe(false);
  });

  it("only one option is checked at a time (radio group behaves as a group)", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.click(offOption());
    expect(offOption().checked).toBe(true);
    expect(allOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(false);

    await user.click(mentionsOption());
    expect(offOption().checked).toBe(false);
    expect(allOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(true);
  });
});

describe("ProfilePage — 'Menções e mensagens diretas' sound mode", () => {
  const offOption = () => screen.getByLabelText(/desativado/i) as HTMLInputElement;
  const allOption = () => screen.getByLabelText(/todas as mensagens/i) as HTMLInputElement;
  const mentionsOption = () => screen.getByLabelText(/^somente menções$/i) as HTMLInputElement;
  const mentionsAndDmsOption = () =>
    screen.getByLabelText(/menções e mensagens diretas/i) as HTMLInputElement;

  beforeEach(() => {
    localStorage.clear();
  });
  afterEach(() => {
    localStorage.clear();
  });

  it("is a normal, equal, non-nested radio option alongside the other three", async () => {
    renderPage();
    await settled();

    expect(mentionsAndDmsOption().type).toBe("radio");
    expect(mentionsAndDmsOption()).not.toBeDisabled();
    expect(mentionsAndDmsOption().checked).toBe(false);
  });

  it("selecting it persists the mode", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.click(mentionsAndDmsOption());

    expect(mentionsAndDmsOption().checked).toBe(true);
    expect(getSoundNotificationMode()).toBe("mentions_and_dms");
  });

  it("reflects a previously persisted value on mount", async () => {
    localStorage.setItem("nchat.notifications.sound.mode", "mentions_and_dms");
    renderPage();
    await settled();

    expect(mentionsAndDmsOption().checked).toBe(true);
  });

  it("migrates the legacy 'mentions' + DM-flag combination to this mode on mount", async () => {
    localStorage.setItem("nchat.notifications.sound.mode", "mentions");
    localStorage.setItem("nchat.notifications.sound.dmWithoutMention", "true");
    renderPage();
    await settled();

    expect(mentionsAndDmsOption().checked).toBe(true);
    expect(mentionsOption().checked).toBe(false);
  });

  it("behaves as part of the same mutually exclusive group as the other three options", async () => {
    const user = userEvent.setup();
    renderPage();
    await settled();

    await user.click(mentionsAndDmsOption());
    expect(offOption().checked).toBe(false);
    expect(allOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(false);
    expect(mentionsAndDmsOption().checked).toBe(true);

    await user.click(mentionsOption());
    expect(mentionsAndDmsOption().checked).toBe(false);
    expect(mentionsOption().checked).toBe(true);
  });

  it("is reachable and selectable via its associated label", async () => {
    renderPage();
    await settled();

    await userEvent.click(screen.getByText("Menções e mensagens diretas"));

    expect(mentionsAndDmsOption().checked).toBe(true);
  });
});
