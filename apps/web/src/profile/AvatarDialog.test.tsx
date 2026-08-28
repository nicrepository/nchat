import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AvatarDialog from "./AvatarDialog";
import { AvatarUploadError } from "./profileApi";

const { mockUpload, mockRemove, mockRefresh } = vi.hoisted(() => ({
  mockUpload: vi.fn(),
  mockRemove: vi.fn(),
  mockRefresh: vi.fn(),
}));

vi.mock("./profileApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./profileApi")>();
  return {
    ...actual,
    uploadAvatar: (file: File) => mockUpload(file),
    removeAvatar: () => mockRemove(),
  };
});

vi.mock("./selfProfile", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./selfProfile")>();
  return {
    ...actual,
    refreshSelfProfile: () => mockRefresh(),
  };
});

// jsdom lacks object URL support; provide deterministic, counted stubs so
// revoke/leak behaviour can be asserted precisely (same approach as
// ProfilePage.test.tsx, the file this dialog's logic was ported from).
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
});
afterEach(() => {
  vi.clearAllMocks();
});

function pngFile(name = "me.png", bytes: number[] = [1, 2, 3]) {
  return new File([new Uint8Array(bytes)], name, { type: "image/png" });
}
function gifFile(name = "x.gif") {
  return new File([new Uint8Array([1])], name, { type: "image/gif" });
}

// The native input is deliberately hidden (per issue #672 §1.5) and triggered
// only via the visible "Selecionar arquivo" button, so it has no accessible
// role/label to query by — a direct DOM query is the only way to reach it.
const fileInput = () => document.querySelector('input[type="file"]') as HTMLInputElement;
const selectBtn = () => screen.getByRole("button", { name: /selecionar arquivo/i });
const uploadBtn = () => screen.getByRole("button", { name: /enviar avatar/i });
const removeBtn = () => screen.getByRole("button", { name: /remover avatar/i });
const closeBtn = () => screen.getByRole("button", { name: /^fechar$/i });
const cancelSelectionBtn = () => screen.queryByRole("button", { name: /cancelar seleção/i });

