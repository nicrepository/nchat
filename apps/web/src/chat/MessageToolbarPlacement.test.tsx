/**
 * Where the hover toolbar sits, and what keeps it there (issue #839).
 *
 * The regression: after the timeline loaded older history and switched to the
 * virtualized branch, the toolbar drifted away from its bubble, because a
 * transformed virtual row became the containing block of a `position: fixed`
 * toolbar placed in viewport coordinates. jsdom lays nothing out, so the
 * coordinate-space half of that is proven in a browser (e2e); what this file
 * pins down is everything the placement promises regardless of which branch
 * renders the row: the bubble's *current* box is the only input, the healthy
 * geometry (issue #331) is reproduced, an anchor that left the reader's band
 * closes the toolbar, and the toolbar keeps its place in the message's DOM so
 * hover, focus and Tab still treat it as part of the message.
 */

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { emptyEmojiUsage } from "./emoji/emojiUsage";
import MessageBubble from "./MessageBubble";
import type { MessageBubbleProps } from "./MessageBubble";
import type { Message } from "./chatTypes";
import { useReactionMenu } from "./message-area/timeline/useReactionMenu";

const MENU = { width: 246, height: 36 };
/** The distance the toolbar keeps from the bubble (MessageToolbar's toolbarGap). */
const GAP = 6;
/** What the toolbar's top edge sits above the bubble's when placed above it. */
const ABOVE = MENU.height + GAP;
/** The list's band on screen: inside a 1024×768 window, below a header. */
const LIST = { top: 60, bottom: 700, left: 200, right: 1000 };
/** Distance kept from the band's edges (useAnchoredPicker's viewportPadding). */
const PADDING = 8;

function rect(over: Partial<DOMRect>): DOMRect {
  const base = { top: 0, bottom: 0, left: 0, right: 0, width: 0, height: 0 };
  const r = { ...base, ...over };
  return { ...r, x: r.left, y: r.top, toJSON: () => ({}) } as DOMRect;
}

function box(top: number, left: number, width: number, height = 40): DOMRect {
  return rect({ top, bottom: top + height, left, right: left + width, width, height });
}

/** The geometry the browser would report, owned by each test. */
const layout = { bubble: box(300, 313, 511), menu: MENU };

function messageWith(overrides: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    senderId: "user-1",
    senderDisplayName: "Alex Souza",
    senderEmail: "alex@example.test",
    kind: "user",
    bodyText: "olá",
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    linkSafetyState: "",
    deletedAt: null,
    createdAt: "2026-08-17T12:00:00Z",
    updatedAt: "2026-08-17T12:00:00Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    ...overrides,
  };
}

function propsWith(overrides: Partial<MessageBubbleProps> = {}): MessageBubbleProps {
  return {
    message: messageWith(),
    isMine: false,
    onToggleReaction: vi.fn(),
    onReplyMessage: vi.fn(),
    onReferenceMessage: vi.fn(),
    onToggleFavorite: vi.fn(),
    onEditMessage: vi.fn(),
    onEditForbidden: vi.fn(),
    onDeleteMessage: vi.fn(),
    recentReactionEmojis: ["👍"],
    emojiUsage: emptyEmojiUsage,
    onEmojiToneChange: vi.fn(),
    currentUserId: "me",
    reactionMenuVisible: true,
    onReactionMenuVisibleChange: vi.fn(),
    pickerOpen: false,
    onPickerOpenChange: vi.fn(),
    ...overrides,
  };
}

/** The bubble inside the list it would scroll in, as the timeline renders it. */
function Timeline({ nodeKey, ...props }: MessageBubbleProps & { nodeKey?: string }) {
  return (
    <div className="chat-msg-area__list">
      {/* A new key is a new DOM node for the same message: what a virtual row
          remounting looks like from the bubble's point of view. */}
      <MessageBubble key={nodeKey} {...props} />
    </div>
  );
}

function renderToolbar(overrides: Partial<MessageBubbleProps> = {}) {
  const props = propsWith(overrides);
  const view = render(<Timeline {...props} />);
  return {
    ...view,
    props,
    rerender: (next: Partial<MessageBubbleProps> = {}, nodeKey?: string) =>
      view.rerender(<Timeline {...props} {...next} nodeKey={nodeKey} />),
  };
}

