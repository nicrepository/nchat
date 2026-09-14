/**
 * The one place an event becomes a notification *class* (issue #826).
 *
 * Three facts decide how strongly a message asks for the reader's attention —
 * the author's priority, whether the message names this reader, and where the
 * reader actually is — and before this module each consumer answered that in
 * its own terms. A resolution that lives in several places is a resolution that
 * will eventually disagree with itself: one surface treating a mention as
 * ordinary while another treats it as urgent is not a bug in either of them, it
 * is the absence of a single answer.
 *
 * So this module answers it once, and consumers decide only *how* to deliver
 * the class they are handed. They never recompute which class an event has.
 *
 * ## What this is not
 *
 * It is not an authorisation. Whether an event may alert at all is the central
 * policy engine's decision (`libs/go/platform/notificationpolicy`, #744),
 * narrowed locally by soundRules' per-surface gates. A class says how loud
 * something would be *if* it is allowed to be anything; it can never turn a
 * `deny` into a surface, and nothing here is consulted before those gates.
 *
 * It is also not a mechanism. There is no `HTMLAudioElement` here, no toast, no
 * Notification API, no Service Worker and no React: the resolution is a pure
 * function of explicit data, which is what lets the precedence matrix be tested
 * as a matrix rather than through a browser that has to be persuaded to play a
 * sound. Browser state is read at the edge (see readAttentionContext in
 * notificationPresentation) and passed in already resolved.
 *
 * ## Precedence
 *
 * ```text
 * URGENT > MENTION > IMPORTANT/NORMAL > IN-CONVERSATION
 * ```
 *
 * Read as a ladder of attention, strongest first, which is how #826 states it
 * and how #819's Lumen family consumes it: `in-conversation` is the most
 * discreet treatment, the one that *replaces* `message` when the reader is
 * already watching the conversation ("conversa aberta/ativa -> Lumen
 * In-Conversation; conversa nao aberta -> Lumen Message"). It is not a rung
 * that `message` outranks in the same context — the two describe the same
 * ordinary message in two different contexts, and exactly one of them applies.
 *
 * The pairwise wins #826's acceptance criteria state are the ones enforced
 * here, and all four hold: urgent over mention, urgent over in-conversation,
 * mention over message, mention over in-conversation.
 *
 * `important` deliberately resolves exactly like `standard`. #826 is explicit
 * that it introduces no new class at this stage — it keeps the message/mention
 * treatment its context already gives it — and a class exists here only when
 * something downstream must treat it differently.
 */

import type { MessagePriority } from "./chatTypes";

/**
 * The closed vocabulary every consumer receives.
 *
 * Four values, because four are what the surfaces downstream distinguish (see
 * #819's Lumen family). It is deliberately not the same axis as soundRules'
 * `SoundClass`, which is the *server's* classification of an event for a
 * recipient (`general`/`direct`/`mention`) and answers a different question:
 * what kind of event is this, rather than how strongly does it ask for
 * attention right now. Collapsing the two would tie the local attention
 * context to a decision the server makes without it.
 */
export type NotificationClass = "urgent" | "mention" | "message" | "in-conversation";

/**
 * Where the reader actually is, as three separate facts.
 *
 * They are separate because a single "is this conversation active" boolean is
 * what #826 exists to refuse: a conversation whose id is the current one is not
 * a conversation the reader is looking at when the tab sits in the background
 * or the window has lost focus. Each fact is observed by the client alone — no
 * server sees any of them.
 */
export interface AttentionContext {
  /** This client is showing the conversation the event belongs to. */
  conversationOpen: boolean;
  /** The tab is not in the background (`document.visibilityState`). */
  documentVisible: boolean;
  /** The window has the reader's focus (`document.hasFocus()`). */
  windowFocused: boolean;
}

/** One event, reduced to the facts that decide its class and nothing else. */
export interface NotificationClassInput {
  /** The author's stated priority, already normalised. See chatTypes. */
  priority: MessagePriority;
  /**
   * The message names this reader, as the server's own mention codec read it
   * (`named_user_ids`/`names_everyone`, via soundRules' isNamedRecipient). The
   * browser does not parse the body to find out — a client grammar that drifts
   * from the server's is a client that disagrees about what a mention is.
   */
  namesRecipient: boolean;
  attention: AttentionContext;
}

/**
 * Whether the reader is demonstrably watching this conversation right now.
 *
 * All three, never a subset: the conversation open in a hidden tab and the
 * conversation open in an unfocused window are both "somewhere the reader is
 * not", and treating either as attention is how a message gets silently
 * downgraded to the discreet treatment while nobody is looking at it.
 */
export function isInConversation(attention: AttentionContext): boolean {
  return attention.conversationOpen && attention.documentVisible && attention.windowFocused;
}

/**
 * Resolves one event's notification class. Pure, total and deterministic: the
 * same input always yields the same value, and there is no state to carry
 * between calls — repeated resolution of one event is free and changes nothing.
 *
 * Idempotency of *delivery* is a different concern with a different owner, and
 * deliberately not here: notificationBurst records what has already been
 * announced, keyed by message id, and notificationPresentation's cross-tab
 * claim decides which tab announces it. Putting a memory inside this function
 * would make the same event resolve differently the second time, which is
 * exactly what a stable decision must not do.
 */
export function resolveNotificationClass(input: NotificationClassInput): NotificationClass {
  if (input.priority === "urgent") return "urgent";
  if (input.namesRecipient) return "mention";
  return isInConversation(input.attention) ? "in-conversation" : "message";
}
