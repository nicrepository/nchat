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
