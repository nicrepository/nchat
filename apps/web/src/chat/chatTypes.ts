// ── Chat domain types ────────────────────────────────────────────────────────

export type ChannelType = "public" | "private";

/**
 * The two ordering keys the sidebar sorts a section by (issue #414).
 *
 * `lastMessageAt` is when the newest message of this conversation was
 * persisted, as the server states it, and `null` when there is none. Null is
 * not "unknown" and not "very old": it is what puts a conversation nobody has
 * written in behind every conversation somebody has, however recently it was
 * created. The two fields therefore stay separate instead of collapsing into
 * one `lastMessageAt ?? createdAt`, which would let a brand-new empty
 * conversation outrank an active one.
 *
 * Both are optional so a payload from a server that predates the fields parses
 * unchanged: such items simply order by name and id, which is what they did
 * before. Neither is ever produced by this client — a browser clock has no
 * standing here.
 */
export interface ConversationActivity {
  /** Server timestamp of the newest persisted message; null when there is none. */
  lastMessageAt?: string | null;
  /** Server creation timestamp; the fallback key for conversations without messages. */
  createdAt?: string | null;
  /** Server-side preference timestamp; null when this user has not pinned the conversation. */
  pinnedAt?: string | null;
}

export interface Channel extends ConversationActivity {
  id: string;
  name: string;
  type: ChannelType;
  /** Server-derived permission. The forwarding endpoint remains authoritative. */
  canWrite: boolean;
  /**
   * Whether the server would accept a rename of this channel from the current
   * viewer (issue #527). Presentation only: it decides whether the row's action
   * menu offers "Renomear canal", and PATCH /api/chat/channels/{id} re-derives
   * the same decision from the session on every call. Optional so a payload
   * from a server that predates the field parses unchanged, and absent is read
   * as "no".
   */
  canRename?: boolean;
  /**
   * Whether this channel is the workspace's structural general channel
   * (issue #527). It is the server's `is_general`, never a comparison against
   * the visible name: a channel called "Geral" that is not the general one is
   * ordinary, and the general one renamed to anything else is still structural.
   *
   * The row menu reads it to omit rename, mute and leave. The backend refuses
   * all three for it regardless, in SQL.
   */
  isGeneral?: boolean;
  /** This viewer's own notification preference (issue #527). */
  muted?: boolean;
  unreadCount?: number;
  /** True once the unread count includes a message that mentions the current user. */
  hasMentionUnread?: boolean;
  categoryId?: string;
  categoryName?: string;
}

export interface ChannelCategory {
  id?: string;
  name: string;
  kind: "category" | "uncategorized";
}

export type DMType = "1:1" | "group";

/**
 * The wire values `chat.dm_conversations.type` is allowed to hold.
 *
 * The column is closed by CHECK to exactly these two, so anything else is not a
 * conversation type this build knows how to render — a newer server, a proxy
 * rewriting the payload, or a corrupted response. It is deliberately a lookup
 * and not a default: `value === "group" ? "group" : "1:1"` turns every unknown
 * value into a 1:1, which would hand an unrecognised conversation a profile
 * panel and a request for a counterpart that may not exist.
 */
export function parseDMConversationType(value: unknown): DMType | undefined {
  switch (value) {
    case "direct":
      return "1:1";
    case "group":
      return "group";
    default:
      return undefined;
  }
}

export type AvatarColor = "purple" | "green" | "blue" | "rose" | "amber" | "teal";

export type OnlineStatus = "online" | "away" | "offline";

export interface DMParticipant {
  id: string;
  displayName: string;
  initials: string;
  color: AvatarColor;
  status: OnlineStatus;
}

/**
 * The other participant of a 1:1 DM, as resolved by the server for the current
 * viewer. Present only for `type: "1:1"` conversations whose counterpart could
 * be resolved; groups never carry one. Presence/status is deliberately absent —
 * the backend does not track it, so the UI must not invent it.
 */
