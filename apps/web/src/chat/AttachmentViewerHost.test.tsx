/**
 * AttachmentViewerHost tests (issue #675).
 *
 * The whole point of the host is a lifecycle one: a viewer opened from a
 * message must not be taken down when the timeline's virtualization unmounts
 * that message. So the tests here unmount the card while the viewer is open —
 * which is what a scroll does in a large conversation — and then ask whether
 * the viewer is still usable and where focus ended up.
 */

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AttachmentViewerHost from "./AttachmentViewerHost";
import { useAttachmentViewer } from "./attachmentViewer";
import type { ChannelAttachment } from "./chatTypes";

vi.mock("./filesApi", () => ({
  fetchAttachmentContent: () => Promise.reject(new Error("not used")),
  fetchDocumentPreviewManifest: () => Promise.reject(new Error("not used")),
  fetchDocumentPreviewPage: () => Promise.reject(new Error("not used")),
  fetchDocumentPreviewSheet: () => Promise.reject(new Error("not used")),
  regenerateDocumentPreview: vi.fn(),
}));

const attachment: ChannelAttachment = {
  id: "img-1",
  filename: "paisagem.png",
  contentType: "image/png",
  size: 2048,
  status: "clean",
  previewStatus: "ready",
  createdAt: "2026-07-15T12:00:00.000Z",
};

const bytes = new Blob(["preview-bytes"]);

/** A card exactly like a message's: it asks for a viewer and owns the trigger. */
function Card() {
  const viewer = useAttachmentViewer();
  return (
    <button
      type="button"
      onClick={(event) =>
        viewer.openImage(attachment, {
          trigger: event.currentTarget,
          blob: bytes,
          isOriginal: true,
        })
      }
    >
      Ampliar paisagem.png
    </button>
  );
}

/** Mounts the card, and lets a test take it away the way a scroll would. */
function Timeline({ fallback }: { fallback?: () => void }) {
  const [cardMounted, setCardMounted] = useState(true);
  return (
    <AttachmentViewerHost onFocusFallback={fallback}>
      <button type="button" onClick={() => setCardMounted(false)}>
        Rolar para longe
      </button>
      {cardMounted && <Card />}
    </AttachmentViewerHost>
  );
}

beforeEach(() => {
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => "blob:handed-over"),
    revokeObjectURL: vi.fn(),
  });
  window.matchMedia = ((query: string) =>
    ({
      matches: false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    }) as unknown as MediaQueryList) as typeof window.matchMedia;
});

afterEach(() => {
  vi.unstubAllGlobals();
  // @ts-expect-error -- restore jsdom's absence of matchMedia between tests.
  delete window.matchMedia;
});

describe("a viewer opened from a message", () => {
  it("stays open, and still showing its image, after that message unmounts", async () => {
    const user = userEvent.setup();
    render(<Timeline />);

    await user.click(screen.getByRole("button", { name: "Ampliar paisagem.png" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Rolar para longe" }));

    // The card is gone — the virtualization did what it exists to do — and the
    // viewer is untouched, still drawing the bytes it was handed.
    expect(screen.queryByRole("button", { name: "Ampliar paisagem.png" })).not.toBeInTheDocument();
    const dialog = screen.getByRole("dialog");
    expect(dialog).toBeInTheDocument();
    expect(dialog.querySelector("img")).toHaveAttribute("src", "blob:handed-over");
  });

  it("revokes its own address for the handed-over bytes when it closes", async () => {
    const user = userEvent.setup();
    render(<Timeline />);

    await user.click(screen.getByRole("button", { name: "Ampliar paisagem.png" }));
    await user.click(screen.getByRole("button", { name: "Fechar visualização ampliada" }));

    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:handed-over");
  });
});

describe("focus on close", () => {
  it("returns focus to the control that opened it while it is still there", async () => {
    const user = userEvent.setup();
    render(<Timeline />);
    const trigger = screen.getByRole("button", { name: "Ampliar paisagem.png" });

    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: "Fechar visualização ampliada" }));

    expect(trigger).toHaveFocus();
  });

  it("uses the fallback when that control has been unmounted underneath it", async () => {
    const user = userEvent.setup();
    const fallback = vi.fn();
    render(<Timeline fallback={fallback} />);

    await user.click(screen.getByRole("button", { name: "Ampliar paisagem.png" }));
    await user.click(screen.getByRole("button", { name: "Rolar para longe" }));
    await user.click(screen.getByRole("button", { name: "Fechar visualização ampliada" }));

    // Focusing a detached node sends focus to <body>, which is the silent
    // disappearance the issue forbids.
    expect(fallback).toHaveBeenCalledTimes(1);
  });
});

describe("nesting", () => {
  it("lets the outer host own the viewer, so only one is ever on screen", async () => {
    const user = userEvent.setup();
    render(
      <AttachmentViewerHost>
        <AttachmentViewerHost>
          <Card />
        </AttachmentViewerHost>
      </AttachmentViewerHost>,
    );

    await user.click(screen.getByRole("button", { name: "Ampliar paisagem.png" }));

    expect(screen.getAllByRole("dialog")).toHaveLength(1);
  });
});

describe("without a host", () => {
  it("refuses to render a card whose Ampliar would silently do nothing", () => {
    // Louder than a no-op button: a viewer that never opens is far harder to
    // notice in review than a missing provider is at first render.
    expect(() => render(<Card />)).toThrow(/AttachmentViewerHost/);
  });
});
