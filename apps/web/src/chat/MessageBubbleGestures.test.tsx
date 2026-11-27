/**
 * What a message's touch gestures are allowed to do (issue #1152): a swipe is
 * the existing reply action for the message it started on, revalidated at
 * release; a long press opens the shared menu; and a touch's compatibility
 * events no longer reveal the hover toolbar, while a mouse and keyboard still do.
 */

import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { emptyEmojiUsage } from "./emoji/emojiUsage";
import MessageBubble from "./MessageBubble";
import type { MessageBubbleProps } from "./MessageBubble";
import type { Message } from "./chatTypes";
import { LONG_PRESS_MS, SWIPE_REPLY_PX } from "./useMessageTouchGestures";

function messageWith(overrides: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    senderId: "user-1",
    senderDisplayName: "Alex Souza",
    senderEmail: "alex@example.test",
    kind: "user",
    bodyText: "mensagem para responder",
    bodyFormat: "v1",
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
    reactionMenuVisible: false,
    onReactionMenuVisibleChange: vi.fn(),
    pickerOpen: false,
    onPickerOpenChange: vi.fn(),
    onReactionMenuLongPress: vi.fn(),
    ...overrides,
  };
}

function renderBubble(overrides: Partial<MessageBubbleProps> = {}) {
  const props = propsWith(overrides);
  const view = render(<MessageBubble {...props} />);
  return {
    ...view,
    props,
    rerender: (next: Partial<MessageBubbleProps>) =>
      view.rerender(<MessageBubble {...props} {...next} />),
  };
}

const body = () => screen.getByText("mensagem para responder");
const touch = { pointerType: "touch", pointerId: 1, isPrimary: true };

function swipeStart() {
  fireEvent.pointerDown(body(), { ...touch, clientX: 100, clientY: 200 });
  for (const dx of [20, 50, SWIPE_REPLY_PX + 20]) {
    fireEvent.pointerMove(body(), { ...touch, clientX: 100 + dx, clientY: 202 });
  }
}

/** On the row itself: a removed message no longer shows the text it started on. */
const swipeEnd = () =>
  fireEvent.pointerUp(screen.getByTestId("chat-msg-bubble"), {
    ...touch,
    clientX: 100 + SWIPE_REPLY_PX + 20,
    clientY: 202,
  });

describe("swipe to reply", () => {
  it.each([
    ["a received message", false],
    ["the reader's own message", true],
  ])("calls the existing reply action once for %s", (_label, isMine) => {
    const { props } = renderBubble({ isMine });
    swipeStart();
    swipeEnd();
    expect(props.onReplyMessage).toHaveBeenCalledTimes(1);
    expect(props.onReplyMessage).toHaveBeenCalledWith(props.message);
  });

  it("does not reply to a message removed during the drag", () => {
    const { props, rerender } = renderBubble();
    swipeStart();
    rerender({ message: messageWith({ isRemoved: true }) });
    swipeEnd();
    expect(props.onReplyMessage).not.toHaveBeenCalled();
  });

  it("does not reply to another message a recycled row now shows", () => {
    const { props, rerender } = renderBubble();
    swipeStart();
    rerender({ message: messageWith({ id: "msg-2", bodyText: "mensagem para responder" }) });
    swipeEnd();
    expect(props.onReplyMessage).not.toHaveBeenCalled();
  });

  it("does not reply while a touch menu or picker is open", () => {
    const { props } = renderBubble({ gesturesBlocked: true });
    swipeStart();
    swipeEnd();
    expect(props.onReplyMessage).not.toHaveBeenCalled();
  });

  it("starts no gesture where the timeline wired none", () => {
    const { props } = renderBubble({ onReactionMenuLongPress: undefined });
    swipeStart();
    swipeEnd();
    expect(props.onReplyMessage).not.toHaveBeenCalled();
  });
});

