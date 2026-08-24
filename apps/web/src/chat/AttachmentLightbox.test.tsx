/**
 * AttachmentLightbox tests (issue #491).
 *
 * The shell (portal, dialog semantics, focus trap, Escape) is copied from
 * AddMembersDialog, already covered for that dialog — these tests focus on
 * what is specific here: no Baixar/no controls beyond Fechar and the
 * reduced-motion play toggle, and the fetch-reuse-vs-fetch-original split.
 * Focus RETURN to the trigger is MessageAttachments' responsibility (it owns
 * the trigger element), so it is asserted there, not here.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AttachmentLightbox from "./AttachmentLightbox";
import type { ChannelAttachment } from "./chatTypes";

const mockContent = vi.hoisted(() => vi.fn());
vi.mock("./filesApi", () => ({
  fetchAttachmentContent: (...args: unknown[]) => mockContent(...args),
}));

const createObjectURL = vi.fn();
const revokeObjectURL = vi.fn();
const onClose = vi.fn();

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "img-1",
    filename: "paisagem.png",
    contentType: "image/png",
    size: 2048,
    status: "clean",
    previewStatus: "ready",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

function stubMatchMedia(reduced: boolean) {
  window.matchMedia = ((query: string) =>
    ({
      matches: query.includes("reduce") ? reduced : false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    }) as unknown as MediaQueryList) as typeof window.matchMedia;
}

beforeEach(() => {
  mockContent.mockReset().mockResolvedValue(new Blob(["original-bytes"]));
  onClose.mockReset();
  createObjectURL.mockReset();
  revokeObjectURL.mockReset();
  let created = 0;
  createObjectURL.mockImplementation(() => `blob:orig-${++created}`);
  vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
  stubMatchMedia(false);
});

afterEach(() => {
  vi.unstubAllGlobals();
  // @ts-expect-error -- restore jsdom's absence of matchMedia between tests.
  delete window.matchMedia;
});

describe("shell", () => {
  it("renders into document.body as a labelled dialog", () => {
    render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog.parentElement?.parentElement).toBe(document.body);
    expect(screen.getByText("paisagem.png")).toBeInTheDocument();
  });

  it("offers no download control — the enlarged view is media plus Fechar only", () => {
    render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    expect(screen.queryByRole("button", { name: /baixar/i })).not.toBeInTheDocument();
  });

  it("focuses Fechar on open", () => {
    render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    expect(screen.getByRole("button", { name: "Fechar visualização ampliada" })).toHaveFocus();
  });

  it("closes on Escape", async () => {
    const user = userEvent.setup();
    render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes on a backdrop click but not on a click inside the dialog", async () => {
    const user = userEvent.setup();
    render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    await user.click(screen.getByRole("dialog"));
    expect(onClose).not.toHaveBeenCalled();

    await user.click(screen.getByRole("dialog").parentElement as HTMLElement);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes on the Fechar button", async () => {
    const user = userEvent.setup();
    render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Fechar visualização ampliada" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("resolution", () => {
  it("fetches the original after mount and shows the inline URL immediately meanwhile", async () => {
    mockContent.mockReturnValue(new Promise(() => {}));
    render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    expect(screen.getByRole("dialog").querySelector("img")).toHaveAttribute(
      "src",
      "blob:preview-1",
    );
    await waitFor(() => expect(mockContent).toHaveBeenCalledWith("img-1", expect.any(AbortSignal)));
  });

  it("swaps to the original once it resolves", async () => {
    render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    await waitFor(() =>
      expect(screen.getByRole("dialog").querySelector("img")).toHaveAttribute("src", "blob:orig-1"),
    );
  });

  it("does not fetch again when the inline URL is already the original", () => {
    render(
      <AttachmentLightbox
        attachment={attachment({ contentType: "image/webp" })}
        inlineUrl="blob:webp-original"
        inlineIsOriginal
        onClose={onClose}
      />,
    );

    expect(screen.getByRole("dialog").querySelector("img")).toHaveAttribute(
      "src",
      "blob:webp-original",
    );
    expect(mockContent).not.toHaveBeenCalled();
  });

  it("keeps showing the inline URL when the upgrade fetch fails", async () => {
    mockContent.mockRejectedValue(new Error("403"));
    render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    await waitFor(() => expect(mockContent).toHaveBeenCalled());
    expect(screen.getByRole("dialog").querySelector("img")).toHaveAttribute(
      "src",
      "blob:preview-1",
    );
  });

  it("revokes the original URL it created when it unmounts", async () => {
    const { unmount } = render(
      <AttachmentLightbox
        attachment={attachment()}
        inlineUrl="blob:preview-1"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );
    await waitFor(() =>
      expect(screen.getByRole("dialog").querySelector("img")).toHaveAttribute("src", "blob:orig-1"),
    );

    unmount();

    expect(revokeObjectURL).toHaveBeenCalledWith("blob:orig-1");
    // The URL the card owns is never this component's to revoke.
    expect(revokeObjectURL).not.toHaveBeenCalledWith("blob:preview-1");
  });
});

describe("GIF + reduced motion", () => {
  function gifAttachment(overrides: Partial<ChannelAttachment> = {}) {
    return attachment({ contentType: "image/gif", ...overrides });
  }

  beforeEach(() => stubMatchMedia(true));

  it("shows the static preview and a play control instead of auto-fetching the original", () => {
    render(
      <AttachmentLightbox
        attachment={gifAttachment()}
        inlineUrl="blob:static-preview"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    expect(screen.getByRole("dialog").querySelector("img")).toHaveAttribute(
      "src",
      "blob:static-preview",
    );
    expect(mockContent).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Reproduzir animação" })).toBeInTheDocument();
  });

  it("fetches and shows the animated original once the user presses play", async () => {
    const user = userEvent.setup();
    render(
      <AttachmentLightbox
        attachment={gifAttachment()}
        inlineUrl="blob:static-preview"
        inlineIsOriginal={false}
        onClose={onClose}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Reproduzir animação" }));

    await waitFor(() =>
      expect(screen.getByRole("dialog").querySelector("img")).toHaveAttribute("src", "blob:orig-1"),
    );
    expect(screen.queryByRole("button", { name: "Reproduzir animação" })).not.toBeInTheDocument();
  });

  it("shows no play control when the card already sent the animated original", () => {
    render(
      <AttachmentLightbox
        attachment={gifAttachment()}
        inlineUrl="blob:already-animated"
        inlineIsOriginal
        onClose={onClose}
      />,
    );

    expect(screen.queryByRole("button", { name: "Reproduzir animação" })).not.toBeInTheDocument();
    expect(mockContent).not.toHaveBeenCalled();
  });
});