export interface DMCounterpart {
  userId: string;
  /** Already resolved server-side: full_name, else display_name, else fallback. */
  displayName: string;
  /** Absent when unset or when the stored URL is not a safe http(s) target. */
  avatarUrl?: string;
}

export interface DMConversation extends ConversationActivity {
  id: string;
  type: DMType;
  name: string;
  participants: DMParticipant[];
  counterpart?: DMCounterpart;
  unreadCount?: number;
  /** True once the unread count includes a message that mentions the current user. */
  hasMentionUnread?: boolean;
  /** This viewer's own notification preference (issue #527). */
  muted?: boolean;
}

/**
 * Splits the canonical DM list into the two sidebar sections it feeds.
 *
 * The only input is `type`, the server-derived discriminator persisted as
 * `chat.dm_conversations.type` (CHECK IN ('direct','group')) — never the title,
 * the avatar, the initials or how many participants happen to be visible.
 *
 * Each known type is matched explicitly and anything else is dropped. The
 * `default` is unreachable for a value that really is a `DMType`, but this
 * function is reachable from JSON that was cast rather than parsed, and an
 * `else` branch would silently file every unknown type under "Mensagens
 * diretas" — the one bucket whose entries get a profile panel. Losing a row the
 * UI cannot classify is the safe failure; misfiling it is not.
 */
export function partitionDMs(dms: DMConversation[]): {
  directs: DMConversation[];
  groups: DMConversation[];
} {
  const directs: DMConversation[] = [];
  const groups: DMConversation[] = [];
  for (const dm of dms) {
    switch (dm.type) {
      case "1:1":
        directs.push(dm);
        break;
      case "group":
        groups.push(dm);
        break;
      default:
        break;
    }
  }
  return { directs, groups };
}

export interface DMCandidate {
  userId: string;
  displayName: string;
}

export interface DirectDMResult {
  conversationId: string;
  created: boolean;
}

// ── Sidebar active selection ─────────────────────────────────────────────────

export type ActiveItem = { kind: "channel"; id: string } | { kind: "dm"; id: string } | null;

// ── Messages ─────────────────────────────────────────────────────────────────

export type MessageKind = "user" | "system";
/**
 * RF-21 adds a third state. `pending_link_scan` is a message the backend
 * accepted and is withholding while Cloudflare URL Scanner decides about the
 * links in it: only its own sender ever receives one, in the create response,
 * and nobody else is shown it until the backend promotes it.
 *
 * It is not a client-side decision. The frontend renders this state; it never
 * infers it, and it cannot clear it.
 */
export type MessageStatus = "active" | "deleted" | "pending_link_scan";

/**
 * RF-21 link-safety axis, independent of MessageStatus (issue #135).
 *
 * `status` answers "does this message exist for readers". This answers "what is
 * known about the links it carries", and the two are deliberately separate: a
 * published message may carry links the provider could not produce a verdict for,
 * and refusing the message on that was the bug this replaced.
 *
 * - `""` — nothing to say. The message carries no links, or predates the axis.
 * - `"safe"` — every link holds a current clearance.
 * - `"inconclusive"` — at least one scan finished without a usable verdict, and
 *   none was malicious. The message is published normally and its content is
 *   rendered exactly as any other; a notice above it says the automatic preview
 *   could not be loaded. It does **not** mean the link is dangerous, and the copy
 *   must never say so.
 * - `"malicious"` — at least one link was condemned. Reachable on a message that
 *   is already on screen, because a later reconciliation may prove a link
 *   malicious after it was delivered.
 *
 * Nothing in the client decides this value; it is rendered, never inferred. In
 * particular the client never fetches a link to find out — see MessageBubble.
 */
export type MessageLinkSafety = "" | "safe" | "inconclusive" | "malicious" | "unknown";

/**
 * The states the server actually persists. `unknown` is not one of them — it is a
 * local reading, produced by the decoder when the server said something this
 * build does not understand.
 */
const persistedLinkSafetyStates = ["", "safe", "inconclusive", "malicious"] as const;

