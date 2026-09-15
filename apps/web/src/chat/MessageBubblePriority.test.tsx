/**
 * How a message's stated priority is drawn on the message itself (issue #823).
 *
 * # What these tests are really guarding
 *
 * Three rules that are easy to state and easy to lose in a stylesheet:
 *
 *  1. `standard` renders exactly what it rendered before this issue. Not "a
 *     neutral badge", not "an empty element" — nothing. It is asserted as the
 *     absence of the element, so a future change that starts marking ordinary
 *     messages fails here rather than in a busy channel;
 *  2. `important` and `urgent` are distinguishable without colour. Every
 *     assertion below reads text and icons, never a class that carries a tint,
 *     because a reader with no colour has exactly those two;
 *  3. priority belongs to the content. It is asserted to sit inside the bubble
 *     and above the body — not beside the sender, the timestamp or the presence
 *     dot, which would make the author's claim read as a fact about the author.
 *
 * And one rule that is not about rendering at all: the badge is the author's
 * claim, nothing is authorised by it, and an urgent message must stay as
 * readable as any other — so the bubble's own surface is asserted untouched.
 */

import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { emptyEmojiUsage } from "./emoji/emojiUsage";
import MessageBubble from "./MessageBubble";
import type { MessageBubbleProps } from "./MessageBubble";
import type { Message, MessageAcknowledgement } from "./chatTypes";

function messageWith(overrides: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    senderId: "user-1",
    senderDisplayName: "Alex",
    senderEmail: "alex@example.test",
    kind: "user",
    bodyText: "reiniciar o cluster agora",
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    linkSafetyState: "",
    deletedAt: null,
    createdAt: "2026-09-15T12:00:00Z",
    updatedAt: "2026-09-15T12:00:00Z",
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
    onToggleReaction: vi.fn(),
    onReplyMessage: vi.fn(),
    onReferenceMessage: vi.fn(),
    onToggleFavorite: vi.fn(),
    onEditMessage: vi.fn(),
    onEditForbidden: vi.fn(),
    onDeleteMessage: vi.fn(),
    emojiUsage: emptyEmojiUsage,
    onEmojiToneChange: vi.fn(),
    currentUserId: "me",
    recentReactionEmojis: [],
    reactionMenuVisible: false,
    onReactionMenuVisibleChange: vi.fn(),
    pickerOpen: false,
    onPickerOpenChange: vi.fn(),
    ...overrides,
  };
  return render(<MessageBubble {...props} />);
}

const priorityBadge = () => screen.queryByTestId("chat-message-priority");

afterEach(cleanup);

describe("a standard message", () => {
  // The non-regression test the issue asks for by name. Absence, not emptiness.
  it("draws no priority marker of any kind", () => {
    renderBubble({ message: messageWith({ priority: "standard" }) });
    expect(priorityBadge()).not.toBeInTheDocument();
    expect(screen.queryByText(/prioridade da mensagem/i)).not.toBeInTheDocument();
  });

  it("draws none for a message that states no priority at all", () => {
    renderBubble({ message: messageWith({ priority: undefined }) });
    expect(priorityBadge()).not.toBeInTheDocument();
  });

  // The rendering is identical, not merely similar: same markup, same content.
  it("renders exactly what a message with no priority field renders", () => {
    const { container: stated } = renderBubble({
      message: messageWith({ priority: "standard" }),
    });
    const before = stated.innerHTML;
    cleanup();
    const { container: silent } = renderBubble({ message: messageWith({ priority: undefined }) });
    expect(silent.innerHTML).toBe(before);
  });
});

describe("an important message", () => {
  it("states its priority in words a reader can read", () => {
    renderBubble({ message: messageWith({ priority: "important" }) });
    const badge = priorityBadge();
    expect(badge).toBeInTheDocument();
    expect(badge).toHaveTextContent("Importante");
  });

  // Text and an icon, so the state survives the loss of colour. The icon is
  // hidden from assistive technology because the word beside it already says it.
  it("carries an icon that is decorative and a label that is not", () => {
    const { container } = renderBubble({ message: messageWith({ priority: "important" }) });
    const icon = container.querySelector(".chat-msg-area__priority .material-symbols-outlined");
    expect(icon).toBeInTheDocument();
    expect(icon).toHaveAttribute("aria-hidden", "true");
    expect(within(priorityBadge()!).getByText("Importante")).toBeInTheDocument();
  });

  // Moderate, not an alarm: important and urgent must not be the same thing
  // with a different colour.
  it("is not announced or marked the same way an urgent message is", () => {
    renderBubble({ message: messageWith({ priority: "important" }) });
    expect(priorityBadge()).not.toHaveTextContent("Urgente");
    expect(priorityBadge()).toHaveAttribute("data-priority", "important");
  });
});

