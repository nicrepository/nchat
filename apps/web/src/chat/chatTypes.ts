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
 * A single pass pushing each conversation into exactly one bucket is what makes
 * "every item in exactly one section, none duplicated, none dropped" a property
 * of the construction rather than of two filters agreeing with each other.
 */
export function partitionDMs(dms: DMConversation[]): {
  directs: DMConversation[];
  groups: DMConversation[];
} {
  const directs: DMConversation[] = [];
  const groups: DMConversation[] = [];
  for (const dm of dms) {
    (dm.type === "group" ? groups : directs).push(dm);
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
