import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import ProfilePage from "./ProfilePage";
import { AvatarUploadError } from "./profileApi";
import { _resetSelfProfile, useSelfProfile } from "./selfProfile";

const { mockUpload, mockRemove, mockFetchProfile } = vi.hoisted(() => ({
  mockUpload: vi.fn(),
  mockRemove: vi.fn(),
  mockFetchProfile: vi.fn(),
}));

vi.mock("./profileApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./profileApi")>();
  return {
    ...actual,
    uploadAvatar: (file: File) => mockUpload(file),
    removeAvatar: () => mockRemove(),
    fetchMyProfile: (signal?: AbortSignal) => mockFetchProfile(signal),
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
