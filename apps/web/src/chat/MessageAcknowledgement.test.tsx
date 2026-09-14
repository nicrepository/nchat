/**
 * Acknowledgement in the message bubble (issue #824).
 *
 * What this guards is the contract #824 states for a reader: the request is
 * visible, confirming it is explicit, the terminal states are distinguishable
 * in words rather than in colour, and the sender sees the server's own counts.
 *
 * The strip renders what the server said and nothing else — so every case below
 * is "given this summary, this is what a person sees and this is what a click
 * causes". Nothing here decides who may act; the server does, and the tests
 * assert that the client does not second-guess it.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import MessageAcknowledgementStrip from "./MessageAcknowledgement";
import MessageBubble, { type MessageBubbleProps } from "./MessageBubble";
import { emptyEmojiUsage } from "./emoji/emojiUsage";
import type { Message, MessageAcknowledgement } from "./chatTypes";

function summaryWith(overrides: Partial<MessageAcknowledgement> = {}): MessageAcknowledgement {
  return {
    messageId: "msg-1",
    required: true,
    total: 5,
    pending: 2,
    acknowledged: 3,
    responded: 0,
    expired: 0,
    cancelled: 0,
    ...overrides,
  };
}

function renderStrip(
  overrides: Partial<React.ComponentProps<typeof MessageAcknowledgementStrip>> = {},
) {
  const onAcknowledge = vi.fn();
  render(
    <MessageAcknowledgementStrip
      messageId="msg-1"
      acknowledgement={summaryWith()}
      senderId="sender-1"
      currentUserId="reader-1"
      submitting={false}
      onAcknowledge={onAcknowledge}
      {...overrides}
    />,
  );
  return { onAcknowledge };
}

/** Renders as the message's author: the one role that sees the summary. */
function renderAsSender(
  overrides: Partial<React.ComponentProps<typeof MessageAcknowledgementStrip>> = {},
) {
  return renderStrip({ senderId: "sender-1", currentUserId: "sender-1", ...overrides });
}

function action() {
  return screen.getByRole("button", { name: /confirmar recebimento/i });
}

describe("message acknowledgement strip", () => {
  it("offers the action to a recipient who has not answered", () => {
    renderStrip({
      currentUserId: "reader-1",
      acknowledgement: summaryWith({ viewerState: "pending" }),
    });
    expect(action()).toBeEnabled();
  });

  it("records the confirmation for the message it belongs to", () => {
    const { onAcknowledge } = renderStrip({
      acknowledgement: summaryWith({ viewerState: "pending" }),
    });
    fireEvent.click(action());
    expect(onAcknowledge).toHaveBeenCalledWith("msg-1");
  });

  it("cannot start a second confirmation while the first is in flight", () => {
    const { onAcknowledge } = renderStrip({
      acknowledgement: summaryWith({ viewerState: "pending" }),
      submitting: true,
    });
    const button = screen.getByRole("button", { name: /confirmar recebimento/i });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    fireEvent.click(button);
    expect(onAcknowledge).not.toHaveBeenCalled();
  });

  // Every terminal state resolves the request, and each says which one it was:
  // confirmed, answered, withdrawn or timed out are four different facts for the
  // person who was asked.
  it.each([
    ["acknowledged", /recebimento confirmado/i],
    ["responded", /resolvido pela sua resposta/i],
    ["expired", /confirmação expirada/i],
    ["cancelled", /confirmação cancelada/i],
  ] as const)("reports %s without offering the action again", (state, label) => {
    renderStrip({
      currentUserId: "reader-1",
      acknowledgement: summaryWith({ viewerState: state }),
    });
    expect(screen.getByTestId("acknowledgement-state")).toHaveTextContent(label);
    expect(screen.queryByRole("button", { name: /confirmar recebimento/i })).toBeNull();
  });

  // The state is carried in text and in a data attribute, never in colour
  // alone — which is what a screen reader and a monochrome display both need.
  it("names the terminal state in text, not only by styling", () => {
    renderStrip({
      currentUserId: "reader-1",
      acknowledgement: summaryWith({ viewerState: "acknowledged" }),
    });
    expect(screen.getByTestId("acknowledgement-state")).toHaveAttribute(
      "data-state",
      "acknowledged",
    );
  });

  it("shows the sender the server's counts rather than a recomputed total", () => {
    renderAsSender({
      acknowledgement: summaryWith({ total: 7, acknowledged: 4, pending: 2, responded: 1 }),
    });
    const summary = screen.getByTestId("acknowledgement-summary");
    expect(summary).toHaveTextContent("4 de 7 confirmaram");
    expect(summary).toHaveTextContent("2 pendente");
    // The sender is not a recipient of their own message, so no action is drawn.
    expect(screen.queryByRole("button", { name: /confirmar recebimento/i })).toBeNull();
  });

  // Nothing outstanding is a state worth reading plainly: the pending line is
  // omitted rather than rendered as "0 pendente(s)".
  it("omits the pending line once everybody has answered", () => {
    renderAsSender({ acknowledgement: summaryWith({ total: 3, acknowledged: 3, pending: 0 }) });
    const summary = screen.getByTestId("acknowledgement-summary");
    expect(summary).toHaveTextContent("3 de 3 confirmaram");
    expect(summary).not.toHaveTextContent(/pendente/i);
  });

  it("says nothing at all until the summary has been read", () => {
    renderStrip({ acknowledgement: undefined });
    expect(screen.queryByTestId("acknowledgement")).toBeNull();
  });

  it("says nothing for a message that asked nobody", () => {
    renderStrip({ acknowledgement: summaryWith({ required: false, total: 0 }) });
    expect(screen.queryByTestId("acknowledgement")).toBeNull();
  });
});