const toolbar = () => screen.getByRole("toolbar", { name: "Reagir à mensagem" });
/** The toolbar once it is hidden, which takes it out of the accessibility tree. */
const hiddenToolbar = () => document.querySelector('[role="toolbar"]');

/** The placed toolbar is wholly above or wholly below the bubble, gap included. */
function expectClearOfBubble() {
  const top = Number.parseFloat(toolbar().style.top);
  const bottom = top + MENU.height;
  const above = bottom <= layout.bubble.top - GAP;
  const below = top >= layout.bubble.bottom + GAP;
  expect(above || below).toBe(true);
}
const shell = () => screen.getByTestId("chat-msg-bubble");

beforeEach(() => {
  layout.bubble = box(300, 313, 511);
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    if (this.classList.contains("chat-msg-area__list")) return rect(LIST);
    if (this.classList.contains("chat-msg-area__msg-bubble")) return layout.bubble;
    if (this.getAttribute("role") === "toolbar") return box(0, 0, MENU.width, MENU.height);
    // The picker's own anchor, somewhere inside the window.
    if (this.getAttribute("aria-label") === "Mais reações") return box(300, 500, 30, 30);
    return rect({});
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("reaction toolbar placement", () => {
  // The healthy geometry: a small gap above the top edge of the bubble,
  // starting at its horizontal middle.
  it("sits a small gap above a received message, from the bubble's middle", () => {
    renderToolbar();
    expect(toolbar()).toHaveStyle({
      top: `${300 - ABOVE}px`,
      left: "568.5px",
      visibility: "visible",
    });
    const toolbarBottom = Number.parseFloat(toolbar().style.top) + MENU.height;
    expect(300 - toolbarBottom).toBeGreaterThan(0);
    expectClearOfBubble();
  });

  // Mirrored for the reader's own message: it ends at the bubble's middle, and
  // a toolbar wider than a short bubble simply extends to the left of it —
  // never squeezed to the bubble's width, never centred on it.
  it("ends at the middle of the reader's own short message, keeping its own width", () => {
    layout.bubble = box(300, 900, 80);
    renderToolbar({ isMine: true });
    expect(toolbar()).toHaveStyle({ top: `${300 - ABOVE}px`, left: `${940 - MENU.width}px` });
  });

  it("stays whole inside the list's band at either side", () => {
    layout.bubble = box(300, 960, 100);
    const view = renderToolbar();
    expect(toolbar()).toHaveStyle({ left: `${LIST.right - MENU.width - PADDING}px` });

    layout.bubble = box(300, 150, 100);
    view.rerender({ isMine: true });
    expect(toolbar()).toHaveStyle({ left: `${LIST.left + PADDING}px` });
  });

  // The room that counts is the list's, not the window's: above the list is
  // the header, and a toolbar drawn there would be drawn over it.
  it("goes below the bubble only when there is no room above it inside the list", () => {
    layout.bubble = box(LIST.top + PADDING + ABOVE, 313, 511);
    const view = renderToolbar();
    // Exactly the padding from the list's top edge still counts as fitting.
    expect(toolbar()).toHaveStyle({ top: `${LIST.top + PADDING}px` });

    // 70 - 42 = 28: room in the window, none in the list.
    layout.bubble = box(70, 313, 511);
    view.rerender();
    expect(toolbar()).toHaveStyle({ top: `${70 + 40 + GAP}px` });
    expect(Number.parseFloat(toolbar().style.top)).toBeGreaterThanOrEqual(LIST.top);
    expectClearOfBubble();
  });

  // A bubble spanning the list: no room above, none below. A toolbar clamped
  // into the band would lie across the message it acts on, so there is none.
  it("closes rather than cross a bubble that fits the toolbar on neither side", () => {
    layout.bubble = box(70, 313, 511, 620);
    const { props } = renderToolbar();
    expect(hiddenToolbar()).toHaveStyle({ visibility: "hidden" });
    expect((hiddenToolbar() as HTMLElement).style.top).toBe("");
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
  });

  // A prepend, a remeasure or a resize re-renders the row: whatever the reason,
  // the commit re-reads the bubble and nothing from the previous placement
  // survives it.
  it("re-reads the bubble's box on every commit", () => {
    const view = renderToolbar();
    layout.bubble = box(500, 313, 511);
    view.rerender();
    expect(toolbar()).toHaveStyle({ top: `${500 - ABOVE}px` });
  });

  it("follows the bubble while the list scrolls and when the window resizes", () => {
    renderToolbar();
    layout.bubble = box(420, 313, 511);
    fireEvent.scroll(shell().parentElement!);
    expect(toolbar()).toHaveStyle({ top: `${420 - ABOVE}px` });

    layout.bubble = box(120, 313, 511);
    fireEvent(window, new Event("resize"));
    expect(toolbar()).toHaveStyle({ top: `${120 - ABOVE}px` });
  });

  it("places a remounted bubble by its new box, not the old one", () => {
    const first = renderToolbar();
    expect(toolbar()).toHaveStyle({ top: `${300 - ABOVE}px` });
    first.unmount();

    layout.bubble = box(600, 313, 511);
    renderToolbar();
    expect(toolbar()).toHaveStyle({ top: `${600 - ABOVE}px` });
  });

  // The commit itself must refuse an anchor outside the band — not a scroll
  // or resize that may never come. A virtual row can be remounted well past
  // the list's edge while the message is still the hovered one, and nothing
  // moves between that commit and the placement.
  it("stays hidden on a commit whose anchor is already outside the band", () => {
    layout.bubble = box(710, 313, 511);
    const { props } = renderToolbar();
    expect(hiddenToolbar()).toHaveStyle({ visibility: "hidden" });
    expect((hiddenToolbar() as HTMLElement).style.top).toBe("");
    expect((hiddenToolbar() as HTMLElement).style.left).toBe("");
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
  });

  // The same logical message, a new DOM node past the edge: the message being
  // the hovered one does not vouch for the node that carries it now.
  it("closes when the same message is remounted on a node outside the band", () => {
    const { props, rerender } = renderToolbar();
    const before = shell();
    expect(toolbar()).toHaveStyle({ visibility: "visible" });
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);

    layout.bubble = box(710, 313, 511);
    rerender({}, "remounted");
    expect(shell()).not.toBe(before);
    expect(hiddenToolbar()).toHaveStyle({ visibility: "hidden" });
    expect((hiddenToolbar() as HTMLElement).style.top).toBe("");
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });

  // The band is the window ∩ the list: a bubble scrolled under the header is
  // still inside the window, and still gone.
  it("closes when the bubble scrolls out of the list's band", () => {
    const { props } = renderToolbar();
    layout.bubble = box(10, 313, 511);
    fireEvent.scroll(shell().parentElement!);
    expect(hiddenToolbar()).toHaveStyle({ visibility: "hidden" });
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
  });

  it("closes when a resize leaves the bubble outside the window", () => {
    const { props } = renderToolbar();
    layout.bubble = box(900, 313, 511);
    fireEvent(window, new Event("resize"));
    expect(hiddenToolbar()).toHaveStyle({ visibility: "hidden" });
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });

  it("listens only while open, and lets go on close and on unmount", () => {
    const docAdd = vi.spyOn(document, "addEventListener");
    const docRemove = vi.spyOn(document, "removeEventListener");
    const winAdd = vi.spyOn(window, "addEventListener");
    const winRemove = vi.spyOn(window, "removeEventListener");
    const scrollListener = () => docAdd.mock.calls.find(([type]) => type === "scroll")?.[1];
    const resizeListener = () => winAdd.mock.calls.find(([type]) => type === "resize")?.[1];

    const view = renderToolbar();
    expect(scrollListener()).toBeDefined();
    expect(resizeListener()).toBeDefined();

    view.rerender({ reactionMenuVisible: false });
    expect(docRemove).toHaveBeenCalledWith("scroll", scrollListener(), true);
    expect(winRemove).toHaveBeenCalledWith("resize", resizeListener());

    docAdd.mockClear();
    winAdd.mockClear();
    view.rerender({ reactionMenuVisible: true });
    view.unmount();
    expect(docRemove).toHaveBeenCalledWith("scroll", scrollListener(), true);
    expect(winRemove).toHaveBeenCalledWith("resize", resizeListener());
  });

  // The top layer is what frees the toolbar from a transformed virtual row
  // (see the CSS). jsdom has no popover API, so the browser's part is played
  // here, and the module is loaded fresh so it can notice.
  it("puts the toolbar in the top layer where the browser can", async () => {
    const togglePopover = vi.fn();
    Object.defineProperty(HTMLElement.prototype, "togglePopover", {
      configurable: true,
      value: togglePopover,
    });
    vi.resetModules();
    try {
      const { default: FreshBubble } = await import("./MessageBubble");
      render(
        <div className="chat-msg-area__list">
          <FreshBubble {...propsWith()} />
        </div>,
      );
      const element = document.querySelector('[role="toolbar"]');
      expect(element).toHaveAttribute("popover", "manual");
      expect(togglePopover).toHaveBeenCalledWith(true);
    } finally {
      delete (HTMLElement.prototype as { togglePopover?: unknown }).togglePopover;
      vi.resetModules();
    }
  });
});

/**
 * The bubble wired to the conversation's real menu state, so what is asserted
 * is what a reader sees — the toolbar staying or going — and not which callback
 * fired: a pointer simulation may well emit a leave on the way to a descendant,
 * and the grace period exists for exactly that trip.
 */
function OwnedBubble(props: MessageBubbleProps) {
  const menu = useReactionMenu();
  return (
    <div className="chat-msg-area__list">
      <MessageBubble
        {...props}
        reactionMenuVisible={menu.hoveredMessageId === props.message.id}
        onReactionMenuVisibleChange={menu.onReactionMenuVisibleChange}
        pickerOpen={menu.openPickerMessageId === props.message.id}
        onPickerOpenChange={menu.onPickerOpenChange}
      />
    </div>
  );
}

const queryToolbar = () => screen.queryByRole("toolbar", { name: "Reagir à mensagem" });

describe("reaction toolbar ownership", () => {
  // The toolbar is a descendant of the message, so a pointer crossing from the
  // bubble to it — and back — never leaves the message.
  it("keeps the toolbar while the pointer crosses between bubble and toolbar", async () => {
    const user = userEvent.setup();
    render(<OwnedBubble {...propsWith()} />);

    await user.hover(shell());
    expect(queryToolbar()).toBeInTheDocument();
    await user.hover(screen.getByRole("button", { name: "Responder" }));
    await user.hover(screen.getByText("olá"));
    await user.hover(screen.getByRole("button", { name: "Mais reações" }));
    expect(queryToolbar()).toBeInTheDocument();

    await user.unhover(shell());
    await waitFor(() => expect(queryToolbar()).not.toBeInTheDocument());
  });

  it("reaches the toolbar with Tab from the focused message, and hides it on leaving", async () => {
    const user = userEvent.setup();
    render(<OwnedBubble {...propsWith()} />);

    act(() => shell().focus());
    expect(queryToolbar()).toBeInTheDocument();

    await user.tab();
    expect(screen.getByRole("button", { name: "Reagir rapidamente com 👍" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "Responder" })).toHaveFocus();
    expect(queryToolbar()).toBeInTheDocument();

    await user.tab({ shift: true });
    await user.tab({ shift: true });
    expect(shell()).toHaveFocus();
    await user.tab({ shift: true });
    expect(shell()).not.toHaveFocus();
    await waitFor(() => expect(queryToolbar()).not.toBeInTheDocument());
  });

  it("keeps the toolbar while its picker is open, and hands focus back on Escape", async () => {
    const user = userEvent.setup();
    render(<OwnedBubble {...propsWith()} />);

    await user.hover(shell());
    await user.click(screen.getByRole("button", { name: "Mais reações" }));
    expect(screen.getByRole("dialog", { name: "Escolher reação" })).toBeInTheDocument();

    await user.unhover(shell());
    expect(queryToolbar()).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "Escolher reação" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Mais reações" })).toHaveFocus();
  });
});
