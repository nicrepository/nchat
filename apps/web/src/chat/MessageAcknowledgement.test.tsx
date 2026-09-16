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

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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
    // Issue #846: distinct from "acknowledged" and never worded as a
    // confirmation — a reply is not the same outcome.
    ["responded", /^respondido$/i],
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
    // The sender is not a recipient of their own message, so no action is drawn.
    expect(screen.queryByRole("button", { name: /confirmar recebimento/i })).toBeNull();
  });

  // Issue #846: the compact footer is one line — no separate pending count, no
  // "confirmação parcial" block — whatever the counts are.
  it("keeps the summary to one line regardless of how many are still pending", () => {
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

// ── the sender's detail (issue #846's popover) ────────────────────────────────

describe("acknowledgement detail", () => {
  // useAnchoredPicker treats a zero-size anchor as off-screen and dismisses
  // immediately (the same geometry MessageToolbarPlacement.test.tsx stubs for
  // the reaction toolbar's own picker) — jsdom's real getBoundingClientRect is
  // all zeros, so without this the popover would open and instantly close.
  beforeEach(() => {
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue({
      top: 100,
      bottom: 130,
      left: 100,
      right: 200,
      width: 100,
      height: 30,
      x: 100,
      y: 100,
      toJSON: () => ({}),
    } as DOMRect);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  const detail = [
    { recipientId: "u-1", state: "acknowledged" as const, resolvedAt: "2026-09-11T13:00:00Z" },
    { recipientId: "u-2", state: "pending" as const },
    { recipientId: "u-3", state: "responded" as const, resolvedAt: "2026-09-11T14:00:00Z" },
  ];

  function profilesFor(ids: string[]) {
    const names: Record<string, string> = {
      "u-1": "Ana Souza",
      "u-2": "Caio Lima",
      "u-3": "Bia Rocha",
    };
    return Promise.resolve(
      ids.filter((id) => names[id]).map((id) => ({ userId: id, displayName: names[id] })),
    );
  }

  // The summary is the popover's trigger only once there is somewhere for the
  // list to come from (issue #846: a single recipient never offers it either,
  // covered by the one-to-one test below).
  it("lets the sender open the list the server authorised", async () => {
    renderAsSender({
      acknowledgement: summaryWith({ total: 3, acknowledged: 1, recipients: detail }),
      resolveIdentities: profilesFor,
    });
    fireEvent.click(screen.getByRole("button", { name: /confirmaram.*ver confirmações/i }));
    expect(await screen.findByTestId("ack-details-dialog")).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByTestId("ack-details-recipient")).toHaveLength(3));
    const rows = screen.getAllByTestId("ack-details-recipient");
    // Never the raw id (issue #846's own rule) — the resolved name instead.
    expect(rows[0]).toHaveTextContent("Ana Souza");
    expect(rows[0]).not.toHaveTextContent("u-1");
  });

  // A resolve that never returns a name for an id — not authorised, or gone —
  // degrades to a neutral placeholder, never the raw id.
  it("never shows a raw recipient id, even when identity does not resolve", async () => {
    renderAsSender({
      acknowledgement: summaryWith({ total: 3, acknowledged: 1, recipients: detail }),
      resolveIdentities: () => Promise.resolve([]),
    });
    fireEvent.click(screen.getByRole("button", { name: /confirmaram.*ver confirmações/i }));
    await screen.findByTestId("ack-details-dialog");
    await waitFor(() => expect(screen.getAllByTestId("ack-details-recipient")).toHaveLength(3));
    for (const row of screen.getAllByTestId("ack-details-recipient")) {
      expect(row).not.toHaveTextContent(/^u-\d$/);
    }
  });

  // The server decides who may see the list. When it sends none yet, the
  // popover shows a neutral loading state rather than an empty one that reads
  // as "nobody has done anything".
  it("shows a loading state until the detail read lands", async () => {
    renderAsSender({
      acknowledgement: summaryWith({ total: 3, acknowledged: 1, recipients: undefined }),
      resolveIdentities: profilesFor,
    });
    fireEvent.click(screen.getByRole("button", { name: /confirmaram.*ver confirmações/i }));
    expect(await screen.findByTestId("ack-details-dialog")).toHaveTextContent(/carregando/i);
  });

  // A 1:1 DM is the degenerate group: one recipient, told directly rather than
  // as "0 de 1"/"1 de 1" — and never offered a popover for a list of one.
  it("reads clearly for a one-to-one conversation, without a popover", () => {
    renderAsSender({
      acknowledgement: summaryWith({
        total: 1,
        acknowledged: 0,
        pending: 1,
        recipients: [{ recipientId: "u-only", state: "pending" }],
      }),
      resolveIdentities: profilesFor,
    });
    expect(screen.getByTestId("acknowledgement-summary")).toHaveTextContent(
      "Aguardando confirmação",
    );
    expect(screen.queryByRole("button", { name: /ver confirmações/i })).toBeNull();
  });

  it("tells a one-to-one sender the exact confirmation time when it is known", () => {
    renderAsSender({
      acknowledgement: summaryWith({
        total: 1,
        acknowledged: 1,
        pending: 0,
        recipients: [
          { recipientId: "u-only", state: "acknowledged", resolvedAt: "2026-09-11T20:10:00Z" },
        ],
      }),
    });
    expect(screen.getByTestId("acknowledgement-summary")).toHaveTextContent(/confirmado às/i);
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
