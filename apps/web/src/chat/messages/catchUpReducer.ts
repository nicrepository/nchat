/**
 * Issue #947: applying a catch-up read, and what the server said while it was
 * in flight.
 *
 * The read is a snapshot of the server taken at some instant after it was
 * requested. Meanwhile the socket keeps delivering, and the reader keeps acting.
 * Three kinds of state meet here, and each is reconciled by its own rule:
 *
 *  - **server facts read back** — the listing — replace what is drawn, in one
 *    batch, unless the timeline already draws a newer copy of a message;
 *  - **server facts delivered during the read** — the journal (see
 *    catchUpJournal.ts) — are replayed on top. They are absolute (an edit at a
 *    version, a reaction list, a link verdict), and a target's events arrive in
 *    the order the server produced them, so replaying them after the read
 *    yields what the server holds now. Reactions carry no version a timestamp
 *    could order, which is why they are replayed rather than compared; a
 *    single-message read is the exception that does carry one, and is skipped
 *    when the listing already holds a newer copy;
 *  - **this reader's unconfirmed intents** — pending reactions and edits — are
 *    never replayed: they are not facts, and repeating a toggle or an increment
 *    over a baseline that already contains it corrupts it. They live in their
 *    overlays, and are redrawn on top of the result, each by the rule its own
 *    reducer uses.
 *
 * The result rewrites the timeline and nothing else: the composer, the reply
 * being written and the send in flight are not the timeline's, so no part of a
 * catch-up can reach them — except a reply whose parent the read says was
 * deleted, which the server would refuse (CQ947-R2-02). That is the reply drawn
 * when the read lands, by id; a parent the read does not carry proves nothing.
 */

import type { Message, MessagePage } from "../chatTypes";
import { withOptimisticEdit } from "./editingReducer";
import { applyLinkSafetyCorrections } from "./linkSafetyCorrections";
import { insertMessageChronologically } from "./messageOrder";
import { applyPendingReactions } from "./pendingReactions";
import { confirmedReactions } from "./reactionsReducer";
import { applyMessageSnapshots } from "./realtimeReducer";
import type {
  Action,
  ActionOf,
  LastMutation,
  MessagesState,
  PendingEdit,
  PendingReactions,
} from "./types";

type Reducer = (state: MessagesState, action: Action) => MessagesState;

/** Server reactions, by message, for every message the read or the journal spoke about. */
type ServerReactions = Map<string, Message["reactions"]>;

/**
 * True when the timeline already draws a copy newer than the one read — a
 * single-message read that landed while the listing was in flight.
 */
function drawnIsNewer(drawn: Message | undefined, read: Message): boolean {
  return drawn !== undefined && Date.parse(drawn.updatedAt) > Date.parse(read.updatedAt);
}

interface Base {
  state: MessagesState;
  /** The copies actually applied — what the server said about each of them. */
  applied: Message[];
  lastMutation: LastMutation;
}

/** The listing written onto the timeline, in place. */
function stitch(state: MessagesState, read: Message[]): Base {
  const drawn = new Map(state.messages.map((message) => [message.id, message]));
  const applied = read.filter((message) => !drawnIsNewer(drawn.get(message.id), message));
  const batch = applyMessageSnapshots(state, applied, true);
  return {
    state: {
      ...state,
      messages: batch.messages,
      linkSafetyCorrections: batch.linkSafetyCorrections,
    },
    applied,
    lastMutation: batch.appended ? "ws_append" : "none",
  };
}

/**
 * A gap too long to stitch: the timeline becomes what was read — plus the
 * reader's own messages still being scanned, which no listing ever returns and
 * which only their own promotion or refusal may take out.
 */
function replace(state: MessagesState, page: MessagePage): Base {
  const read = page.messages.map((message) =>
    applyLinkSafetyCorrections(message, state.linkSafetyCorrections),
  );
  const pending = state.messages.filter((message) => message.status === "pending_link_scan");
  const messages = pending.reduce(
    (current, message) => insertMessageChronologically(current, message).messages,
    read,
  );
  return {
    state: { ...state, messages, nextCursor: page.nextCursor, loadingMore: false },
    applied: page.messages,
    lastMutation: "initial",
  };
}

/**
 * The absolute reaction list a journal entry states — an event's, or an
 * authoritative read's (CQ947-R2, section 10) — by message, if it states one.
 */