// ── viewer roles ─────────────────────────────────────────────────────────────
//
// Code review, #824: the strip decided "is this the sender?" by asking whether
// viewerState was absent. Absent means "this message never asked you", which is
// true of the sender and equally true of anybody who joined the conversation
// after the question was put — so a late joiner was being shown the sender's
// view. The role is now decided from identity, and these tests hold it there.

describe("acknowledgement viewer roles", () => {
  it("treats the message author as the sender", () => {
    renderAsSender({ acknowledgement: summaryWith() });
    expect(screen.getByTestId("acknowledgement")).toHaveAttribute("data-role", "sender");
    expect(screen.getByTestId("acknowledgement-summary")).toBeInTheDocument();
  });

  it("treats somebody the message asked as a recipient", () => {
    renderStrip({
      senderId: "sender-1",
      currentUserId: "reader-1",
      acknowledgement: summaryWith({ viewerState: "pending" }),
    });
    expect(screen.getByTestId("acknowledgement")).toHaveAttribute("data-role", "recipient");
    expect(action()).toBeInTheDocument();
    expect(screen.queryByTestId("acknowledgement-summary")).toBeNull();
  });

  // The regression: someone who joined afterwards is neither, and must be shown
  // neither view.
  it("shows a late joiner nothing at all", () => {
    const { onAcknowledge } = renderStrip({
      senderId: "sender-1",
      currentUserId: "late-joiner",
      acknowledgement: summaryWith({ viewerState: undefined, recipients: undefined }),
    });
    expect(screen.queryByTestId("acknowledgement")).toBeNull();
    expect(screen.queryByTestId("acknowledgement-summary")).toBeNull();
    expect(screen.queryByRole("button", { name: /confirmar recebimento/i })).toBeNull();
    expect(onAcknowledge).not.toHaveBeenCalled();
  });

  // Even if a server ever sent a recipient list to somebody who is not the
  // author, the late joiner draws nothing: the role gate comes first.
  it("never shows a late joiner other people's answers", () => {
    renderStrip({
      senderId: "sender-1",
      currentUserId: "late-joiner",
      acknowledgement: summaryWith({
        viewerState: undefined,
        recipients: [{ recipientId: "u-1", state: "acknowledged" }],
      }),
    });
    expect(screen.queryByTestId("acknowledgement-details")).toBeNull();
    expect(screen.queryByTestId("acknowledgement-recipient")).toBeNull();
  });

  // An unauthenticated render has no identity to compare, so it cannot be the
  // author — and falls to the neutral case rather than to the sender's.
  it("does not treat an unknown reader as the sender", () => {
    renderStrip({
      senderId: "sender-1",
      currentUserId: "",
      acknowledgement: summaryWith({ viewerState: undefined }),
    });
    expect(screen.queryByTestId("acknowledgement")).toBeNull();
  });
});

// ── the sender's detail ──────────────────────────────────────────────────────