describe("an urgent message", () => {
  it("says the word, visibly", () => {
    renderBubble({ message: messageWith({ priority: "urgent" }) });
    expect(priorityBadge()).toHaveTextContent("Urgente");
    expect(priorityBadge()).toHaveAttribute("data-priority", "urgent");
  });

  // The axis is named for a screen reader, which reaches the word without the
  // message around it to make "Urgente" unambiguous.
  it("names the axis for a reader who cannot see it beside the message", () => {
    renderBubble({ message: messageWith({ priority: "urgent" }) });
    expect(within(priorityBadge()!).getByText("Prioridade da mensagem:")).toBeInTheDocument();
  });

  it("carries its own icon, distinct from important's", () => {
    const { container } = renderBubble({ message: messageWith({ priority: "urgent" }) });
    const urgentIcon = container.querySelector(
      ".chat-msg-area__priority .material-symbols-outlined",
    )?.textContent;
    cleanup();
    const { container: other } = renderBubble({
      message: messageWith({ priority: "important" }),
    });
    const importantIcon = other.querySelector(
      ".chat-msg-area__priority .material-symbols-outlined",
    )?.textContent;
    expect(urgentIcon).toBeTruthy();
    expect(urgentIcon).not.toBe(importantIcon);
  });

  // Issue #846: "Requer confirmação" and "Persistente" ride the same compact
  // row as the priority label, each independently — neither is inferred from
  // urgent, and a flag that is false draws nothing at all.
  it("shows the complementary claims only when their own flag is set", () => {
    renderBubble({
      message: messageWith({
        priority: "urgent",
        acknowledgementRequired: true,
        persistentNotifications: true,
      }),
    });
    expect(priorityBadge()).toHaveTextContent("Requer confirmação");
    expect(priorityBadge()).toHaveTextContent("Persistente");
  });

  it("shows neither complementary claim when neither flag is set", () => {
    renderBubble({ message: messageWith({ priority: "urgent" }) });
    expect(priorityBadge()).not.toHaveTextContent("Requer confirmação");
    expect(priorityBadge()).not.toHaveTextContent("Persistente");
  });

  /**
   * The rule the issue states twice: a marker on the message, never a red
   * panel. The bubble that holds the body must carry no priority class of its
   * own, so nothing repaints the surface the text is read against.
   */
  it("marks the message without repainting the bubble", () => {
    const { container } = renderBubble({ message: messageWith({ priority: "urgent" }) });
    const bubble = container.querySelector(".chat-msg-area__msg-bubble");
    expect(bubble).toBeInTheDocument();
    expect(bubble?.className).not.toMatch(/priority|urgent|important/);
    const shell = container.querySelector(".chat-msg-area__msg");
    expect(shell?.className).not.toMatch(/priority|urgent|important/);
    // The body is still there, unchanged and unwithheld.
    expect(screen.getByText("reiniciar o cluster agora")).toBeInTheDocument();
  });

  // A marker, not an animation: #823 rules out anything that blinks or pulses.
  it("adds no animation to the message", () => {
    const { container } = renderBubble({ message: messageWith({ priority: "urgent" }) });
    expect(priorityBadge()?.getAttribute("style")).toBeNull();
    expect(container.querySelectorAll("[class*=blink], [class*=pulse]").length).toBe(0);
  });
});