/**
 * Narrows an unknown server value to a MessageLinkSafety.
 *
 * An unrecognised value becomes `"unknown"`, which authorises nothing — **not**
 * `"inconclusive"`, which would authorise an anchor.
 *
 * That distinction is the whole of CQ-004. "The provider ran a scan and produced
 * no usable verdict" and "this build does not recognise what the server said" are
 * different facts with different safe answers. Collapsing the second into the
 * first meant a future server state — a rollout of a build that knows more than
 * this one — would silently grant clickable links on a state nobody here has
 * reasoned about.
 *
 * The fail-closed direction is a distinct value that no rendering rule admits.
 */
export function normalizeLinkSafety(raw?: unknown): MessageLinkSafety {
  if (raw === undefined || raw === null || raw === "") return "";
  return (persistedLinkSafetyStates as readonly string[]).includes(raw as string)
    ? (raw as MessageLinkSafety)
    : "unknown";
}

/**
 * The one place that decides whether a message's links may be drawn as anchors.
 *
 * An allowlist of exactly two states, and it is exported so the bubble, the tests
 * and any future surface all ask the same question rather than each re-deriving
 * it:
 *
 *	safe          checked, cleared            -> anchor
 *	inconclusive  checked, no usable verdict  -> anchor, with the notice
 *	malicious     checked, condemned          -> no anchor
 *	"" (legacy)   never checked               -> no anchor
 *	unknown       not understood by this build -> no anchor
 *
 * Nothing derived from `status` belongs here: a published message is not a
 * verified one.
 */
export function linkSafetyAllowsAnchors(state: MessageLinkSafety | undefined): boolean {
  return state === "safe" || state === "inconclusive";
}

/**
 * What a "Verificar novamente" attempt reports back to the UI (issue #135).
 *
 * `state` is the message's authoritative link-safety state afterwards, and
 * `retryAfterSeconds` is how long the server's own cooldown runs — the UI
 * disables the button for that long rather than offering an action it knows will
 * be refused. A failed attempt rejects rather than resolving to this, so both
 * fields are always present.
 */
export interface LinkSafetyRecheck {
  state: MessageLinkSafety;
  updatedAt: string;
  retryAfterSeconds: number;
}

export type MessageBodyFormat = "v1" | "v2" | "v3";

export function normalizeBodyFormat(raw?: string): MessageBodyFormat {
  return raw === "v3" ? "v3" : raw === "v2" ? "v2" : "v1";
}

export interface MentionCandidate {
  /** "all" is synthesized client-side (see mentionExtension.tsx) — never fetched from the server. */
  mentionType: "user" | "all";
  id: string;
  label: string;
}

/**
 * The server-generated conversation events a system message can describe
 * (issue #527). A closed set: an event this build does not know is rendered as
 * nothing rather than guessed at.
 */
export type ConversationEventType = "conversation_renamed" | "conversation_member_left";

/**
 * The structured facts a system message carries. Deliberately no actor name —
 * the actor is the message's own sender, resolved through the same authorized
 * projection every other message's sender goes through, so nothing a client
 * sends can put a name here.
 */
export interface ConversationEventPayload {
  oldName?: string;
  newName?: string;
}

