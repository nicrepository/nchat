// ── Chat domain types ────────────────────────────────────────────────────────

export type ChannelType = "public" | "private";

export interface Channel {
  id: string;
  name: string;
  type: ChannelType;
  /** Server-derived permission. The forwarding endpoint remains authoritative. */
  canWrite: boolean;
  unreadCount?: number;
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

export interface DMConversation {
  id: string;
  type: DMType;
  name: string;
  participants: DMParticipant[];
  counterpart?: DMCounterpart;
  unreadCount?: number;
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

// ── Current user (sidebar footer) ───────────────────────────────────────────

export interface CurrentUser {
  displayName: string;
  initials: string;
  color: AvatarColor;
  role: string;
}

// ── Sidebar active selection ─────────────────────────────────────────────────

export type ActiveItem = { kind: "channel"; id: string } | { kind: "dm"; id: string } | null;

// ── Messages ─────────────────────────────────────────────────────────────────

export type MessageKind = "user" | "system";
export type MessageStatus = "active" | "deleted";
export type MessageBodyFormat = "v1" | "v2" | "v3";

export function normalizeBodyFormat(raw?: string): MessageBodyFormat {
  return raw === "v3" ? "v3" : raw === "v2" ? "v2" : "v1";
}

export interface MentionCandidate {
  mentionType: "user" | "channel";
  id: string;
  label: string;
}

export interface Message {
  id: string;
  senderId: string;
  senderDisplayName: string;
  senderEmail: string;
  kind: MessageKind;
  /** Only present when status is "active". Empty for removed messages. */
  bodyText: string;
  bodyFormat: MessageBodyFormat;
  isRemoved: boolean;
  status: MessageStatus;
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
    };

export interface MessagePage {
  messages: Message[];
  /** Opaque cursor; non-empty when an older page is available. */
  nextCursor: string;
}

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

export interface ChannelAttachment {
  id: string;
  filename: string;
  contentType: string;
  /** Plaintext size in bytes. */
  size: number;
  status: AttachmentStatus;
  createdAt: string; // ISO 8601
}