describe("where the priority is drawn", () => {
  /**
   * Priority is a property of what was said. Asserting the containment both
   * ways is what stops it drifting into the meta row, where it would read as an
   * attribute of the sender rather than of the message.
   */
  it("belongs to the content block, not to the sender or the timestamp", () => {
    const { container } = renderBubble({
      message: messageWith({ priority: "urgent" }),
      isMine: false,
    });
    const bubble = container.querySelector(".chat-msg-area__msg-bubble");
    expect(bubble).toContainElement(priorityBadge());

    const meta = container.querySelector(".chat-msg-area__msg-meta");
    expect(meta).not.toContainElement(priorityBadge());
    const avatar = container.querySelector(".chat-msg-area__msg-avatar");
    expect(avatar).not.toContainElement(priorityBadge());
  });

  it("sits above the message body", () => {
    const { container } = renderBubble({ message: messageWith({ priority: "urgent" }) });
    const bubble = container.querySelector(".chat-msg-area__msg-bubble")!;
    const body = screen.getByText("reiniciar o cluster agora");
    expect(bubble).toContainElement(body);
    // First child of the bubble: above the notices, the quote and the body, so
    // the claim is read before the thing it qualifies.
    expect(bubble.firstElementChild).toBe(priorityBadge());
  });

  // Grouping suppresses the avatar, the name and the time — all facts about the
  // sender. The claim belongs to each message, so each message keeps it.
  it("stays on every message of a group", () => {
    renderBubble({ message: messageWith({ priority: "urgent" }), isGrouped: true });
    expect(priorityBadge()).toHaveTextContent("Urgente");
  });

  it("is drawn the same way on the reader's own message", () => {
    renderBubble({ message: messageWith({ priority: "urgent" }), isMine: true });
    expect(priorityBadge()).toHaveTextContent("Urgente");
  });

  // The placeholder replaces the message; the claim its author made about it
  // goes with everything else the placeholder replaces.
  it("is withheld from a removed message", () => {
    renderBubble({
      message: messageWith({ priority: "urgent", isRemoved: true, bodyText: "" }),
    });
    expect(priorityBadge()).not.toBeInTheDocument();
    expect(screen.getByText("Mensagem removida.")).toBeInTheDocument();
  });
});

describe("priority beside the other things a message carries", () => {
  const acknowledgement: MessageAcknowledgement = {
    messageId: "msg-1",
    required: true,
    total: 7,
    pending: 3,
    acknowledged: 4,
    responded: 0,
    expired: 0,
    cancelled: 0,
  };

  // The two halves of #820's urgent flow, on one message: the badge above the
  // body, the sender's count below it, and neither displacing the other.
  it("coexists with the acknowledgement summary the sender sees", () => {
    renderBubble({
      message: messageWith({ priority: "urgent", acknowledgementRequired: true, senderId: "me" }),
      currentUserId: "me",
      acknowledgement,
      onAcknowledge: vi.fn(),
    });
    expect(priorityBadge()).toHaveTextContent("Urgente");
    expect(screen.getByTestId("acknowledgement-summary")).toHaveTextContent("4 de 7 confirmaram");
  });

  // A message can be forwarded and urgent at once; the two markers are separate
  // facts and neither replaces the other.
  it("coexists with the forwarded marker", () => {
    renderBubble({
      message: messageWith({ priority: "important", isForwarded: true }),
    });
    expect(priorityBadge()).toHaveTextContent("Importante");
    expect(screen.getByTestId("chat-message-forwarded")).toBeInTheDocument();
  });

  /**
   * Rendering only, and deliberately not evidence about editing.
   *
   * The fixture is already marked edited, so what this asserts is that the
   * badge and the "(editada)" affordance coexist — not that an edit preserved
   * anything, which a fixture cannot show. The transition itself is exercised
   * where it happens, against the real editMessageLocal round trip and its
   * rollback: see useMessages.test.ts, "useMessages — message editing".
   */
  it("draws the badge on a message that is already marked edited", () => {
    renderBubble({
      message: messageWith({ priority: "urgent", isEdited: true, editCount: 1 }),
    });
    expect(priorityBadge()).toHaveTextContent("Urgente");
    expect(screen.getByText("(editada)")).toBeInTheDocument();
  });

  // A quote is a preview of another message, and the preview contract carries
  // no priority. Drawing one here would be inventing a claim the server never
  // made about the quoted message.
  it("does not put a badge on a quoted preview", () => {
    renderBubble({
      message: messageWith({
        priority: "standard",
        quoted: {
          id: "msg-0",
          authorId: "user-2",
          bodyText: "mensagem original",
          bodyFormat: "v2",
          isRemoved: false,
          deletedAt: null,
          createdAt: "2026-09-15T11:00:00Z",
          updatedAt: "2026-09-15T11:00:00Z",
          linkSafetyState: "",
        },
      }),
      quoteAuthorLabel: "Bruno",
    });
    expect(screen.getByTestId("chat-message-quote")).toBeInTheDocument();
    expect(priorityBadge()).not.toBeInTheDocument();
  });
});