export interface Message {
  id: string;
  senderId: string;
  senderDisplayName: string;
  senderEmail: string;
  /**
   * The sender's avatar (issue #495). Absent when unset or when the stored
   * URL is not a safe same-origin target — same policy as every other
   * avatarUrl in this module (sidebar counterpart, channel members, group
   * participants, direct profile), applied by the same safeAvatarUrl.
   */
  senderAvatarUrl?: string;
  kind: MessageKind;
  /**
   * Set only on `kind: "system"` (issue #527). The database enforces that
   * pairing, so a user message can never carry one.
   */
  eventType?: ConversationEventType;
  eventPayload?: ConversationEventPayload;
  /** Only present when status is "active". Empty for removed messages. */
  bodyText: string;
  bodyFormat: MessageBodyFormat;
  isRemoved: boolean;
  status: MessageStatus;
  /**
   * RF-21 link-safety state (issue #135). Independent of `status`: an
   * `active` message may be `"inconclusive"`, which is what draws the notice.
   *
   * Optional, and absent means exactly what `""` means — nothing to say about
   * links. That is safe in the only direction that matters: no state, and no
   * state this client does not understand, ever authorises anything. See
   * normalizeLinkSafety, which resolves an unrecognised server value to
   * `"unknown"` — a state that authorises nothing.
   */
  linkSafetyState?: MessageLinkSafety;
  deletedAt?: string | null;
  createdAt: string; // ISO 8601
  updatedAt: string; // ISO 8601
  isEdited: boolean;
  editCount: number;
  editedAt?: string;
  reactions: MessageReaction[];
  /** True when the current user favorited this message (RF-06, private per user). */
  isFavorited: boolean;
  /** Server-derived RF-08 snapshot marker; source provenance is intentionally hidden. */
  isForwarded: boolean;
  /** Immediate parent preview for RF-07 quote-reply. One level only. */
  quoted?: QuotedMessage;
  /** RF-09 cross-target reference, resolved for the current reader. */
  reference?: MessageReference;
  /**
   * RF-32 files bound to this message. Absent for every message that carries
   * none, and withheld entirely for a removed one.
   *
   * The element type is ChannelAttachment, the same shape the details panel
   * lists, so AttachmentThumbnail, AttachmentVideo and useAttachmentPreview
   * work here unchanged and the scan gates are not written a second time.
   */
  attachments?: ChannelAttachment[];
}

export interface MessageEditHistoryEntry {
  body: string;
  bodyFormat: number;
  versionedAt: string;
}

export interface MessageReaction {
  emoji: string;
  count: number;
  reactedByMe: boolean;
  /**
   * A bounded prefix of the people behind `count` (issue #496) — enough to name
   * the two the tooltip spells out, whoever the reader is. The rest are
   * summarised from `count`, so this is never the whole set.
   */
  users: ReactionUser[];
}

/** The only identity a reaction carries: who, and what to call them. */
export interface ReactionUser {
  userId: string;
  displayName: string;
}

/**
 * The named reactors of one aggregate (issue #496), from either transport.
 *
 * Defensive because it renders straight into a tooltip other people read: an
 * entry without both an id and a name is dropped rather than shown as an empty
 * label, and a server that sends no users at all yields an empty list, which the
 * formatter reads as "count only".
 */
export function parseReactionUsers(value: unknown): ReactionUser[] {
  if (!Array.isArray(value)) return [];
  return value.filter(isReactionUserResponse).map((user) => ({
    userId: user.user_id,
    displayName: user.display_name,
  }));
}

function isReactionUserResponse(
  value: unknown,
): value is { user_id: string; display_name: string } {
  if (typeof value !== "object" || value === null) return false;
  const user = value as Record<string, unknown>;
  return (
    typeof user.user_id === "string" &&
    user.user_id !== "" &&
    typeof user.display_name === "string" &&
    user.display_name !== ""
  );
}

export interface QuotedMessage {
  id: string;
  authorId: string;
  /** Empty when the original message is removed or inaccessible. */
  bodyText: string;
  bodyFormat: MessageBodyFormat;
  isRemoved: boolean;
  deletedAt: string | null;
  createdAt: string;
  updatedAt?: string;
  /**
   * The quoted message's own link-safety marker (issue #135, CQ-002). The
   * server already withholds `bodyText` when this is "malicious"; this is what
   * lets the quote say why instead of rendering an empty block.
   */
  linkSafetyState: MessageLinkSafety;
}

export type MessageReference =
  | { available: false }
  | {
      available: true;
      messageId: string;
      targetType: "channel" | "dm";
      targetId: string;
      targetLabel: string;
      authorDisplayName: string;
      bodyText: string;
      bodyFormat: MessageBodyFormat;
      createdAt: string;
      updatedAt?: string;
      linkSafetyState: MessageLinkSafety;
    };

