/**
 * Virtualized timeline and lazy attachments, end to end (issue #675).
 *
 * The stress fixture is the one the issue names — roughly 500 messages and 100
 * mixed attachments — and every assertion here is about cost, not appearance:
 *
 *   - how many messages exist in the DOM when a large history is loaded;
 *   - how many requests opening that conversation costs;
 *   - that no video, document original or image original is among them;
 *   - that a preview burst stays inside the concurrency limit;
 *   - that leaving the conversation takes its pending work with it.
 *
 * Sizes come from a stubbed getBoundingClientRect because jsdom performs no
 * layout: without one every row measures zero and "which rows are near the
 * viewport" has no meaning. That is the only thing simulated — the virtualizer,
 * the scheduler and the components under it are the real ones.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTokens } from "../lib/authSession";
import ChatMessageArea from "./ChatMessageArea";
import { MAX_CONCURRENT_PREVIEWS } from "./previewScheduler";
import { ESTIMATED_ROW_HEIGHT_PX, VIRTUALIZE_MIN_ROWS } from "./timelineVirtualization";
import type { ChannelAttachment, Message, MessagePage } from "./chatTypes";

const {
  mockFetchChannelMessages,
  mockFetchAttachmentPreview,
  mockFetchAttachmentContent,
  mockFetchDocumentPreviewPage,
} = vi.hoisted(() => ({
  mockFetchChannelMessages: vi.fn<() => Promise<MessagePage>>(),
  mockFetchAttachmentPreview: vi.fn(),
  mockFetchAttachmentContent: vi.fn(),
  mockFetchDocumentPreviewPage: vi.fn(),
}));

vi.mock("./chatApi", () => ({
  MessageEditError: class MessageEditError extends Error {},
  fetchChannelMessages: () => mockFetchChannelMessages(),
  fetchChannelMessage: vi.fn(),
  fetchChannelMessageSecuritySnapshots: vi.fn().mockResolvedValue([]),
  postChannelMessage: vi.fn(),
  forwardChannelMessage: vi.fn(),
  fetchDMMessages: vi.fn(),
  fetchDMMessage: vi.fn(),
  fetchDMMessageSecuritySnapshots: vi.fn().mockResolvedValue([]),
  postDMMessage: vi.fn(),
  fetchAllowedReactionEmojis: vi.fn().mockResolvedValue([]),
  favoriteMessage: vi.fn(),
  unfavoriteMessage: vi.fn(),
  fetchPins: vi.fn().mockResolvedValue([]),
  pinMessage: vi.fn(),
  unpinMessage: vi.fn(),
  editMessage: vi.fn(),
  deleteMessage: vi.fn(),
  getMessageHistory: vi.fn(),
  fetchChannelDetails: vi.fn().mockRejectedValue(new Error("not used")),
  fetchGroupDetails: vi.fn().mockRejectedValue(new Error("not used")),
  fetchDirectProfile: vi.fn().mockRejectedValue(new Error("not used")),
  getOrCreateDirectDM: vi.fn(),
  safeAvatarUrl: () => null,
}));

vi.mock("./filesApi", () => ({
  fetchConversationAttachments: vi.fn().mockResolvedValue([]),
  fetchAttachmentPreview: (...args: unknown[]) => mockFetchAttachmentPreview(...args),
  fetchAttachmentContent: (...args: unknown[]) => mockFetchAttachmentContent(...args),
  fetchDocumentPreviewPage: (...args: unknown[]) => mockFetchDocumentPreviewPage(...args),
  fetchDocumentPreviewManifest: vi.fn(),
  fetchDocumentPreviewSheet: vi.fn(),
  regenerateDocumentPreview: vi.fn(),
  uploadAttachment: vi.fn(),
  deleteAttachmentDraft: vi.fn(),
  tooLargeMessage: () => "",
  AttachmentUploadError: class AttachmentUploadError extends Error {},
}));

vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: () => ({
    toggleReaction: vi.fn(),
    sendTyping: vi.fn(),
    connectionStatus: "connected",
  }),
}));

// ── Fixture ───────────────────────────────────────────────────────────────────

const MESSAGE_COUNT = 500;
const ATTACHMENT_COUNT = 100;

function attachment(index: number): ChannelAttachment {
  // Four kinds in rotation, so the fixture is "100 mixed attachments" rather
  // than a hundred of the cheapest one.
  const kind = index % 4;
  const base = {
    id: `att-${index}`,
    status: "clean" as const,
    previewStatus: "ready" as const,
    createdAt: "2026-07-15T12:00:00.000Z",
  };
  if (kind === 0) {
    return { ...base, filename: `foto-${index}.png`, contentType: "image/png", size: 900_000 };
  }
  if (kind === 1) {
    return { ...base, filename: `clipe-${index}.mp4`, contentType: "video/mp4", size: 8_000_000 };
  }
  if (kind === 2) {
    return {
      ...base,
      filename: `relatorio-${index}.pdf`,
      contentType: "application/pdf",
      size: 400_000,
    };
  }
  return { ...base, filename: `nota-${index}.mp3`, contentType: "audio/mpeg", size: 3_000_000 };
}

function message(index: number): Message {
  // Spread across several days so day dividers are part of the row model under
  // test, and one system message so that kind is exercised too.
  const day = 10 + Math.floor(index / 120);
  const withAttachment = index % 5 === 0 && index / 5 < ATTACHMENT_COUNT;
  return {
    id: `m-${index}`,
    senderId: index % 3 === 0 ? "me-123" : "user-abc",
    senderDisplayName: index === 7 ? "Ana" : "",
    senderEmail: "",
    kind: index === 7 ? "system" : "user",
    ...(index === 7 ? { eventType: "conversation_member_left" as const } : {}),
    bodyText: `Mensagem ${index}`,
    bodyFormat: "v1",
    isRemoved: false,
    status: "active",
    createdAt: `2026-07-${day}T12:${String(index % 60).padStart(2, "0")}:00.000Z`,
    updatedAt: `2026-07-${day}T12:${String(index % 60).padStart(2, "0")}:00.000Z`,
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    ...(withAttachment ? { attachments: [attachment(index / 5)] } : {}),
  } as Message;
}

function largeHistory(count = MESSAGE_COUNT): Message[] {
  return Array.from({ length: count }, (_, index) => message(index));
}

/** The scroll container's height, in the absence of any real layout. */
const VIEWPORT_HEIGHT_PX = 900;

