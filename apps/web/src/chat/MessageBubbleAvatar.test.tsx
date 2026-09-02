/**
 * The message sender's avatar (issue #495).
 *
 * QA reproduced a bug where a message's avatar always rendered as initials,
 * even for a sender with a valid personalized avatar, in DMs and groups. The
 * contract this file guards: personalized avatar when present and loadable,
 * initials as the only fallback, and no broken-image glyph — implemented by
 * delegating to PersonAvatarImage, the same image-or-initials state machine
 * every other avatar in the app already uses.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { emptyEmojiUsage } from "./emoji/emojiUsage";
import MessageBubble from "./MessageBubble";
import type { MessageBubbleProps } from "./MessageBubble";
import type { Message } from "./chatTypes";

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

function renderBubble(overrides: Partial<MessageBubbleProps> = {}) {
  const props: MessageBubbleProps = {
    message: messageWith(),
    isMine: false,
    onToggleReaction: vi.fn(),
    onReplyMessage: vi.fn(),
    onReferenceMessage: vi.fn(),
    onToggleFavorite: vi.fn(),
    onEditMessage: vi.fn(),
    onEditForbidden: vi.fn(),
    onDeleteMessage: vi.fn(),
    recentReactionEmojis: [],
    // The reaction row's own state, owned by the conversation (issue #496).
    emojiUsage: emptyEmojiUsage,
    onEmojiToneChange: vi.fn(),
    currentUserId: "me",
    reactionMenuVisible: false,
    onReactionMenuVisibleChange: vi.fn(),
    pickerOpen: false,
    onPickerOpenChange: vi.fn(),
    ...overrides,
  };
  return render(<MessageBubble {...props} />);
}

function avatarBox() {
  return screen.getByTestId("chat-msg-bubble").querySelector(".chat-msg-area__msg-avatar")!;
}

describe("MessageBubble avatar", () => {
  it("renders the personalized avatar image when the sender has a valid one", () => {
    renderBubble({
      message: messageWith({ senderAvatarUrl: "/media/avatars/alex.png" }),
    });
    const img = avatarBox().querySelector("img");
    expect(img).toHaveAttribute("src", "/media/avatars/alex.png");
  });

  it("falls back to initials when the sender has no avatar", () => {
    renderBubble({ message: messageWith({ senderAvatarUrl: undefined }) });
    expect(avatarBox().querySelector("img")).not.toBeInTheDocument();
    expect(avatarBox()).toHaveTextContent("AS");
  });

  it("falls back to initials, with no broken-image glyph, once the image fails to load", () => {
    renderBubble({
      message: messageWith({ senderAvatarUrl: "/media/avatars/broken.png" }),
    });
    const img = avatarBox().querySelector("img")!;
    fireEvent.error(img);
    expect(avatarBox().querySelector("img")).not.toBeInTheDocument();
    expect(avatarBox()).toHaveTextContent("AS");
  });

  it("retries a changed avatar URL after a previous one failed to load", () => {
    const { rerender } = renderBubble({
      message: messageWith({ id: "msg-1", senderAvatarUrl: "/media/avatars/broken.png" }),
    });
    fireEvent.error(avatarBox().querySelector("img")!);
    expect(avatarBox().querySelector("img")).not.toBeInTheDocument();

    const props: MessageBubbleProps = {
      message: messageWith({ id: "msg-1", senderAvatarUrl: "/media/avatars/new.png" }),
      isMine: false,
      onToggleReaction: vi.fn(),
      onReplyMessage: vi.fn(),
      onReferenceMessage: vi.fn(),
      onToggleFavorite: vi.fn(),
      onEditMessage: vi.fn(),
      onEditForbidden: vi.fn(),
      onDeleteMessage: vi.fn(),
      recentReactionEmojis: [],
      emojiUsage: emptyEmojiUsage,
      onEmojiToneChange: vi.fn(),
      currentUserId: "me",
      reactionMenuVisible: false,
      onReactionMenuVisibleChange: vi.fn(),
      pickerOpen: false,
      onPickerOpenChange: vi.fn(),
    };
    rerender(<MessageBubble {...props} />);
    expect(avatarBox().querySelector("img")).toHaveAttribute("src", "/media/avatars/new.png");
  });

  it("produces correct initials for an accented, composed display name", () => {
    renderBubble({
      message: messageWith({ senderDisplayName: "Álvaro Ferreira", senderAvatarUrl: undefined }),
    });
    expect(avatarBox()).toHaveTextContent("ÁF");
  });

  it("never renders an avatar for the viewer's own message, with or without an avatar URL", () => {
    renderBubble({
      isMine: true,
      message: messageWith({ senderAvatarUrl: "/media/avatars/alex.png" }),
    });
    expect(screen.getByTestId("chat-msg-bubble").querySelector(".chat-msg-area__msg-avatar")).toBe(
      null,
    );
  });

  it("keeps the display name as the primary textual identity regardless of avatar state", () => {
    renderBubble({
      isMine: false,
      isGrouped: false,
      message: messageWith({ senderAvatarUrl: "/media/avatars/alex.png" }),
    });
    expect(screen.getByTestId("chat-msg-sender")).toHaveTextContent("Alex Souza");
  });

  it("keeps the avatar decorative when the sender name opens a DM", () => {
    const openAuthor = vi.fn();
    renderBubble({
      onOpenAuthorDM: openAuthor,
      message: messageWith({ senderAvatarUrl: "/media/avatars/alex.png" }),
    });

    fireEvent.click(avatarBox());
    expect(openAuthor).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Abrir conversa com Alex Souza" })).toBe(
      screen.getByTestId("chat-msg-sender"),
    );
  });
});