export interface MessagePage {
  messages: Message[];
  /** Opaque cursor; non-empty when an older page is available. */
  nextCursor: string;
}

export type MessageSecuritySnapshot =
  | { messageId: string; available: false }
  | {
      messageId: string;
      available: true;
      status: MessageStatus;
      linkSafetyState: MessageLinkSafety;
      updatedAt: string;
      quoted?: {
        messageId: string;
        status: MessageStatus;
        linkSafetyState: MessageLinkSafety;
        updatedAt: string;
      };
    };

// ── Favorites (RF-06) ────────────────────────────────────────────────────────

export interface FavoriteItem {
  message: Message;
  /** Non-empty when the message belongs to a channel. */
  channelId: string;
  /** Non-empty when the message belongs to a DM conversation. */
  dmConversationId: string;
  favoritedAt: string; // ISO 8601
}

export interface FavoritesPage {
  favorites: FavoriteItem[];
  /** Opaque cursor; non-empty when an older page is available. */
  nextCursor: string;
}

// ── Pins (RF-05) ─────────────────────────────────────────────────────────────

export interface PinnedItem {
  message: Message;
  /** User who pinned the message. */
  pinnedByUserId: string;
  pinnedAt: string; // ISO 8601
}

// ── Channel details panel (issue #435) ───────────────────────────────────────

export type ChannelMemberRole = "member" | "moderator";

/**
 * One member of a channel, as the details panel renders them.
 *
 * `presence` is what the server asserts about this member. Every entry the
 * server puts in `onlineMembers` carries "online"; the field is kept rather
 * than implied so the client can verify the claim instead of trusting the
 * list's name. It is absent when the server states nothing — never defaulted
 * to a status the backend did not send.
 *
 * `role` is the channel role, the only complementary attribute the domain has:
 * there is no job title or department anywhere in auth.users.
 */
export interface ChannelMemberProfile {
  userId: string;
  displayName: string;
  /** Absent when unset or when the stored URL is not a safe same-origin target. */
  avatarUrl?: string;
  role: ChannelMemberRole;
  presence?: OnlineStatus;
}

/**
 * The channel-details payload.
 *
 * The three member figures are independent and none is derived from another:
 *  - `memberCount` is every active member of the channel, online or not — the
 *    channel's size, which does not change when someone disconnects;
 *  - `onlineCount` is how many of those are online right now, and may exceed
 *    `onlineMembers.length` when more are online than the preview shows;
 *  - `onlineMembers` is the capped preview, filtered by presence server-side
 *    *before* the limit is applied, so an offline member never takes a slot
 *    from an online one.
 *
 * `description` is deliberately not a field: chat.channels has no description
 * column, so the panel renders its empty state rather than a value nothing can
 * produce.
 */
export interface ChannelDetails {
  id: string;
  slug: string;
  name: string;
  type: ChannelType;
  createdAt: string; // ISO 8601
  memberCount: number;
  onlineCount: number;
  onlineMembers: ChannelMemberProfile[];
  /**
   * Whether the server would let this caller add participants (issue #398).
   *
   * A rendering hint, never a control: `POST .../members` re-derives the same
   * decision from the session on every call, so hiding the action protects
   * nobody and showing it grants nothing. It is normalized with a strict
   * `=== true`, so an absent or malformed field leaves it false — a server that
   * predates this field hides the action rather than enabling it.
   */
  canManageMembers: boolean;
}

// ── Add members (issue #398) ─────────────────────────────────────────────────

/**
 * What one add-members call actually changed, as the server reports it.
 *
 * `added` and `alreadyMembers` are separate so a retry stays legible: repeating
 * an identical request reports the same people under `alreadyMembers` and adds
 * nothing, which is neither a fresh success nor a failure.
 *
 * `memberCount` is the authoritative post-commit total. The panel sets its
 * counter from it rather than incrementing a local number, so a concurrent add
 * by someone else does not leave the two views disagreeing.
 */
export interface AddMembersResult {
  added: number;
  alreadyMembers: number;
  memberCount: number;
}