describe("AvatarDialog", () => {
  it("renders the hidden file input operable only via the visible trigger button", () => {
    render(<AvatarDialog onClose={vi.fn()} />);
    expect(selectBtn()).toBeInTheDocument();
    expect(fileInput()).toHaveAttribute("hidden");
    expect(fileInput()).toHaveAttribute("aria-hidden", "true");
    expect(fileInput()).toHaveAttribute("tabindex", "-1");
  });

  it("rejects an unsupported file type before staging", () => {
    render(<AvatarDialog onClose={vi.fn()} />);
    fireEvent.change(fileInput(), { target: { files: [gifFile()] } });
    expect(screen.getByText(/escolha uma imagem jpeg ou png/i)).toBeInTheDocument();
    expect(screen.queryByAltText(/pré-visualização/i)).not.toBeInTheDocument();
    expect(uploadBtn()).toBeDisabled();
    expect(mockUpload).not.toHaveBeenCalled();
  });

  it("rejects an oversized file before staging", () => {
    render(<AvatarDialog onClose={vi.fn()} />);
    const big = new File([new Uint8Array(6 * 1024 * 1024)], "big.png", { type: "image/png" });
    fireEvent.change(fileInput(), { target: { files: [big] } });
    expect(screen.getByText(/muito grande/i)).toBeInTheDocument();
    expect(uploadBtn()).toBeDisabled();
    expect(mockUpload).not.toHaveBeenCalled();
  });

  it("shows a preview for a valid selection before uploading, without calling the network", () => {
    render(<AvatarDialog onClose={vi.fn()} />);
    fireEvent.change(fileInput(), { target: { files: [pngFile()] } });
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute("src", "blob:preview-1");
    expect(mockUpload).not.toHaveBeenCalled();
  });

  it("cancel discards the preview only, no network call", async () => {
    const user = userEvent.setup();
    render(<AvatarDialog onClose={vi.fn()} />);
    fireEvent.change(fileInput(), { target: { files: [pngFile()] } });
    expect(screen.getByAltText(/pré-visualização/i)).toBeInTheDocument();

    await user.click(cancelSelectionBtn()!);

    expect(screen.queryByAltText(/pré-visualização/i)).not.toBeInTheDocument();
    expect(mockUpload).not.toHaveBeenCalled();
    expect(mockRemove).not.toHaveBeenCalled();
    expect(revoked).toContain("blob:preview-1");
  });

  it("upload calls uploadAvatar with the selected file, then refreshSelfProfile, then onClose", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    mockUpload.mockResolvedValue("/api/auth/avatars/new.png");
    render(<AvatarDialog onClose={onClose} />);
    const file = pngFile();
    fireEvent.change(fileInput(), { target: { files: [file] } });

    await user.click(uploadBtn());

    await waitFor(() => expect(mockUpload).toHaveBeenCalledWith(file));
    expect(mockRefresh).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("remove calls removeAvatar, then refreshSelfProfile, then onClose", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    mockRemove.mockResolvedValue(undefined);
    render(<AvatarDialog currentAvatarUrl="/api/auth/avatars/old.png" onClose={onClose} />);

    await user.click(removeBtn());

    await waitFor(() => expect(mockRemove).toHaveBeenCalledTimes(1));
    expect(mockRefresh).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("disables Remover avatar when there is no persisted avatar (currentAvatarUrl prop absent)", () => {
    render(<AvatarDialog onClose={vi.fn()} />);
    expect(removeBtn()).toBeDisabled();
  });

  it("enables Remover avatar when currentAvatarUrl is supplied by the caller", () => {
    render(<AvatarDialog currentAvatarUrl="/api/auth/avatars/old.png" onClose={vi.fn()} />);
    expect(removeBtn()).toBeEnabled();
  });

  it("revokes the preview object URL on re-selection (swap), and never twice", () => {
    render(<AvatarDialog onClose={vi.fn()} />);
    fireEvent.change(fileInput(), { target: { files: [pngFile("a.png")] } });
    fireEvent.change(fileInput(), { target: { files: [pngFile("b.png")] } });
    expect(revoked).toEqual(["blob:preview-1"]);
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute("src", "blob:preview-2");
  });

  it("revokes the preview object URL on unmount, exactly once", () => {
    const { unmount } = render(<AvatarDialog onClose={vi.fn()} />);
    fireEvent.change(fileInput(), { target: { files: [pngFile()] } });
    expect(revoked).toEqual([]);

    unmount();
    expect(revoked).toEqual(["blob:preview-1"]);
  });

  it("keeps the selection on a network error during upload, allowing retry", async () => {
    const user = userEvent.setup();
    mockUpload
      .mockRejectedValueOnce(new AvatarUploadError("unknown", "Falhou."))
      .mockResolvedValueOnce("/api/auth/avatars/ok.png");
    render(<AvatarDialog onClose={vi.fn()} />);
    fireEvent.change(fileInput(), { target: { files: [pngFile()] } });

    await user.click(uploadBtn());
    expect(await screen.findByText(/falhou/i)).toBeInTheDocument();
    expect(screen.getByAltText(/pré-visualização/i)).toHaveAttribute("src", "blob:preview-1");
    expect(uploadBtn()).toBeEnabled();

    await user.click(uploadBtn());
    await waitFor(() => expect(mockUpload).toHaveBeenCalledTimes(2));
  });

  it("keeps the persisted avatar visible on a network error during removal, allowing retry", async () => {
    const user = userEvent.setup();
    mockRemove.mockRejectedValueOnce(new AvatarUploadError("unknown", "Erro ao remover."));
    render(<AvatarDialog currentAvatarUrl="/api/auth/avatars/old.png" onClose={vi.fn()} />);

    await user.click(removeBtn());
    expect(await screen.findByText(/erro ao remover/i)).toBeInTheDocument();
    // The persisted avatar renders via PersonAvatarImage with alt="" (no
    // adjacent caption names the person elsewhere in this dialog), so it is
    // not reachable by accessible name — query the preview image directly.
    expect(document.querySelector(".avatar-dialog__preview-img")).toHaveAttribute(
      "src",
      "/api/auth/avatars/old.png",
    );
    expect(mockRefresh).not.toHaveBeenCalled();
  });

  it("a duplicate upload click in the same tick reaches the service exactly once", async () => {
    let resolveUpload!: (value: string) => void;
    mockUpload.mockReturnValue(new Promise((resolve) => (resolveUpload = resolve)));
    render(<AvatarDialog onClose={vi.fn()} />);
    fireEvent.change(fileInput(), { target: { files: [pngFile()] } });

    const btn = uploadBtn();
    fireEvent.click(btn);
    fireEvent.click(btn);

    expect(mockUpload).toHaveBeenCalledTimes(1);
    resolveUpload("/api/auth/avatars/once.png");
    await waitFor(() => expect(mockRefresh).toHaveBeenCalledTimes(1));
  });

  it("closes on Escape when not mid-upload/remove", () => {
    const onClose = vi.fn();
    render(<AvatarDialog onClose={onClose} />);
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("does not close on Escape while an upload is in flight", async () => {
    const onClose = vi.fn();
    let resolveUpload!: (value: string) => void;
    mockUpload.mockReturnValue(new Promise((resolve) => (resolveUpload = resolve)));
    render(<AvatarDialog onClose={onClose} />);
    fireEvent.change(fileInput(), { target: { files: [pngFile()] } });
    fireEvent.click(uploadBtn());

    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();

    resolveUpload("/api/auth/avatars/ok.png");
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("the Fechar button closes the dialog with no pending action", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<AvatarDialog onClose={onClose} />);
    await user.click(closeBtn());
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("announces selection and network errors in a polite live region", async () => {
    render(<AvatarDialog onClose={vi.fn()} />);
    fireEvent.change(fileInput(), { target: { files: [gifFile()] } });
    const status = screen.getByRole("status");
    expect(status).toHaveAttribute("aria-live", "polite");
    expect(status).toHaveTextContent(/escolha uma imagem jpeg ou png/i);
  });
});
