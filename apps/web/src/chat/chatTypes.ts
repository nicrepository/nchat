// ── Chat domain types ────────────────────────────────────────────────────────

export type ChannelType = "public" | "private";

export interface Channel {
  id: string;
  name: string;
  type: ChannelType;
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

export interface DMConversation {
  id: string;
  type: DMType;
  name: string;
  participants: DMParticipant[];
  unreadCount?: number;
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
  createdAt: string; // ISO 8601
  updatedAt: string; // ISO 8601
  reactions: MessageReaction[];
  /** True when the current user favorited this message (RF-06, private per user). */
  isFavorited: boolean;
}

export interface MessageReaction {
  emoji: string;
  count: number;
  reactedByMe: boolean;
}

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