/**
 * One participant of a group conversation, as the details panel renders them.
 *
 * Deliberately without a role: chat.dm_members.role is closed by CHECK to
 * 'member', so a group has no role to show. `presence` is decoration — it says
 * whether this participant is connected and never decides whether they appear.
 */
export interface GroupParticipantProfile {
  userId: string;
  displayName: string;
  /** Absent when unset or when the stored URL is not a safe same-origin target. */
  avatarUrl?: string;
  presence?: OnlineStatus;
}

/**
 * The group-details payload.
 *
 * A group is a `chat.dm_conversations` row of type 'group', not a channel, so
 * this carries no visibility, slug, category or description — the domain has
 * none of them for conversations and none is invented here.
 *
 * `participantCount` is every active participant and is never
 * `participants.length`: that array is a capped preview.
 */
export interface GroupDetails {
  id: string;
  name: string;
  createdAt: string; // ISO 8601
  participantCount: number;
  participants: GroupParticipantProfile[];
  /** Same meaning and the same strict normalization as ChannelDetails' (issue #398). */
  canManageMembers: boolean;
}

/**
 * The other participant of a 1:1 DM, as the profile panel renders them
 * (issue #443).
 *
 * Every field beyond `userId` and `displayName` is optional because every one
 * of them can genuinely be missing, and absent means absent:
 *  - `avatarUrl` is dropped when unset or when the stored URL is not a safe
 *    same-origin target, and the initials fallback renders;
 *  - `presence` is absent when the server does not track it, which the panel
 *    must not read as "offline";
 *  - `email` is absent when the server did not send one;
 *  - `jobTitle`, `department` and `timezone` have no column anywhere in the
 *    domain today, so the server never sends them. They are declared because
 *    they are the prototype's rows and the panel renders "Não informado" for
 *    each — the decision of what the row says lives in one place, and the day a
 *    column exists the value flows through with no change here.
 *
 * `timezone` is an IANA name and is validated before use: an unknown or
 * malformed one is treated as missing rather than fed to Intl.
 */
export interface DirectProfile {
  userId: string;
  displayName: string;
  avatarUrl?: string;
  presence?: OnlineStatus;
  email?: string;
  jobTitle?: string;
  department?: string;
  timezone?: string;
}

/**
 * The 1:1 profile payload.
 *
 * Deliberately not a conversation projection: a direct conversation has no
 * name, no visibility, no participant count and no roster worth showing — it
 * has one other person.
 *
 * `kind` is part of the value rather than a tag the hook adds afterwards. The
 * server states the variant, the client verifies it, and only a verified
 * response can be constructed — so a payload that says "group" can never be
 * relabelled as a profile on its way through. `conversationId` is likewise the
 * value the server sent *after* it was checked against the conversation that
 * was requested, never the request argument echoed back, which is what makes a
 * misrouted response detectable instead of invisible.
 */
export interface DirectDetails {
  kind: "direct";
  conversationId: string;
  profile: DirectProfile;
}

/**
 * What the details panel is describing right now.
 *
 * The three shapes are discriminated by `kind`, mirroring the domain: a
 * channel, a group and a 1:1 conversation are different aggregates with
 * different vocabulary, so the panel switches on the tag rather than on
 * optional fields that only one of them ever populates. In particular the
 * direct variant carries no `participants`, which is what stops a profile
 * panel from ever rendering a two-person roster.
 */
export type ConversationDetails =
  | ({ kind: "channel" } & ChannelDetails)
  | ({ kind: "group" } & GroupDetails)
  // Already tagged by its own client, which validated the tag the server sent
  // instead of asserting one — see DirectDetails.
  | DirectDetails;

/** Scan lifecycle of an attachment. Only "clean" is ever downloadable. */
export type AttachmentStatus = "pending_scan" | "clean" | "rejected";

