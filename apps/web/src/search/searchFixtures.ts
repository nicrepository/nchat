/** Test-only factories for search results (issue #900). Never imported by the app. */

import type {
  ChannelSearchResult,
  FileSearchResult,
  GroupSearchResult,
  LinkSearchResult,
  MessageSearchResult,
  SearchResultPage,
  UserSearchResult,
} from "./searchTypes";

export function resultPage<T>(items: T[], nextCursor: string | null = null): SearchResultPage<T> {
  return { items, nextCursor, hasMore: nextCursor !== null };
}

export function messageResult(overrides: Partial<MessageSearchResult> = {}): MessageSearchResult {
  return {
    id: "m1",
    conversation: { kind: "channel", id: "c1", type: "public", name: "infraestrutura" },
    senderId: "u1",
    senderDisplayName: "Juliane Lino",
    senderAvatarUrl: null,
    bodyText: "Checklist de backup semanal atualizado",
    createdAt: "2026-09-01T09:41:00Z",
    score: 1,
    ...overrides,
  };
}

export function userResult(overrides: Partial<UserSearchResult> = {}): UserSearchResult {
  return { id: "u1", displayName: "Juliane Lino", avatarUrl: null, ...overrides };
}

export function channelResult(overrides: Partial<ChannelSearchResult> = {}): ChannelSearchResult {
  return {
    id: "c1",
    slug: "infraestrutura",
    displayName: "infraestrutura",
    isPrivate: false,
    description: null,
    memberCount: 42,
    isGeneral: false,
    ...overrides,
  };
}

export function groupResult(overrides: Partial<GroupSearchResult> = {}): GroupSearchResult {
  return {
    id: "g1",
    title: "Projeto NChat",
    participantCount: 6,
    lastMessageAt: null,
    ...overrides,
  };
}

export function fileResult(overrides: Partial<FileSearchResult> = {}): FileSearchResult {
  return {
    id: "f1",
    filename: "relatorio-backup.pdf",
    contentType: "application/pdf",
    size: 2_516_582,
    status: "clean",
    previewStatus: "available",
    messageId: "m9",
    conversation: { kind: "channel", id: "c1", type: "public", name: "infraestrutura" },
    createdAt: "2026-09-01T09:41:00Z",
    ...overrides,
  };
}

export function linkResult(overrides: Partial<LinkSearchResult> = {}): LinkSearchResult {
  return {
    id: "m7:abababababababababababababababab",
    messageId: "m7",
    url: "https://docs.example.com/runbook",
    hostname: "docs.example.com",
    conversation: { kind: "channel", id: "c1", type: "public", name: "infraestrutura" },
    senderId: "u1",
    senderDisplayName: "Juliane Lino",
    createdAt: "2026-09-01T09:41:00Z",
    ...overrides,
  };
}
