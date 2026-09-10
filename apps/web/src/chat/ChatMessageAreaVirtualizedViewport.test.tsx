/**
 * #492/#788 behaviour on the *virtualized* timeline (issue #675).
 *
 * The rest of the suite exercises those invariants below VIRTUALIZE_MIN_ROWS,
 * where every row is mounted and the DOM answers every question. That is the
 * branch that was already correct. This file is the other one: more than sixty
 * rows, real per-row heights that differ, and only a window of them mounted at
 * any moment — which is when "restore the reader's position" stops being a DOM
 * lookup and becomes a question about a row model.
 *
 * # Why there is a scrollport harness
 *
 * jsdom performs no layout: `offsetHeight` is zero for everything, `scrollTop`
 * is inert, and `Element.scrollTo` does not exist. Those four facts are exactly
 * the inputs the virtualizer and the #492 state machine read, so without them
 * every assertion here would be vacuous — the failure mode PR #792 already hit
 * once, when a regression test passed with the tail-lock removed entirely.
 *
 * So the harness below plays the browser's part and nothing else: it gives rows
 * and the scrollport a height, makes `scrollTop` real and event-emitting, and
 * drives the two sentinels' IntersectionObserver from the scroll geometry. No
 * component behaviour is stubbed, and no TanStack internal is inspected — every
 * assertion reads what a user could see: which message sits at the top of the
 * viewport, how many are mounted, where focus went.
 */

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTokens } from "../lib/authSession";
import { flushResizeObservers } from "../setupTests";
import ChatMessageArea from "./ChatMessageArea";
import { saveViewportAnchor } from "./chatViewportPersistence";
import { PREPEND_ANCHOR_TOLERANCE_PX, VIRTUALIZE_MIN_ROWS } from "./timelineVirtualization";
import type { ChatOutletContext } from "./ChatShell";
import type { Message, MessagePage } from "./chatTypes";
import type { WSMessageCreatedEvent } from "./useChatWebSocket";

const CURRENT_USER = "me-123";
const OTHER_USER = "user-abc";

const { mockFetchChannelMessages, mockPostChannelMessage, wsState } = vi.hoisted(() => ({
  mockFetchChannelMessages: vi.fn<(id: string, before?: string) => Promise<MessagePage>>(),
  mockPostChannelMessage: vi.fn(),
  wsState: { onMessageCreated: null as ((event: WSMessageCreatedEvent) => void) | null },
}));

vi.mock("./chatApi", () => ({
  MessageEditError: class MessageEditError extends Error {},
  fetchChannelMessages: (id: string, before?: string) => mockFetchChannelMessages(id, before),
  fetchChannelMessage: vi.fn(),
  fetchChannelMessageSecuritySnapshots: vi.fn().mockResolvedValue([]),
  postChannelMessage: (...args: unknown[]) => mockPostChannelMessage(...args),
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
  fetchAttachmentPreview: vi.fn(),
  fetchAttachmentContent: vi.fn(),
  fetchDocumentPreviewPage: vi.fn(),
  fetchDocumentPreviewManifest: vi.fn(),
  fetchDocumentPreviewSheet: vi.fn(),
  regenerateDocumentPreview: vi.fn(),
  uploadAttachment: vi.fn(),
  deleteAttachmentDraft: vi.fn(),
  tooLargeMessage: () => "",
  AttachmentUploadError: class AttachmentUploadError extends Error {},
}));

vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: ({
    onMessageCreated,
  }: {
    onMessageCreated: (event: WSMessageCreatedEvent) => void;
  }) => {
    wsState.onMessageCreated = onMessageCreated;
    return { toggleReaction: vi.fn(), sendTyping: vi.fn(), connectionStatus: "connected" };
  },
}));

// ── Fixture ───────────────────────────────────────────────────────────────────

const VIEWPORT_PX = 600;

/**
 * Deliberately uneven, and never a multiple of the estimate: a row model that
 * only worked for uniform heights would land every assertion here off by the
 * accumulated difference.
 */
function heightFor(index: number): number {
  const heights = [64, 96, 148, 212, 88];
  return heights[((index % heights.length) + heights.length) % heights.length];
}

/** Dividers and system rows: one height, and not one a message ever has. */
const NON_MESSAGE_ROW_HEIGHT = 40;

/**
 * The ordinal in a fixture message id, including the negative ids a prepended
 * page uses.
 *
 * Heights are keyed on this rather than on the row's index on purpose: a
 * message does not become taller because twenty older ones loaded above it, and
 * a harness that said otherwise would invent drift the component never caused
 * — and would disagree with the virtualizer, which caches by message id.
 */
function messageOrdinal(messageId: string): number {
  return Number(messageId.slice("m-".length));
}

function message(index: number, overrides: Partial<Message> = {}): Message {
  const day = index < 40 ? 14 : 15;
  return {
    id: `m-${index}`,
    senderId: index % 4 === 0 ? CURRENT_USER : OTHER_USER,
    senderDisplayName: index % 4 === 0 ? "Eu" : "Outra pessoa",
    senderEmail: "",
    kind: "user",
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
    ...overrides,
  } as Message;
}