function statedReactions(
  entry: Action,
  previous: (messageId: string) => Message["reactions"],
): [string, Message["reactions"]] | undefined {
  if (entry.type === "reaction_snapshot") return [entry.messageId, entry.reactions];
  if (entry.type !== "reaction_updated" || !entry.event.reaction) return undefined;
  const { reaction } = entry.event;
  return [
    reaction.message_id,
    confirmedReactions(previous(reaction.message_id), reaction, entry.actorIsMe),
  ];
}

/**
 * False for a single-message read older than the copy the rebase already holds:
 * replayed, it would undo the listing. Newer, it is what the server says now —
 * and a message the listing does not carry is inserted, not lost to a replace.
 */
function stillCurrent(state: MessagesState, entry: Action): boolean {
  if (entry.type !== "message_snapshot") return true;
  const held = state.messages.find(({ id }) => id === entry.message.id);
  return !drawnIsNewer(held, entry.message);
}

/** The journal replayed on top of the base; reaction lists are collected, not drawn. */
function replayJournal(
  base: MessagesState,
  journal: Action[],
  serverReactions: ServerReactions,
  replay: Reducer,
): MessagesState {
  const drawn = new Map(base.messages.map((message) => [message.id, message]));
  const previous = (messageId: string) =>
    serverReactions.get(messageId) ??
    base.pendingReactions.get(messageId)?.confirmed ??
    drawn.get(messageId)?.reactions ??
    [];
  return journal.reduce((state, entry) => {
    const stated = statedReactions(entry, previous);
    if (stated) {
      serverReactions.set(...stated);
      return state;
    }
    return stillCurrent(state, entry) ? replay(state, entry) : state;
  }, base);
}

/** One message with this reader's pending intents drawn over the server's version. */
function redrawMessage(
  message: Message,
  serverReactions: ServerReactions,
  pendingReactions: Map<string, PendingReactions>,
  pendingEdits: Map<string, PendingEdit>,
): Message {
  if (message.isRemoved) return message;
  const confirmed = serverReactions.get(message.id);
  const reacted = confirmed
    ? {
        ...message,
        reactions: applyPendingReactions({
          confirmed,
          intents: pendingReactions.get(message.id)?.intents ?? new Map(),
        }),
      }
    : message;
  const edit = pendingEdits.get(message.id);
  // The server copy already reaches the version this edit claims: it carries
  // this edit, or a later one, and drawing it again would count it twice.
  return edit && reacted.editCount < edit.editCount ? withOptimisticEdit(reacted, edit) : reacted;
}

/** The pending reactions, re-based on what the server now says about each message. */
function rebasePendingReactions(
  pendingReactions: Map<string, PendingReactions>,
  serverReactions: ServerReactions,
): Map<string, PendingReactions> {
  const next = new Map(pendingReactions);
  for (const [messageId, pending] of pendingReactions) {
    const confirmed = serverReactions.get(messageId);
    if (confirmed) next.set(messageId, { confirmed, intents: pending.intents });
  }
  return next;
}

/** The reply being written, unless the read says its parent was deleted. */
function replyAfterRead(replyTo: Message | null, applied: Message[]): Message | null {
  const parent = replyTo && applied.find(({ id }) => id === replyTo.id);
  return parent && (parent.isRemoved || parent.status === "deleted") ? null : replyTo;
}

function applyCaughtUp(
  state: MessagesState,
  action: ActionOf<"caught_up">,
  replay: Reducer,
): MessagesState {
  const base = action.replace ? replace(state, action.page) : stitch(state, action.page.messages);
  const serverReactions: ServerReactions = new Map(
    base.applied.map((message) => [message.id, message.reactions]),
  );
  const settled = replayJournal(base.state, action.journal, serverReactions, replay);
  return {
    ...state,
    messages: settled.messages.map((message) =>
      redrawMessage(message, serverReactions, state.pendingReactions, state.pendingEdits),
    ),
    pendingReactions: rebasePendingReactions(state.pendingReactions, serverReactions),
    replyTo: replyAfterRead(state.replyTo, base.applied),
    linkSafetyCorrections: settled.linkSafetyCorrections,
    nextCursor: base.state.nextCursor,
    loadingMore: base.state.loadingMore,
    lastMutation: base.lastMutation,
    realtimeError: null,
    syncError: null,
  };
}

/** The catch-up's two outcomes: a read applied, or one that is still owed. */
export function reduceCatchUp(
  state: MessagesState,
  action: Action,
  replay: Reducer,
): MessagesState | undefined {
  switch (action.type) {
    case "caught_up":
      return applyCaughtUp(state, action, replay);
    case "catch_up_failed":
      return { ...state, syncError: action.error };
    default:
      return undefined;
  }
}