describe("long press and the hover toolbar", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("opens the shared menu for this message", () => {
    const { props } = renderBubble();
    fireEvent.pointerDown(body(), touch);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(props.onReactionMenuLongPress).toHaveBeenCalledWith("msg-1");
  });

  it("ignores the mouseenter and focus a tap fires as compatibility events", () => {
    const { props } = renderBubble();
    const shell = screen.getByTestId("chat-msg-bubble");
    fireEvent.pointerDown(body(), touch);
    fireEvent.pointerUp(body(), touch);
    fireEvent.mouseEnter(shell);
    fireEvent.focus(shell);
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", true);
  });

  it("still reveals on a mouse hover and on keyboard focus", () => {
    const { props } = renderBubble();
    const shell = screen.getByTestId("chat-msg-bubble");
    fireEvent.pointerDown(body(), { ...touch, pointerType: "mouse" });
    fireEvent.mouseEnter(shell);
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", true);
    vi.mocked(props.onReactionMenuVisibleChange).mockClear();
    fireEvent.focus(shell);
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", true);
  });
});

/**
 * CQR R3 of #1238: how a message becomes the selected one on touch, and how it
 * stops being it when it scrolls out of the timeline. The header's half lives
 * in MessageGesturesIntegration; this is the row's.
 */
describe("touch selection", () => {
  let touchDevice = true;
  beforeEach(() => {
    touchDevice = true;
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: touchDevice, media: query }));
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("selects on a tap of the message, on touch only", () => {
    const onMessageSelect = vi.fn();
    renderBubble({ onMessageSelect });
    fireEvent.click(body());
    expect(onMessageSelect).toHaveBeenCalledWith("msg-1");

    onMessageSelect.mockClear();
    touchDevice = false;
    fireEvent.click(body());
    expect(onMessageSelect).not.toHaveBeenCalled();
  });

  it("leaves a tap on a control inside the message to the control", () => {
    const onMessageSelect = vi.fn();
    renderBubble({
      onMessageSelect,
      message: messageWith({
        reactions: [{ emoji: "👍", count: 1, reactedByMe: false, users: [] }],
      }),
    });
    fireEvent.click(screen.getByRole("button", { name: /👍/ }));
    expect(onMessageSelect).not.toHaveBeenCalled();
  });

  it("never selects a removed message", () => {
    const onMessageSelect = vi.fn();
    renderBubble({ onMessageSelect, message: messageWith({ isRemoved: true }) });
    fireEvent.click(screen.getByTestId("chat-msg-bubble"));
    expect(onMessageSelect).not.toHaveBeenCalled();
  });

  it("selects on keyboard focus arriving at the row, never on a programmatic one", () => {
    const onMessageSelect = vi.fn();
    renderBubble({ onMessageSelect });
    const row = screen.getByTestId("chat-msg-bubble");
    const visible = vi.spyOn(row, "matches").mockReturnValue(false);
    act(() => row.focus());
    expect(onMessageSelect).not.toHaveBeenCalled();

    act(() => row.blur());
    visible.mockImplementation((selector) => selector === ":focus-visible");
    act(() => row.focus());
    expect(onMessageSelect).toHaveBeenCalledWith("msg-1");
  });

  it("opens the menu on Enter once selected, and only on the row itself", () => {
    const onReactionMenuLongPress = vi.fn();
    const { rerender } = renderBubble({ onReactionMenuLongPress });
    const row = screen.getByTestId("chat-msg-bubble");
    fireEvent.keyDown(row, { key: "Enter" });
    expect(onReactionMenuLongPress).not.toHaveBeenCalled();

    rerender({ selected: true });
    expect(row).toHaveClass("chat-msg-area__msg--selected");
    fireEvent.keyDown(body(), { key: "Enter" });
    fireEvent.keyDown(row, { key: "Escape" });
    expect(onReactionMenuLongPress).not.toHaveBeenCalled();
    fireEvent.keyDown(row, { key: "Enter" });
    expect(onReactionMenuLongPress).toHaveBeenCalledWith("msg-1");
  });

  it("ends the selection once the message leaves the timeline's view, watching it alone", () => {
    const observers: {
      root: unknown;
      target?: Element;
      disconnected: boolean;
      fire: (seen: boolean) => void;
    }[] = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        record;
        constructor(callback: IntersectionObserverCallback, options: IntersectionObserverInit) {
          this.record = {
            root: options.root,
            target: undefined as Element | undefined,
            disconnected: false,
            fire: (seen: boolean) =>
              callback([{ isIntersecting: seen } as IntersectionObserverEntry], this as never),
          };
          observers.push(this.record);
        }
        observe(target: Element) {
          this.record.target = target;
        }
        disconnect() {
          this.record.disconnected = true;
        }
      },
    );
    const onReactionMenuRelease = vi.fn();
    const props = propsWith({ onReactionMenuRelease });
    const view = render(
      <div className="chat-msg-area__list" data-testid="list">
        <MessageBubble {...props} />
      </div>,
    );
    const rerender = (next: Partial<MessageBubbleProps>) =>
      view.rerender(
        <div className="chat-msg-area__list" data-testid="list">
          <MessageBubble {...props} {...next} />
        </div>,
      );
    expect(observers).toHaveLength(0);

    rerender({ selected: true });
    expect(observers).toHaveLength(1);
    expect(observers[0].root).toBe(screen.getByTestId("list"));
    expect(observers[0].target).toBe(screen.getByTestId("chat-msg-bubble"));
    // In view, then out of it: that ends it. In view is the list's reading and
    // the visual viewport's (CQR R5), so the boxes say where the row is.
    const row = screen.getByTestId("chat-msg-bubble");
    vi.spyOn(screen.getByTestId("list"), "getBoundingClientRect").mockReturnValue(
      DOMRect.fromRect({ x: 0, y: 100, width: 400, height: 500 }),
    );
    vi.spyOn(
      row.querySelector(".chat-msg-area__msg-bubble")!,
      "getBoundingClientRect",
    ).mockReturnValue(DOMRect.fromRect({ x: 0, y: 300, width: 200, height: 40 }));
    act(() => observers[0].fire(true));
    expect(onReactionMenuRelease).not.toHaveBeenCalled();
    act(() => observers[0].fire(false));
    expect(onReactionMenuRelease).toHaveBeenCalledWith("msg-1");

    // CQR R4: focus is no proof of visibility. A row focused programmatically
    // (preventScroll, a menu handing focus back) whose first reading is out of
    // view ends the selection at once.
    onReactionMenuRelease.mockClear();
    rerender({ selected: false });
    act(() => row.focus({ preventScroll: true }));
    rerender({ selected: true });
    act(() => observers.at(-1)!.fire(false));
    expect(onReactionMenuRelease).toHaveBeenCalledWith("msg-1");
    act(() => row.blur());
    rerender({ selected: false });
    observers.length = 0;
    rerender({ selected: true });

    // An open menu has its own verdict: nothing is watched while it is open.
    rerender({ selected: true, reactionMenuTouch: true });
    expect(observers[0].disconnected).toBe(true);
    expect(observers).toHaveLength(1);

    // Closed after losing its anchor, the row is watched again; already out of
    // view, without focus (the menu took it to the body), that ends it at once.
    onReactionMenuRelease.mockClear();
    act(() => row.blur());
    rerender({ selected: true, reactionMenuTouch: false });
    expect(observers).toHaveLength(2);
    act(() => observers[1].fire(false));
    expect(onReactionMenuRelease).toHaveBeenCalledWith("msg-1");
    rerender({ selected: false });
    expect(observers[1].disconnected).toBe(true);
    expect(observers).toHaveLength(2);
  });

  /**
   * CQR R4: only a selection the keyboard just started may see its first
   * reading out of view taken back — WebKit scrolls the focused row in after
   * the focus event. The row is measured again once the browser has had its
   * frame to scroll: still out, the selection ends; in, it stays. Unmounting
   * meanwhile cancels the measurement.
   */
  it("reconciles the first reading after a keyboard entry, after the browser's scroll", () => {
    const observers: { fire: (seen: boolean) => void; disconnected: boolean }[] = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        record;
        constructor(callback: IntersectionObserverCallback) {
          this.record = {
            disconnected: false,
            fire: (seen: boolean) =>
              callback([{ isIntersecting: seen } as IntersectionObserverEntry], this as never),
          };
          observers.push(this.record);
        }
        observe() {}
        disconnect() {
          this.record.disconnected = true;
        }
      },
    );
    let frames = new Map<number, FrameRequestCallback>();
    let nextFrame = 0;
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.set(++nextFrame, callback);
      return nextFrame;
    });
    const cancelled = vi.fn((id: number) => frames.delete(id));
    vi.stubGlobal("cancelAnimationFrame", cancelled);
    const flushFrame = () =>
      act(() => {
        const due = frames;
        frames = new Map();
        due.forEach((callback) => callback(0));
      });

    const onReactionMenuRelease = vi.fn();
    const onMessageSelect = vi.fn();
    const props = propsWith({ onReactionMenuRelease, onMessageSelect });
    const view = render(
      <div className="chat-msg-area__list">
        <MessageBubble {...props} />
      </div>,
    );
    const rerender = (next: Partial<MessageBubbleProps>) =>
      view.rerender(
        <div className="chat-msg-area__list">
          <MessageBubble {...props} {...next} />
        </div>,
      );
    const row = screen.getByTestId("chat-msg-bubble");
    const bubble = row.querySelector(".chat-msg-area__msg-bubble")!;
    const enterByKeyboard = () => {
      vi.spyOn(row, "matches").mockImplementation((selector) => selector === ":focus-visible");
      act(() => row.focus());
      expect(onMessageSelect).toHaveBeenCalledWith("msg-1");
      rerender({ selected: true });
    };
    const list = DOMRect.fromRect({ x: 0, y: 100, width: 400, height: 500 });
    vi.spyOn(row.parentElement!, "getBoundingClientRect").mockReturnValue(list);
    const bubbleBox = vi.spyOn(bubble, "getBoundingClientRect");

    // Still out once the frame has passed: the selection ends.
    bubbleBox.mockReturnValue(DOMRect.fromRect({ x: 0, y: 900, width: 200, height: 40 }));
    enterByKeyboard();
    act(() => observers.at(-1)!.fire(false));
    expect(onReactionMenuRelease).not.toHaveBeenCalled();
    flushFrame();
    expect(onReactionMenuRelease).not.toHaveBeenCalled();
    flushFrame();
    expect(onReactionMenuRelease).toHaveBeenCalledWith("msg-1");

    // Scrolled in by then: it stays. The entry is spent — the next reading out
    // of view ends it at once.
    onReactionMenuRelease.mockClear();
    act(() => row.blur());
    rerender({ selected: false });
    bubbleBox.mockReturnValue(DOMRect.fromRect({ x: 0, y: 300, width: 200, height: 40 }));
    enterByKeyboard();
    act(() => observers.at(-1)!.fire(false));
    flushFrame();
    flushFrame();
    expect(onReactionMenuRelease).not.toHaveBeenCalled();
    act(() => observers.at(-1)!.fire(false));
    expect(onReactionMenuRelease).toHaveBeenCalledWith("msg-1");

    // Unmounted with the measurement pending: cancelled, nothing released.
    onReactionMenuRelease.mockClear();
    act(() => row.blur());
    rerender({ selected: false });
    bubbleBox.mockReturnValue(DOMRect.fromRect({ x: 0, y: 900, width: 200, height: 40 }));
    enterByKeyboard();
    act(() => observers.at(-1)!.fire(false));
    const watch = observers.at(-1)!;
    view.rerender(<div className="chat-msg-area__list" />);
    expect(watch.disconnected).toBe(true);
    expect(cancelled).toHaveBeenCalled();
    onReactionMenuRelease.mockClear();
    flushFrame();
    flushFrame();
    expect(onReactionMenuRelease).not.toHaveBeenCalled();
  });

  /**
   * CQR R5 of #1238, measured in Chromium at 390×740: selected, then zoomed
   * from 1× to 2× — the visual viewport at y=0…370, the bubble at y=528…569 —
   * and the row stayed selected and focused, the header acting on a message
   * nobody could see. The list's observer sees no change in the list.
   */
  it("ends the selection when the visual viewport leaves the row, with no list reading", () => {
    const observers: { fire: (seen: boolean) => void }[] = [];
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        record;
        constructor(callback: IntersectionObserverCallback) {
          this.record = {
            fire: (seen: boolean) =>
              callback([{ isIntersecting: seen } as IntersectionObserverEntry], this as never),
          };
          observers.push(this.record);
        }
        observe() {}
        disconnect() {}
      },
    );
    let frames = new Map<number, FrameRequestCallback>();
    let nextFrame = 0;
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.set(++nextFrame, callback);
      return nextFrame;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
    const flushFrame = () =>
      act(() => {
        const due = frames;
        frames = new Map();
        due.forEach((callback) => callback(0));
      });
    const listeners: Record<string, EventListener> = {};
    const visual = {
      offsetLeft: 0,
      offsetTop: 0,
      width: 390,
      height: 740,
      addEventListener: vi.fn((type: string, listener: EventListener) => {
        listeners[type] = listener;
      }),
      removeEventListener: vi.fn(),
    };
    vi.stubGlobal("visualViewport", visual);
    const removeWindowListener = vi.spyOn(window, "removeEventListener");

    const onReactionMenuRelease = vi.fn();
    const props = propsWith({ onReactionMenuRelease });
    const view = render(
      <div className="chat-msg-area__list" tabIndex={-1} data-testid="list">
        <MessageBubble {...props} />
      </div>,
    );
    const rerender = (next: Partial<MessageBubbleProps>) =>
      view.rerender(
        <div className="chat-msg-area__list" tabIndex={-1} data-testid="list">
          <MessageBubble {...props} {...next} />
        </div>,
      );
    const list = screen.getByTestId("list");
    const row = screen.getByTestId("chat-msg-bubble");
    vi.spyOn(list, "getBoundingClientRect").mockReturnValue(
      DOMRect.fromRect({ x: 0, y: 117, width: 390, height: 470 }),
    );
    vi.spyOn(
      row.querySelector(".chat-msg-area__msg-bubble")!,
      "getBoundingClientRect",
    ).mockReturnValue(DOMRect.fromRect({ x: 61, y: 528, width: 247, height: 41 }));
    act(() => row.focus());
    rerender({ selected: true });
    act(() => observers.at(-1)!.fire(true));
    expect(onReactionMenuRelease).not.toHaveBeenCalled();
    // A pan that keeps it in sight changes nothing.
    visual.offsetTop = 300;
    visual.height = 370;
    act(() => listeners.scroll(new Event("scroll")));
    flushFrame();
    flushFrame();
    expect(onReactionMenuRelease).not.toHaveBeenCalled();

    // Zoomed to 2×, the visual viewport at y=0…370: out, once reconciled — the
    // focus it held goes to the list, never left on the unseen row.
    visual.offsetTop = 0;
    act(() => listeners.resize(new Event("resize")));
    expect(onReactionMenuRelease).not.toHaveBeenCalled();
    flushFrame();
    flushFrame();
    expect(onReactionMenuRelease).toHaveBeenCalledTimes(1);
    expect(onReactionMenuRelease).toHaveBeenCalledWith("msg-1");
    expect(list).toHaveFocus();

    // Released, nothing watches it: back at 1×, nothing comes back.
    rerender({ selected: false });
    expect(visual.removeEventListener).toHaveBeenCalledWith("resize", listeners.resize);
    expect(visual.removeEventListener).toHaveBeenCalledWith("scroll", listeners.scroll);
    expect(removeWindowListener).toHaveBeenCalledWith("resize", listeners.resize);
    visual.height = 740;
    flushFrame();
    flushFrame();
    expect(onReactionMenuRelease).toHaveBeenCalledTimes(1);
    expect(row).not.toHaveClass("chat-msg-area__msg--selected");

    // Selected again and out of the visual viewport at the first reading, which
    // the list calls in view: that ends it at once. Focus elsewhere stays.
    onReactionMenuRelease.mockClear();
    visual.height = 370;
    const elsewhere = document.createElement("button");
    document.body.append(elsewhere);
    elsewhere.focus();
    rerender({ selected: true });
    act(() => observers.at(-1)!.fire(true));
    expect(onReactionMenuRelease).toHaveBeenCalledWith("msg-1");
    expect(elsewhere).toHaveFocus();
    elsewhere.remove();
    removeWindowListener.mockRestore();
  });
});