/**
 * Comfortably past VIRTUALIZE_MIN_ROWS once day dividers are counted — and no
 * further.
 *
 * Every case here renders this many rows and drives them through repeated
 * measure/reflow passes, so each extra message is paid for twenty times over,
 * and again under coverage instrumentation. What is being tested is the
 * *virtualized* branch, which needs the threshold crossed and nothing more.
 */
const HISTORY_SIZE = VIRTUALIZE_MIN_ROWS + 8;

function history(count = HISTORY_SIZE, offset = 0): Message[] {
  return Array.from({ length: count }, (_, i) => message(offset + i));
}

/** A message.created event in the shape chat-service really sends. */
function messageCreated(id: string, bodyText: string): WSMessageCreatedEvent {
  return {
    type: "message.created",
    workspace_id: "w-1",
    target_type: "channel",
    target_id: "geral",
    message_id: id,
    event_id: `evt-${id}`,
    created_at: "2026-07-15T13:00:00.000Z",
    payload: {
      id,
      workspace_id: "w-1",
      channel_id: "geral",
      sender_id: OTHER_USER,
      sender_display_name: "Outra pessoa",
      kind: "user",
      body_text: bodyText,
      body_format: "v1",
      status: "active",
      created_at: "2026-07-15T13:00:00.000Z",
      updated_at: "2026-07-15T13:00:00.000Z",
    },
  } as unknown as WSMessageCreatedEvent;
}

// ── Scrollport harness ────────────────────────────────────────────────────────

interface Scrollport {
  /** Extra height a specific row reports, for the remeasure case. */
  grow: (messageId: string, height: number) => void;
  scrollTo: (top: number) => void;
  top: () => number;
  height: () => number;
  /** The message id of the row currently at the top edge of the viewport. */
  topmostMessageId: () => string | null;
  mountedBubbles: () => number;
  /**
   * Makes the scrollport refuse to move, as a physical limit does.
   *
   * The premise of the restoration's no-movement ending: a write that changes
   * nothing has no scroll event and therefore no next commit.
   */
  setImmovable: (immovable: boolean) => void;
}

const rowHeightOverrides = new Map<string, number>();
let intersectionCallbacks: Array<{
  callback: IntersectionObserverCallback;
  targets: Set<Element>;
}> = [];

function listElement(): HTMLElement {
  return screen.getByRole("log");
}

function canvas(): HTMLElement | null {
  return screen.queryByTestId("chat-virtual-canvas");
}

/**
 * Where a message's own box sits relative to the top edge of the scrollport.
 *
 * The measure the anchor invariant is actually about: "the message I was
 * reading is still where it was" is a statement about this number, not about
 * which id happens to be topmost — a row straddling the edge belongs to both
 * readings and would flip on a single pixel.
 */
function offsetOfMessage(messageId: string): number | null {
  const element = document.querySelector<HTMLElement>(`[data-message-id="${messageId}"]`);
  if (!element) return null;
  return element.getBoundingClientRect().top - listElement().getBoundingClientRect().top;
}

/** The offset the virtualizer positioned a row at. */
function rowStart(element: HTMLElement): number {
  return Number(/translateY\((-?[\d.]+)px\)/.exec(element.style.transform)?.[1] ?? "0");
}

/** Rows currently mounted, with the offset the virtualizer positioned them at. */
function mountedRows(): Array<{ index: number; start: number; element: HTMLElement }> {
  const host = canvas();
  if (!host) return [];
  return [...host.querySelectorAll<HTMLElement>("[data-index]")]
    .map((element) => ({ index: Number(element.dataset.index), start: rowStart(element), element }))
    .sort((a, b) => a.start - b.start);
}

function totalSize(): number {
  return Number.parseFloat(canvas()?.style.height ?? "0");
}

/**
 * Installs the browser behaviour jsdom lacks, for the duration of one test.
 *
 * Heights come from the fixture rather than from layout, `scrollTop` becomes a
 * real property that emits a scroll event, and the sentinels' observer is
 * driven from the resulting geometry — which is what makes AT_BOTTOM, the
 * tail-lock and the top-sentinel prefetch reachable at all.
 */