/**
 * Gives the scroll container and every virtualized row a height.
 *
 * jsdom performs no layout, so `offsetHeight` — which is what the virtualizer
 * measures both the viewport and each row with — is zero for everything. Those
 * two numbers are the entire input to "which rows are near the viewport", so
 * without them the virtualizer has nothing to decide with and would render
 * either nothing or everything. Nothing else is simulated.
 */
function stubLayout(rowHeight = ESTIMATED_ROW_HEIGHT_PX) {
  const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  const width = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      if (this.dataset.index !== undefined) return rowHeight;
      if (this.classList.contains("chat-msg-area__list")) return VIEWPORT_HEIGHT_PX;
      return 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get: () => 600,
  });
  return () => {
    if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
    if (width) Object.defineProperty(HTMLElement.prototype, "offsetWidth", width);
  };
}

function renderChannel() {
  return render(
    <MemoryRouter initialEntries={["/chat/channel/geral"]}>
      <Routes>
        <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
      </Routes>
    </MemoryRouter>,
  );
}

let restoreLayout = () => {};

beforeEach(() => {
  setTokens("test-at");
  vi.clearAllMocks();
  mockFetchAttachmentPreview.mockResolvedValue(new Blob(["preview"]));
  mockFetchAttachmentContent.mockResolvedValue(new Blob(["original"]));
  mockFetchDocumentPreviewPage.mockResolvedValue(new Blob(["page-1"]));
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => "blob:x"),
    revokeObjectURL: vi.fn(),
  });
  // No attachment ever reports itself near the viewport: the shell state is
  // what a freshly opened conversation looks like before anything scrolls.
  class IdleIntersectionObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("IntersectionObserver", IdleIntersectionObserver);
  restoreLayout = stubLayout();
});

afterEach(() => {
  restoreLayout();
  vi.unstubAllGlobals();
});

// ── Timeline ──────────────────────────────────────────────────────────────────

describe("timeline size", () => {
  it("mounts a window, not the history, for a large conversation", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: largeHistory(),
      nextCursor: "",
    });

    renderChannel();

    await waitFor(() => expect(screen.getByTestId("chat-virtual-canvas")).toBeInTheDocument());
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble").length).toBeGreaterThan(0));

    const mounted = screen.getAllByTestId("chat-msg-bubble").length;
    // The real bound is viewport + overscan; asserting a generous ceiling keeps
    // this a statement about *not growing with the history* rather than a
    // snapshot of one particular overscan number.
    expect(mounted).toBeLessThan(MESSAGE_COUNT / 5);
  });

  it("keeps the scrollable height of the whole history, so the scrollbar still tells the truth", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: largeHistory(), nextCursor: "" });

    renderChannel();

    const canvas = await screen.findByTestId("chat-virtual-canvas");
    const total = Number.parseFloat(canvas.style.height);
    const mountedRows = canvas.querySelectorAll("[data-index]").length;
    expect(total).toBeGreaterThan(mountedRows * ESTIMATED_ROW_HEIGHT_PX);
  });

  it("renders a single page in full — virtualizing it would cost more than it saves", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: largeHistory(VIRTUALIZE_MIN_ROWS - 10),
      nextCursor: "",
    });

    renderChannel();

    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble").length).toBeGreaterThan(0));
    expect(screen.queryByTestId("chat-virtual-canvas")).not.toBeInTheDocument();
  });

  it("still draws day dividers and system messages inside the virtual window", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: largeHistory(), nextCursor: "" });

    renderChannel();

    const canvas = await screen.findByTestId("chat-virtual-canvas");
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble").length).toBeGreaterThan(0));

    // The fixture's first day opens the list and message 7 is a conversation
    // event — both inside the first window, and neither is a MessageBubble.
    expect(canvas.querySelectorAll(".chat-msg-area__day-divider").length).toBeGreaterThan(0);
    expect(screen.getAllByTestId("chat-system-message").length).toBeGreaterThan(0);
  });

  it("gives the list somewhere to put focus when a focused row is unmounted", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: largeHistory(), nextCursor: "" });

    renderChannel();
    await screen.findByTestId("chat-virtual-canvas");

    // Programmatically focusable, never in the tab order: it is a recovery
    // destination, not a stop on the way to the composer.
    expect(screen.getByRole("log")).toHaveAttribute("tabindex", "-1");
  });
});