describe("acknowledgement detail", () => {
  const detail = [
    { recipientId: "u-1", state: "acknowledged" as const, resolvedAt: "2026-09-11T13:00:00Z" },
    { recipientId: "u-2", state: "pending" as const },
    { recipientId: "u-3", state: "responded" as const, resolvedAt: "2026-09-11T14:00:00Z" },
  ];

  it("lets the sender open the list the server authorised", () => {
    renderAsSender({
      acknowledgement: summaryWith({ total: 3, acknowledged: 1, recipients: detail }),
    });
    expect(screen.getByTestId("acknowledgement-details")).toBeInTheDocument();
    const rows = screen.getAllByTestId("acknowledgement-recipient");
    expect(rows).toHaveLength(3);
    expect(rows[0]).toHaveTextContent("u-1");
    expect(rows[0]).toHaveTextContent("Confirmou");
    expect(rows[1]).toHaveTextContent("Pendente");
    expect(rows[2]).toHaveTextContent("Respondeu");
  });

  it("names each individual state, not just a colour", () => {
    renderAsSender({
      acknowledgement: summaryWith({
        recipients: [
          { recipientId: "u-4", state: "expired" },
          { recipientId: "u-5", state: "cancelled" },
        ],
      }),
    });
    const rows = screen.getAllByTestId("acknowledgement-recipient");
    expect(rows[0]).toHaveTextContent("Expirou");
    expect(rows[0]).toHaveAttribute("data-state", "expired");
    expect(rows[1]).toHaveTextContent("Cancelado");
  });

  // The server decides who may see the list. When it sends none, the sender
  // still gets the counts and the disclosure is simply not offered — no second
  // request, no fallback, no locally-inferred authorisation.
  it("degrades to counts alone when the server sent no list", () => {
    renderAsSender({ acknowledgement: summaryWith({ recipients: undefined }) });
    expect(screen.getByTestId("acknowledgement-summary")).toBeInTheDocument();
    expect(screen.queryByTestId("acknowledgement-details")).toBeNull();
  });

  it("offers no disclosure for an empty list", () => {
    renderAsSender({ acknowledgement: summaryWith({ recipients: [] }) });
    expect(screen.queryByTestId("acknowledgement-details")).toBeNull();
  });

  // A 1:1 DM is the degenerate group: one recipient, and the sender must still
  // be able to tell which of the five states they are in.
  it("reads clearly for a one-to-one conversation", () => {
    renderAsSender({
      acknowledgement: summaryWith({
        total: 1,
        acknowledged: 0,
        pending: 1,
        recipients: [{ recipientId: "u-only", state: "pending" }],
      }),
    });
    expect(screen.getByTestId("acknowledgement-summary")).toHaveTextContent("0 de 1 confirmaram");
    expect(screen.getAllByTestId("acknowledgement-recipient")[0]).toHaveTextContent("Pendente");
  });
});

// ── inside the bubble ────────────────────────────────────────────────────────

function messageWith(overrides: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    senderId: "user-1",
    senderDisplayName: "Alex Souza",
    senderEmail: "alex@example.test",
    kind: "user",
    bodyText: "reiniciar o cluster",
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    linkSafetyState: "",
    deletedAt: null,
    createdAt: "2026-09-11T12:00:00Z",
    updatedAt: "2026-09-11T12:00:00Z",
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
    message: messageWith({ acknowledgementRequired: true }),
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
    ...overrides,
  };
  return render(<MessageBubble {...props} />);
}

describe("MessageBubble acknowledgement", () => {
  it("draws the strip for a message that asked for confirmation", () => {
    renderBubble({
      acknowledgement: summaryWith({ viewerState: "pending" }),
      onAcknowledge: vi.fn(),
    });
    expect(screen.getByRole("button", { name: /confirmar recebimento/i })).toBeInTheDocument();
  });

  // The central rule of #824 at the rendering layer: drawing a message is not
  // confirming it. Nothing about mounting a bubble may call the action.
  it("never confirms a message merely by rendering it", () => {
    const onAcknowledge = vi.fn();
    renderBubble({ acknowledgement: summaryWith({ viewerState: "pending" }), onAcknowledge });
    expect(onAcknowledge).not.toHaveBeenCalled();
  });

  it("draws nothing for an ordinary message", () => {
    renderBubble({ message: messageWith(), onAcknowledge: vi.fn() });
    expect(screen.queryByTestId("acknowledgement")).toBeNull();
  });
});