function installScrollport(): Scrollport {
  const originalHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  const originalWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth");
  const originalIntoView = Element.prototype.scrollIntoView;
  let scrollTop = 0;

  const rowHeight = (element: HTMLElement): number => {
    const messageId = element.querySelector<HTMLElement>("[data-message-id]")?.dataset.messageId;
    if (messageId === undefined) return NON_MESSAGE_ROW_HEIGHT;
    return rowHeightOverrides.get(messageId) ?? heightFor(messageOrdinal(messageId));
  };

  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      if (this.dataset.index !== undefined) return rowHeight(this);
      if (this.classList.contains("chat-msg-area__list")) return VIEWPORT_PX;
      return 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get: () => 720,
  });

  // #675: the anchor logic and the prepend restoration both ask the DOM where
  // a row actually is, and jsdom answers zero for every box — which would make
  // every offset assertion below vacuous and, worse, let a broken restoration
  // pass. Rects are therefore derived from the same geometry the rest of this
  // harness plays: the row's own translateY, its fixture height, and the
  // current scroll offset.
  const originalRect = Element.prototype.getBoundingClientRect;
  const rectAt = (top: number, height: number) =>
    ({
      top,
      bottom: top + height,
      height,
      left: 0,
      right: 720,
      width: 720,
      x: 0,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
  Element.prototype.getBoundingClientRect = function getBoundingClientRect(this: Element) {
    if (this instanceof HTMLElement) {
      if (this.classList.contains("chat-msg-area__list")) return rectAt(0, VIEWPORT_PX);
      const row = this.closest<HTMLElement>("[data-index]");
      if (row) return rectAt(rowStart(row) - scrollTop, rowHeight(row));
    }
    return originalRect.call(this);
  };

  const bound = new WeakSet<HTMLElement>();

  /**
   * Gives the scrollport live metrics, once.
   *
   * Getters rather than snapshots, because the component reads `scrollHeight`
   * at moments the harness does not drive — most importantly in the layout
   * effect right after a prepend, where a stale value would make it compute the
   * wrong compensation and the test would then be measuring the harness.
   */
  const geometry = () => {
    const list = listElement();
    if (!bound.has(list)) {
      bound.add(list);
      Object.defineProperty(list, "scrollHeight", { configurable: true, get: () => totalSize() });
      Object.defineProperty(list, "clientHeight", { configurable: true, get: () => VIEWPORT_PX });
      Object.defineProperty(list, "scrollTop", {
        configurable: true,
        get: () => scrollTop,
        set: (value: number) => applyScroll(value),
      });
    }
    return { list, height: totalSize() };
  };

  /**
   * Settles the layout, then moves — never both in one event.
   *
   * #788: ChatMessageArea trusts a scroll event only when scrollHeight is
   * unchanged since the previous one, because an event that also grew the
   * timeline describes a reflow rather than a person. A browser settles layout
   * before anyone can scroll, so the harness does the same.
   */
  let scrolling = false;
  let immovable = false;
  const applyScroll = (next: number) => {
    if (immovable) return;
    // The component's own compensation assigns scrollTop, which lands back
    // here; one level of that is the point, an unbounded cascade is not.
    if (scrolling) {
      const { height } = geometry();
      scrollTop = Math.max(0, Math.min(next, Math.max(0, height - VIEWPORT_PX)));
      return;
    }
    scrolling = true;
    try {
      const settle = geometry();
      settle.list.dispatchEvent(new Event("scroll"));

      const { list, height } = geometry();
      scrollTop = Math.max(0, Math.min(next, Math.max(0, height - VIEWPORT_PX)));
      list.dispatchEvent(new Event("scroll"));
      flushSentinels();
    } finally {
      scrolling = false;
    }
  };

  const flushSentinels = () => {
    const height = totalSize();
    const atBottom = scrollTop + VIEWPORT_PX >= height - 1;
    const atTop = scrollTop <= 0;
    for (const observer of intersectionCallbacks) {
      const entries = [...observer.targets]
        .map((target) => {
          const element = target as HTMLElement;
          if (element.dataset.testid === "chat-bottom-sentinel") {
            return { target, isIntersecting: atBottom } as IntersectionObserverEntry;
          }
          // The only other sentinel the timeline observes is the top one.
          return { target, isIntersecting: atTop } as IntersectionObserverEntry;
        })
        .filter(Boolean);
      if (entries.length > 0) observer.callback(entries, {} as IntersectionObserver);
    }
  };

  const originalScrollTo = Element.prototype.scrollTo;
  // What virtualizer.scrollToIndex ultimately calls. Without it every
  // programmatic positioning in the virtualized branch would silently no-op,
  // and the tests below would assert nothing.
  Element.prototype.scrollTo = function scrollTo(this: Element, ...args: unknown[]) {
    const options = args[0];
    const top =
      typeof options === "object" && options !== null
        ? Number((options as ScrollToOptions).top ?? 0)
        : Number(args[1] ?? 0);
    if (this instanceof HTMLElement && this.classList.contains("chat-msg-area__list")) {
      applyScroll(top);
    }
  } as typeof Element.prototype.scrollTo;

  Element.prototype.scrollIntoView = function scrollIntoView(this: Element) {
    if (this instanceof HTMLElement && this.dataset.testid === "chat-bottom-sentinel") {
      applyScroll(Number.MAX_SAFE_INTEGER);
      return;
    }
    const row = (this as HTMLElement).closest?.("[data-index]") as HTMLElement | null;
    if (row) {
      applyScroll(rowStart(row));
    }
  };

  return {
    grow: (messageId, height) => {
      rowHeightOverrides.set(messageId, height);
    },
    scrollTo: (top) => act(() => applyScroll(top)),
    top: () => scrollTop,
    height: () => totalSize(),
    topmostMessageId: () => {
      // Dividers are rows too, and carry no message — the first *message* at or
      // below the top edge is what "the message being read" means.
      const row = mountedRows().find((candidate) => {
        const end = candidate.start + rowHeight(candidate.element);
        return end > scrollTop && candidate.element.querySelector("[data-message-id]") !== null;
      });
      return (
        row?.element.querySelector<HTMLElement>("[data-message-id]")?.dataset.messageId ?? null
      );
    },
    mountedBubbles: () => screen.queryAllByTestId("chat-msg-bubble").length,
    setImmovable: (next: boolean) => {
      immovable = next;
    },
    restore: () => {
      if (originalHeight)
        Object.defineProperty(HTMLElement.prototype, "offsetHeight", originalHeight);
      if (originalWidth) Object.defineProperty(HTMLElement.prototype, "offsetWidth", originalWidth);
      Element.prototype.scrollIntoView = originalIntoView;
      Element.prototype.scrollTo = originalScrollTo;
      Element.prototype.getBoundingClientRect = originalRect;
    },
  } as Scrollport & { restore: () => void };
}

// ── Render ────────────────────────────────────────────────────────────────────

function ContextProvider({ ctx }: { ctx: ChatOutletContext }) {
  return <Outlet context={ctx} />;
}

function renderChannel(options: { unreadCount?: number; entry?: string } = {}) {
  const ctx: ChatOutletContext = {
    currentUserId: CURRENT_USER,
    channels: [
      {
        id: "geral",
        name: "geral",
        type: "public",
        canWrite: true,
        unreadCount: options.unreadCount ?? 0,
      },
    ],
    dms: [],
  } as unknown as ChatOutletContext;
  return render(
    <MemoryRouter initialEntries={[options.entry ?? "/chat/channel/geral"]}>
      <Routes>
        <Route path="/chat" element={<ContextProvider ctx={ctx} />}>
          <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

let scrollport: Scrollport & { restore: () => void };

beforeEach(() => {
  setTokens("test-at");
  sessionStorage.clear();
  rowHeightOverrides.clear();
  intersectionCallbacks = [];
  wsState.onMessageCreated = null;
  vi.clearAllMocks();
  class DrivenIntersectionObserver {
    private readonly entry: { callback: IntersectionObserverCallback; targets: Set<Element> };
    constructor(callback: IntersectionObserverCallback) {
      this.entry = { callback, targets: new Set<Element>() };
      intersectionCallbacks.push(this.entry);
    }
    observe(target: Element) {
      this.entry.targets.add(target);
    }
    unobserve(target: Element) {
      this.entry.targets.delete(target);
    }
    disconnect() {
      intersectionCallbacks = intersectionCallbacks.filter((item) => item !== this.entry);
    }
  }
  vi.stubGlobal("IntersectionObserver", DrivenIntersectionObserver);
  scrollport = installScrollport() as Scrollport & { restore: () => void };
});

afterEach(() => {
  scrollport.restore();
  vi.unstubAllGlobals();
});

/**
 * Waits until every mounted row sits exactly its own height below the previous
 * one — which is the observable form of "the virtualizer measured each row
 * instead of assuming the estimate".
 */
/** Re-applies the current scroll against the current geometry, as a reflow does. */
function reflow() {
  scrollport.scrollTo(scrollport.top());
}

/**
 * Drives measurement/reflow passes until `check` holds.
 *
 * A bounded loop rather than a nested waitFor (which deadlocks against the
 * outer one) and rather than a sleep (which would make the test a race): each
 * pass is one round of "the browser measured, then re-laid-out", and the loop
 * stops the moment the assertion holds.
 */
/**
 * Scrolls to the end and keeps settling until the offset really is the tail.
 *
 * One pass is not enough: the last rows are still estimates when the scroll
 * lands, so measuring them grows the canvas and the previous "bottom" is no
 * longer the bottom — exactly the reflow the #788 tail-lock exists for.
 */
async function scrollToTail() {
  scrollport.scrollTo(Number.MAX_SAFE_INTEGER);
  await settleUntil(() => {
    scrollport.scrollTo(Number.MAX_SAFE_INTEGER);
    expect(scrollport.top()).toBe(Math.max(0, scrollport.height() - VIEWPORT_PX));
  });
}

async function settleUntil(check: () => void, passes = 12) {
  for (let pass = 0; pass < passes; pass += 1) {
    act(() => flushResizeObservers());
    reflow();
    await act(async () => {
      await Promise.resolve();
    });
    try {
      check();
      return;
    } catch {
      // Not settled yet; another pass.
    }
  }
  check();
}

async function settleLayout() {
  await waitFor(() => {
    // A real browser reports each row's box through a ResizeObserver, which is
    // how the virtualizer learns a height it could not measure inline (it skips
    // inline measurement while a scroll is in flight). jsdom fires none on its
    // own, so the suite's own driver plays that part.
    act(() => flushResizeObservers());
    const rows = mountedRows();
    expect(rows.length).toBeGreaterThan(2);
    // The interior of the window, not its edge: the row that just entered the
    // window reports its height on the render after the one that mounted it, so
    // the boundary is legitimately one pass behind at any given moment.
    for (let i = 1; i < rows.length - 1; i += 1) {
      expect(rows[i].start - rows[i - 1].start).toBe(rows[i - 1].element.offsetHeight);
    }
  });
}

async function openVirtualizedChannel(options: { unreadCount?: number; entry?: string } = {}) {
  const result = renderChannel(options);
  await screen.findByTestId("chat-virtual-canvas");
  await waitFor(() => expect(scrollport.mountedBubbles()).toBeGreaterThan(0));
  await settleLayout();
  return result;
}

// ── The matrix ────────────────────────────────────────────────────────────────

// Each case drives a real ~90-row timeline through repeated measure/reflow
// passes; alone that is well under a second, but under the full suite's
// parallel workers it does not fit the 5s default.
describe("a virtualized timeline", { timeout: 30_000 }, () => {
  it("keeps only a window mounted while measuring every row at its own height", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });

    await openVirtualizedChannel();

    expect(scrollport.mountedBubbles()).toBeLessThan(HISTORY_SIZE / 2);
    // settleLayout() already proved each row sits at its own measured height;
    // this is the other half — those heights genuinely differ, so nothing here
    // would survive a fixed-height list.
    const starts = mountedRows().map((row) => row.start);
    const gaps = new Set(starts.slice(1).map((start, i) => start - starts[i]));
    expect(gaps.size).toBeGreaterThan(1);
  });

  it("grows the scrollable height when a row becomes taller, and keeps the reader where they were", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });
    await openVirtualizedChannel();
    scrollport.scrollTo(1200);
    const before = { total: scrollport.height(), reading: scrollport.topmostMessageId() };
    expect(before.reading).not.toBeNull();

    // An attachment finishing its layout, which is what remeasure exists for.
    // The row the reader is looking at, not one above the fold: an attachment
    // finishing its layout in view is the case that must not move them.
    scrollport.grow(before.reading!, 480);
    await settleLayout();

    // The timeline knows it is taller, and the reader is still on the same
    // message — the row grew, the viewport did not jump to follow it.
    await waitFor(() => expect(scrollport.height()).toBeGreaterThan(before.total));
    expect(scrollport.topmostMessageId()).toBe(before.reading);
  });

  it("keeps the message being read at the same offset when a page is prepended", async () => {
    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: history(), nextCursor: "older" })
      .mockResolvedValueOnce({ messages: history(20, -20), nextCursor: "" });
    await openVirtualizedChannel();

    // The top sentinel is what asks for the previous page, so reaching the top
    // is how a reader triggers it. What must not move is the message they are
    // looking at — here, the one at the top edge.
    scrollport.scrollTo(0);
    const heightBefore = scrollport.height();
    const anchorId = scrollport.topmostMessageId();
    expect(anchorId).toBe("m-0");
    const offsetBefore = offsetOfMessage(anchorId!);
    expect(offsetBefore).not.toBeNull();

    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(scrollport.height()).toBeGreaterThan(heightBefore));
    // The prepended rows enter as estimates and are then measured one by one;
    // settling is what puts the restoration through that whole cycle rather
    // than reading it half-way.
    await settleUntil(() => expect(scrollport.top()).toBeGreaterThan(0));
    await settleLayout();

    // Still mounted — it was not discarded with the page that arrived above it
    // — and still at the very same offset, after the real heights landed.
    const offsetAfter = offsetOfMessage(anchorId!);
    expect(offsetAfter).not.toBeNull();
    expect(Math.abs(offsetAfter! - offsetBefore!)).toBeLessThanOrEqual(PREPEND_ANCHOR_TOLERANCE_PX);
    // And it is genuinely the anchor that was kept, not merely some message:
    // the page inserted above starts at m--20, and none of it is at the edge.
    expect(scrollport.topmostMessageId()).toBe(anchorId);
  });

  // ── The resize matrix ───────────────────────────────────────────────────────
  //
  // What a row changing height does to the reading position, by where that row
  // sits relative to it. Every assertion is in pixels on a named message —
  // "which id is topmost" would pass with the viewport off by most of a row.

  it("follows a row that is entirely above the reader when it grows", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });
    await openVirtualizedChannel();
    scrollport.scrollTo(1500);
    await settleLayout();
    const reading = scrollport.topmostMessageId()!;
    const readingOffset = offsetOfMessage(reading)!;
    // A row well above the fold, mounted in the overscan so it is measured.
    const above = mountedRows()
      .map((row) => row.element.querySelector<HTMLElement>("[data-message-id]")?.dataset.messageId)
      .find((id) => id !== undefined && messageOrdinal(id) < messageOrdinal(reading) - 1)!;
    const grewBy = 100;
    scrollport.grow(above, heightFor(messageOrdinal(above)) + grewBy);
    await settleLayout();

    // Everything it changed is already scrolled past, so the offset follows it
    // and the reader does not move.
    expect(scrollport.topmostMessageId()).toBe(reading);
    expect(Math.abs(offsetOfMessage(reading)! - readingOffset)).toBeLessThanOrEqual(
      PREPEND_ANCHOR_TOLERANCE_PX,
    );
  });

  it("does not jump when the row the reader is looking at grows downward", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });
    await openVirtualizedChannel();
    scrollport.scrollTo(1500);
    await settleLayout();
    const reading = scrollport.topmostMessageId()!;
    const readingOffset = offsetOfMessage(reading)!;
    const before = scrollport.top();

    // The row that crosses the top edge — an attachment in it finishing its
    // layout. It grows *below* the reader's eyes.
    scrollport.grow(reading, heightFor(messageOrdinal(reading)) + 200);
    await settleLayout();

    // The reader stays on it, and the viewport is not dragged by the 200px of
    // content they cannot see. Compensating here is what a "starts above the
    // reader" rule gets wrong.
    expect(scrollport.topmostMessageId()).toBe(reading);
    expect(Math.abs(offsetOfMessage(reading)! - readingOffset)).toBeLessThanOrEqual(
      PREPEND_ANCHOR_TOLERANCE_PX,
    );
    expect(Math.abs(scrollport.top() - before)).toBeLessThanOrEqual(PREPEND_ANCHOR_TOLERANCE_PX);
  });

  it("ignores a row below the reader growing", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });
    await openVirtualizedChannel();
    scrollport.scrollTo(1500);
    await settleLayout();
    const reading = scrollport.topmostMessageId()!;
    const readingOffset = offsetOfMessage(reading)!;
    const before = scrollport.top();
    const below = mountedRows()
      .map((row) => row.element.querySelector<HTMLElement>("[data-message-id]")?.dataset.messageId)
      .find((id) => id !== undefined && messageOrdinal(id) > messageOrdinal(reading) + 1)!;

    scrollport.grow(below, heightFor(messageOrdinal(below)) + 300);
    await settleLayout();

    // Nothing above the reader changed, so nothing about their position may.
    expect(scrollport.top()).toBe(before);
    expect(offsetOfMessage(reading)).toBe(readingOffset);
  });

  it("keeps the anchor through a row that is remeasured after the prepend", async () => {
    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: history(), nextCursor: "older" })
      .mockResolvedValueOnce({ messages: history(20, -20), nextCursor: "" });
    await openVirtualizedChannel();

    scrollport.scrollTo(0);
    const anchorId = scrollport.topmostMessageId()!;
    const offsetBefore = offsetOfMessage(anchorId)!;

    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    await settleUntil(() => expect(scrollport.top()).toBeGreaterThan(0));
    await settleLayout();

    // An attachment in one of the just-prepended rows finishing its layout,
    // well after the restoration decided it was done. The virtualizer owns
    // this correction; what is asserted is that it and the restoration did not
    // both act, which is what used to move the reader.
    scrollport.grow("m--3", 520);
    await settleLayout();

    // Same message, same place — the identity is the contract, and the pixels
    // are what prove it was not merely some row landing on that coordinate.
    expect(offsetOfMessage(anchorId)).not.toBeNull();
    expect(scrollport.topmostMessageId()).toBe(anchorId);
    expect(Math.abs(offsetOfMessage(anchorId)! - offsetBefore)).toBeLessThanOrEqual(
      PREPEND_ANCHOR_TOLERANCE_PX,
    );
  });

  it("hands the scrollport back as soon as the prepend is restored", async () => {
    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: history(), nextCursor: "older" })
      .mockResolvedValueOnce({ messages: history(20, -20), nextCursor: "" });
    await openVirtualizedChannel();

    scrollport.scrollTo(0);
    const anchorId = scrollport.topmostMessageId();
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    await settleUntil(() => expect(scrollport.top()).toBeGreaterThan(0));
    await settleLayout();

    // The reader scrolls somewhere else. A restoration still armed would pull
    // them back to the anchor on the next commit — which is exactly how it used
    // to swallow the trip back to the top sentinel.
    const chosen = Math.round(scrollport.height() / 2);
    scrollport.scrollTo(chosen);
    expect(scrollport.topmostMessageId()).not.toBe(anchorId);
    await settleLayout();

    // They are still where they went. Not asserted to the pixel: jumping into
    // rows nobody has visited lands on estimates, and replacing those with real
    // measurements legitimately shifts the offset by part of a row — what must
    // not happen is being pulled back to the anchor, screens away.
    expect(Math.abs(scrollport.top() - chosen)).toBeLessThan(VIEWPORT_PX);
    expect(scrollport.topmostMessageId()).not.toBe(anchorId);
  });

  it("gives up the restoration when the scrollport cannot move at all", async () => {
    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: history(), nextCursor: "older" })
      .mockResolvedValueOnce({ messages: history(20, -20), nextCursor: "" });
    await openVirtualizedChannel();

    scrollport.scrollTo(0);
    // A physical limit for the whole of the prepend: every write the
    // restoration attempts lands on a scrollport that will not take it, so no
    // write has a scroll event behind it and no pass has a successor. Staying
    // armed here is what used to take the virtualizer's own compensation down
    // with it for the rest of the conversation.
    scrollport.setImmovable(true);
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    await settleLayout();
    scrollport.setImmovable(false);

    // Observable evidence that it let go: the reader's next scroll is honoured
    // and is not undone on the following commit.
    const chosen = Math.round(scrollport.height() / 2);
    scrollport.scrollTo(chosen);
    const landedOn = scrollport.topmostMessageId();
    await settleLayout();

    expect(Math.abs(scrollport.top() - chosen)).toBeLessThan(VIEWPORT_PX);
    expect(scrollport.topmostMessageId()).toBe(landedOn);
  });

  it("gives up the restoration when the reader asks for the end instead", async () => {
    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: history(), nextCursor: "older" })
      .mockResolvedValueOnce({ messages: history(20, -20), nextCursor: "" });
    await openVirtualizedChannel();

    scrollport.scrollTo(0);
    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));

    // An explicit "take me to the end" while the page is still settling: two
    // writers pulling opposite ways would leave the reader at neither.
    await act(async () => {
      screen.getByRole("button", { name: /Ir para o final/i }).click();
    });
    await settleUntil(() =>
      expect(scrollport.top()).toBe(Math.max(0, scrollport.height() - VIEWPORT_PX)),
    );

    expect(scrollport.top()).toBe(Math.max(0, scrollport.height() - VIEWPORT_PX));
  });

  it("opens on the first unread message, with its divider, rather than at the end", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });

    await openVirtualizedChannel({ unreadCount: 12 });

    const divider = await screen.findByRole("separator", { name: "Novas mensagens" });
    expect(divider).toBeInTheDocument();
    // Not at the tail: an unread boundary twelve messages back is well above it.
    expect(scrollport.top()).toBeLessThan(scrollport.height() - VIEWPORT_PX);
  });

  it("restores a saved reading position instead of opening at the end", async () => {
    const anchorId = "m-30";
    saveViewportAnchor(CURRENT_USER, "channel", "geral", {
      atBottom: false,
      anchorMessageId: anchorId,
      anchorOffsetPx: 0,
      savedAt: Date.now(),
    });
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });

    await openVirtualizedChannel();

    await waitFor(() => expect(screen.getByText("Mensagem 30")).toBeInTheDocument());
    expect(scrollport.top()).toBeGreaterThan(0);
    expect(scrollport.top()).toBeLessThan(scrollport.height() - VIEWPORT_PX);
  });

  it("jumps to a quoted message that was never mounted, and highlights it", async () => {
    const quoted = message(2);
    const quoting = message(HISTORY_SIZE, {
      id: "m-quoting",
      quoted: {
        id: quoted.id,
        authorId: quoted.senderId,
        bodyText: quoted.bodyText,
        bodyFormat: "v1",
        isRemoved: false,
      },
    } as Partial<Message>);
    mockFetchChannelMessages.mockResolvedValue({
      messages: [...history(), quoting],
      nextCursor: "",
    });
    await openVirtualizedChannel();
    await scrollToTail();
    // The quoted message is genuinely not mounted from down here. Asserted on
    // the row itself, not on its text — the quote excerpt inside the quoting
    // message repeats that text and would make the check vacuous.
    expect(document.querySelector(`[data-message-id="${quoted.id}"]`)).toBeNull();

    const jump = await screen.findByRole("button", { name: /Ir para mensagem original/ });
    await act(async () => {
      jump.click();
    });
    // The row was far outside the mounted window; asking for it logically is
    // what brings it back, since messageRefs never held it.
    await settleUntil(() => {
      expect(document.querySelector(`[data-message-id="${quoted.id}"]`)).not.toBeNull();
    });
  });

  it("reaches the real tail from a reading position when Ir para o final is pressed", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });
    await openVirtualizedChannel();
    scrollport.scrollTo(600);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Ir para o final/i })).toBeInTheDocument(),
    );

    await act(async () => {
      screen.getByRole("button", { name: /Ir para o final/i }).click();
    });

    await waitFor(() =>
      expect(scrollport.top()).toBeGreaterThanOrEqual(scrollport.height() - VIEWPORT_PX - 1),
    );
    await waitFor(() =>
      expect(screen.getByText(`Mensagem ${HISTORY_SIZE - 1}`)).toBeInTheDocument(),
    );
  });

  it("follows a realtime message while the reader is at the end", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });
    await openVirtualizedChannel();
    await scrollToTail();

    act(() => {
      wsState.onMessageCreated?.(messageCreated("m-new", "Chegou agora"));
    });
    await settleUntil(() => {
      expect(screen.getByText("Chegou agora")).toBeInTheDocument();
    });
  });

  it("never pulls a reader who is up in the history when a realtime message arrives", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });
    await openVirtualizedChannel();
    scrollport.scrollTo(700);
    const reading = scrollport.topmostMessageId();
    const before = scrollport.top();

    act(() => {
      wsState.onMessageCreated?.(messageCreated("m-new", "Não me puxe"));
    });

    await waitFor(() => expect(scrollport.height()).toBeGreaterThan(0));
    expect(scrollport.top()).toBe(before);
    expect(scrollport.topmostMessageId()).toBe(reading);
  });

  it("brings focus back to the timeline when the row holding it goes away", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });
    await openVirtualizedChannel();

    const focusable = mountedRows()[0].element.querySelector<HTMLElement>("[data-message-id]")!;
    focusable.setAttribute("tabindex", "-1");
    act(() => focusable.focus());
    expect(focusable).toHaveFocus();

    // Removing a focused element sends focus to <body> and fires focusout —
    // the browser behaviour jsdom does not reproduce on removal, so it is
    // played here. That focus really is lost when a row unmounts mid-scroll is
    // what the Playwright spec proves in a real browser.
    act(() => {
      focusable.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
      focusable.remove();
    });
    await waitFor(() => expect(listElement()).toHaveFocus());
  });

  // ── #492 above the threshold ────────────────────────────────────────────────
  //
  // The four resolutions #492 defines all used to end in a DOM lookup over a
  // fully mounted history. Above VIRTUALIZE_MIN_ROWS that lookup answers null
  // for anything outside the window, so each of them needs its own proof here —
  // the versions below the threshold prove nothing about this branch.

  it("returns to the real end when the reader sends a message from up in the history", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });
    mockPostChannelMessage.mockResolvedValue(
      message(HISTORY_SIZE, { id: "m-own", senderId: CURRENT_USER, bodyText: "Minha resposta" }),
    );
    await openVirtualizedChannel();
    // Up in the history, far from the tail: the position an own-send has to
    // abandon on purpose (#492 item 21).
    scrollport.scrollTo(400);
    await waitFor(() => expect(scrollport.top()).toBeLessThan(scrollport.height() - VIEWPORT_PX));

    const input = await screen.findByTestId("chat-composer-input");
    fireEvent.paste(input, {
      clipboardData: {
        getData: (type: string) => (type === "text/plain" ? "Minha resposta" : ""),
        types: ["text/plain"],
        files: [],
      },
    });
    await waitFor(() => expect(input).toHaveTextContent("Minha resposta"));
    await act(async () => {
      screen.getByTestId("chat-send-btn").click();
    });

    await waitFor(() => expect(mockPostChannelMessage).toHaveBeenCalledTimes(1));
    // The real tail, confirmed by the geometry rather than by the call
    // returning: the message is the last row and the scrollport is at the end.
    await settleUntil(() => {
      expect(screen.getByText("Minha resposta")).toBeInTheDocument();
      expect(scrollport.top()).toBe(Math.max(0, scrollport.height() - VIEWPORT_PX));
    });

    // And a row settling its height afterwards does not leave the reader short
    // of the end — the #788 tail-lock still owns this, virtualized or not.
    scrollport.grow("m-own", 300);
    await settleLayout();
    await settleUntil(() =>
      expect(scrollport.top()).toBe(Math.max(0, scrollport.height() - VIEWPORT_PX)),
    );
  });

  it("opens on a deep-linked message that is nowhere near the initial window", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });

    // m-3 is at the very top of ninety rows; the conversation opens at the
    // tail, so it is not mounted when the deep link is resolved and
    // messageRefs cannot answer for it.
    await openVirtualizedChannel({ entry: "/chat/channel/geral?message=m-3" });

    await settleUntil(() => {
      expect(document.querySelector('[data-message-id="m-3"]')).not.toBeNull();
    });
    // Positioned, not merely mounted: the row's box really overlaps the
    // viewport — the jump asks for it centred, so its top may legitimately sit
    // a little above the edge — and the reader is nowhere near the end they
    // would otherwise have opened at.
    const box = document
      .querySelector<HTMLElement>('[data-message-id="m-3"]')!
      .getBoundingClientRect();
    const listTop = listElement().getBoundingClientRect().top;
    expect(box.top - listTop).toBeLessThan(VIEWPORT_PX);
    expect(box.bottom - listTop).toBeGreaterThan(0);
    expect(scrollport.top()).toBeLessThan(scrollport.height() - VIEWPORT_PX);
    // And the highlight the deep link exists to draw really is on it.
    await waitFor(() =>
      expect(
        document
          .querySelector('[data-message-id="m-3"]')
          ?.closest(".chat-msg-area__msg--highlight"),
      ).not.toBeNull(),
    );
  });

  it("opens on a first unread that sits far above the initial window", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: history(), nextCursor: "" });

    // Almost everything unread: the boundary is m-5, near the top of the
    // timeline and many screens above the tail the conversation would
    // otherwise open at.
    await openVirtualizedChannel({ unreadCount: HISTORY_SIZE - 5 });

    const divider = await screen.findByRole("separator", { name: "Novas mensagens" });
    expect(divider).toBeInTheDocument();
    await settleUntil(() => {
      expect(document.querySelector('[data-message-id="m-5"]')).not.toBeNull();
    });
    // The separator is what the reader lands on, with the first unread message
    // right below it — not the tail, and not the top of the conversation.
    const dividerRow = divider.closest<HTMLElement>("[data-index]");
    expect(dividerRow).not.toBeNull();
    expect(rowStart(dividerRow!) - scrollport.top()).toBeGreaterThanOrEqual(0);
    expect(rowStart(dividerRow!) - scrollport.top()).toBeLessThan(VIEWPORT_PX);
    expect(scrollport.top()).toBeLessThan(scrollport.height() - VIEWPORT_PX);
  });

  it("keeps date, unread and system rows inside the virtual model", async () => {
    const messages = history();
    messages[5] = message(5, {
      id: "m-5",
      kind: "system",
      eventType: "conversation_member_left",
      senderDisplayName: "Ana",
    } as Partial<Message>);
    mockFetchChannelMessages.mockResolvedValue({ messages, nextCursor: "" });

    await openVirtualizedChannel({ unreadCount: HISTORY_SIZE });

    const host = canvas()!;
    expect(host.querySelectorAll(".chat-msg-area__day-divider").length).toBeGreaterThan(0);
    expect(screen.getByRole("separator", { name: "Novas mensagens" })).toBeInTheDocument();
    expect(screen.getByTestId("chat-system-message")).toBeInTheDocument();
  });
});