/**
 * Inline preview lifecycle (RF-31).
 *
 * It is what decides which of three things the UI draws, and the three are
 * deliberately distinguishable:
 *
 * - `pending`: a preview is being produced; show a placeholder and re-read the
 *   metadata later. The upload never waits for this.
 * - `ready`: a preview exists and may be requested. It is still subject to the
 *   attachment's own `status`: nothing that is not `clean` is served.
 * - `unsupported`: there will never be a preview for this content — an expected
 *   absence, not an error.
 * - `failed`: a preview could not be produced. The user sees the same fallback
 *   as `unsupported`; the difference matters to operators, not to the UI.
 *
 * The fallback for the last three is identical and is the whole contract: the
 * file icon and the download action, which never depended on a preview.
 */
export type AttachmentPreviewStatus = "pending" | "ready" | "unsupported" | "failed";

export interface ChannelAttachment {
  id: string;
  filename: string;
  contentType: string;
  /** Plaintext size in bytes. */
  size: number;
  status: AttachmentStatus;
  previewStatus: AttachmentPreviewStatus;
  /**
   * ISO 8601, or empty when the source does not publish one — a message's
   * attachments are dated by the message itself, so chat-service does not
   * repeat the timestamp. Every reader already treats "" as "no date to show".
   */
  createdAt: string;
}

/**
 * Accepts only the statuses the contract defines. An unknown value degrades to
 * "pending_scan" — the conservative reading, since the UI keys "not
 * downloadable" off anything that is not "clean" and must never promote an
 * unrecognised state to clean.
 *
 * Shared by both clients that parse attachments: file-service's listing
 * (filesApi) and chat-service's message payloads (chatApi). One parser means
 * the two can never disagree about what an unknown status means.
 */
export function parseAttachmentStatus(raw: unknown): AttachmentStatus {
  return raw === "clean" || raw === "rejected" ? raw : "pending_scan";
}

/**
 * Accepts only the four states the preview contract defines. Anything else — an
 * older server that publishes no field at all, or a state this build does not
 * know — degrades to "unsupported", which is the conservative reading: the UI
 * shows the icon and the download action, and never promises a preview that may
 * not exist.
 */
export function parseAttachmentPreviewStatus(raw: unknown): AttachmentPreviewStatus {
  return raw === "pending" || raw === "ready" || raw === "failed" ? raw : "unsupported";
}

interface MessageAttachmentResponse {
  id?: unknown;
  filename?: unknown;
  content_type?: unknown;
  size?: unknown;
  status?: unknown;
  preview_status?: unknown;
}

/**
 * Maps one attachment of a message (RF-32).
 *
 * The wire shape is chat-service's snake_case, and the result is the same
 * ChannelAttachment the details panel renders — which is what lets the timeline
 * reuse AttachmentThumbnail, AttachmentVideo and useAttachmentPreview instead of
 * restating the scan rules. A row without a usable id is dropped rather than
 * rendered as a file nothing can identify.
 *
 * createdAt is empty: a message's attachment is dated by the message.
 */
function parseMessageAttachment(raw: unknown): ChannelAttachment | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const item = raw as MessageAttachmentResponse;
  if (typeof item.id !== "string" || item.id === "") return undefined;
  return {
    id: item.id,
    filename: typeof item.filename === "string" ? item.filename : "",
    contentType: typeof item.content_type === "string" ? item.content_type : "",
    size: typeof item.size === "number" && Number.isFinite(item.size) ? item.size : 0,
    status: parseAttachmentStatus(item.status),
    previewStatus: parseAttachmentPreviewStatus(item.preview_status),
    createdAt: "",
  };
}

/**
 * Parses the attachment list of a message payload, from HTTP or from a
 * WebSocket event — one parser, so the two can never describe the same file
 * differently. Anything that is not a usable array yields undefined, which is
 * also what a text-only message and a pre-RF-32 server look like.
 */
export function parseMessageAttachments(raw: unknown): ChannelAttachment[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const attachments = raw
    .map(parseMessageAttachment)
    .filter((attachment): attachment is ChannelAttachment => attachment !== undefined);
  return attachments.length > 0 ? attachments : undefined;
}
