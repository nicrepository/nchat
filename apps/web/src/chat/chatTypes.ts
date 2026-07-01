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
export type MessageBodyFormat = "v1" | "v2";

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
}

export interface MessagePage {
  messages: Message[];
  /** Opaque cursor; non-empty when an older page is available. */
  nextCursor: string;
}
