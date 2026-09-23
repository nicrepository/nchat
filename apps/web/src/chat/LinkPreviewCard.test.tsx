import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import LinkPreviewCard from "./LinkPreviewCard";
import type { MessageLink } from "./messageLinks";

const fetchLinkPreviewImage = vi.fn<(id: string, signal?: AbortSignal) => Promise<Blob>>();
vi.mock("./chatApi", () => ({
  fetchLinkPreviewImage: (id: string, signal?: AbortSignal) => fetchLinkPreviewImage(id, signal),
}));

const link: MessageLink = {
  ordinal: 0,
  targetKey: "key-a",
  text: "https://example.test/a",
  url: "https://example.test/a",
  hostname: "example.test",
  safety: "safe",
  click: "direct",
  href: "https://example.test/a",
  updatedAt: "2026-08-18T12:00:00Z",
  preview: {
    state: "ready",
    hostname: "example.test",
    siteName: "Example",
    title: "Title",
    description: "Desc",
    imageId: "11111111-1111-4111-8111-111111111111",
    imageWidth: 480,
    imageHeight: 240,
  },
};

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => "blob:nchat/thumb"),
    revokeObjectURL: vi.fn(),
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("LinkPreviewCard image", () => {
  it("loads the derived thumbnail from chat-service and shows it as a blob url", async () => {
    fetchLinkPreviewImage.mockResolvedValue(new Blob(["jpeg"], { type: "image/jpeg" }));
    render(<LinkPreviewCard messageId="m1" link={link} />);

    const image = await screen.findByRole("img", { name: "Imagem de Title" });
    expect(image).toHaveAttribute("src", "blob:nchat/thumb");
    expect(fetchLinkPreviewImage).toHaveBeenCalledWith(
      "11111111-1111-4111-8111-111111111111",
      expect.anything(),
    );
    // Never a remote source, whatever the page declared.
    expect(image.getAttribute("src")).not.toMatch(/^https?:/);
  });

  it("draws the card without an image when the thumbnail cannot be read", async () => {
    fetchLinkPreviewImage.mockRejectedValue(new Error("404"));
    render(<LinkPreviewCard messageId="m1" link={link} />);

    await waitFor(() => expect(fetchLinkPreviewImage).toHaveBeenCalled());
    expect(screen.getByTestId("chat-link-card")).toHaveTextContent("Title");
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });
});

describe("LinkPreviewCard menu", () => {
  it("is keyboard navigable, copies the href and closes on Escape", async () => {
    fetchLinkPreviewImage.mockResolvedValue(new Blob(["jpeg"]));
    const writeText = vi.fn(() => Promise.resolve());
    Object.assign(navigator, { clipboard: { writeText } });
    render(<LinkPreviewCard messageId="m1" link={link} />);

    const trigger = screen.getByRole("button", { name: "Opções da visualização de example.test" });
    fireEvent.click(trigger);
    const menu = screen.getByRole("menu");
    const items = screen.getAllByRole("menuitem");
    expect(document.activeElement).toBe(items[0]);
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    expect(document.activeElement).toBe(items[1]);
    fireEvent.keyDown(menu, { key: "ArrowUp" });
    expect(document.activeElement).toBe(items[0]);

    fireEvent.keyDown(menu, { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(trigger);

    fireEvent.click(trigger);
    await act(async () => {
      fireEvent.click(screen.getByRole("menuitem", { name: "Copiar link" }));
    });
    expect(writeText).toHaveBeenCalledWith("https://example.test/a");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("closes on a pointer down outside", () => {
    fetchLinkPreviewImage.mockResolvedValue(new Blob(["jpeg"]));
    render(<LinkPreviewCard messageId="m1" link={link} />);
    fireEvent.click(screen.getByRole("button", { name: "Opções da visualização de example.test" }));
    expect(screen.getByRole("menu")).toBeInTheDocument();
    fireEvent.pointerDown(document.body);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
});