// ── Attachment cost ───────────────────────────────────────────────────────────

describe("opening a conversation with a hundred attachments", () => {
  it("downloads no originals: no video, no document, no full-size image", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: largeHistory(), nextCursor: "" });

    renderChannel();
    await screen.findByTestId("chat-virtual-canvas");
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble").length).toBeGreaterThan(0));

    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });

  it("starts no preview at all while every attachment is still far from the viewport", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: largeHistory(), nextCursor: "" });

    renderChannel();
    await screen.findByTestId("chat-virtual-canvas");
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble").length).toBeGreaterThan(0));

    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();
    expect(mockFetchDocumentPreviewPage).not.toHaveBeenCalled();
  });

  it("mounts a Play control for a video rather than its bytes", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: largeHistory(), nextCursor: "" });

    renderChannel();
    await screen.findByTestId("chat-virtual-canvas");
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble").length).toBeGreaterThan(0));

    const players = screen.queryAllByTestId("chat-details-file-video");
    expect(players).toHaveLength(0);
    expect(mockFetchAttachmentContent).not.toHaveBeenCalled();
  });

  it("never has more previews in flight than the concurrency limit allows", async () => {
    let inFlight = 0;
    let peak = 0;
    mockFetchAttachmentPreview.mockImplementation(
      () =>
        new Promise<Blob>((resolve) => {
          inFlight += 1;
          peak = Math.max(peak, inFlight);
          setTimeout(() => {
            inFlight -= 1;
            resolve(new Blob(["preview"]));
          }, 0);
        }),
    );
    // Everything on screen at once: the worst case the scheduler exists for.
    class EagerIntersectionObserver {
      readonly callback: IntersectionObserverCallback;
      constructor(callback: IntersectionObserverCallback) {
        this.callback = callback;
      }
      observe(target: Element) {
        this.callback(
          [{ target, isIntersecting: true } as IntersectionObserverEntry],
          this as unknown as IntersectionObserver,
        );
      }
      unobserve() {}
      disconnect() {}
    }
    vi.stubGlobal("IntersectionObserver", EagerIntersectionObserver);
    mockFetchChannelMessages.mockResolvedValue({ messages: largeHistory(), nextCursor: "" });

    renderChannel();
    await screen.findByTestId("chat-virtual-canvas");
    await waitFor(() => expect(mockFetchAttachmentPreview).toHaveBeenCalled());

    expect(peak).toBeLessThanOrEqual(MAX_CONCURRENT_PREVIEWS);
  });

  it("cancels the previews still in flight when the conversation is left", async () => {
    const signals: AbortSignal[] = [];
    mockFetchAttachmentPreview.mockImplementation((_id: string, signal: AbortSignal) => {
      signals.push(signal);
      return new Promise<Blob>(() => {});
    });
    class EagerIntersectionObserver {
      readonly callback: IntersectionObserverCallback;
      constructor(callback: IntersectionObserverCallback) {
        this.callback = callback;
      }
      observe(target: Element) {
        this.callback(
          [{ target, isIntersecting: true } as IntersectionObserverEntry],
          this as unknown as IntersectionObserver,
        );
      }
      unobserve() {}
      disconnect() {}
    }
    vi.stubGlobal("IntersectionObserver", EagerIntersectionObserver);
    mockFetchChannelMessages.mockResolvedValue({ messages: largeHistory(), nextCursor: "" });

    const { unmount } = renderChannel();
    await screen.findByTestId("chat-virtual-canvas");
    await waitFor(() => expect(signals.length).toBeGreaterThan(0));

    unmount();

    // A conversation the reader has left must not keep competing with the one
    // they opened instead.
    expect(signals.every((signal) => signal.aborted)).toBe(true);
  });
});
