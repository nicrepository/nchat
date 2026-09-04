/**
 * ChatMessageArea tests.
 *
 * All API calls are mocked at the chatApi module level — no runtime fixtures.
 * The component itself is the unit under test.
 */

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { MemoryRouter, Outlet, Route, Routes, useLocation, useNavigate } from "react-router";

import { ApiRequestError } from "../lib/api";

import type {
  ActiveDirectCallSession,
  ActiveResourceCallSession,
  ChatOutletContext,
} from "./ChatShell";
import type { Call } from "./callState";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import { flushResizeObservers, observedElements } from "../setupTests";
import ChatMessageArea from "./ChatMessageArea";
import { isCatalogedEmoji, loadEmojiCatalog, resetEmojiCatalogCache } from "./emoji/emojiCatalog";
import { avatarColorFor } from "./messageDisplay";
import type { Message, MessagePage } from "./chatTypes";
import type {
  WSClientErrorEvent,
  WSMessageCreatedEvent,
  WSMessageUpdatedEvent,
  WSReactionUpdatedEvent,
  WSSubscribedEvent,
  WSTypingUpdatedEvent,
} from "./useChatWebSocket";
import { useChatWebSocket } from "./useChatWebSocket";
import * as chatApi from "./chatApi";

// ── TipTap public-event helper ────────────────────────────────────────────────

async function fillEditor(element: HTMLElement, text: string) {
  fireEvent.paste(element, {
    clipboardData: {
      getData: (type: string) => (type === "text/plain" ? text : ""),
      types: ["text/plain"],
      files: [],
    },
  });
  await waitFor(() => expect(element).toHaveTextContent(text));
}

async function replaceEditorText(element: HTMLElement, text: string) {
  element.focus();
  await userEvent.keyboard("{Control>}a{/Control}{Backspace}");
  await fillEditor(element, text);
}

// ── Mock chatApi ──────────────────────────────────────────────────────────────

const {
  mockFetchChannelMessages,
  mockFetchChannelMessage,
  mockFetchChannelMessageSecuritySnapshots,
  mockPostChannelMessage,
  mockForwardChannelMessage,
  mockFetchDMMessages,
  mockFetchDMMessage,
  mockFetchDMMessageSecuritySnapshots,
  mockPostDMMessage,
  mockFetchAllowedReactionEmojis,
  mockFavoriteMessage,
  mockUnfavoriteMessage,
  mockFetchPins,
  mockPinMessage,
  mockUnpinMessage,
  mockEditMessage,
  mockDeleteMessage,
  mockGetMessageHistory,
  mockFetchChannelDetails,
  mockFetchGroupDetails,
  mockFetchDirectProfile,
  mockFetchChannelAttachments,
  mockGetOrCreateDirectDM,
  wsMockState,
} = vi.hoisted(() => ({
  mockFetchChannelMessages:
    vi.fn<
      (channelId: string, beforeCursor?: string, signal?: AbortSignal) => Promise<MessagePage>
    >(),
  mockFetchChannelMessage:
    vi.fn<(channelId: string, messageId: string, signal?: AbortSignal) => Promise<Message>>(),
  mockFetchChannelMessageSecuritySnapshots: vi.fn(),
  mockPostChannelMessage:
    vi.fn<
      (
        channelId: string,
        bodyText: string,
        parentMessageId?: string,
        referencedMessageId?: string,
        signal?: AbortSignal,
      ) => Promise<Message>
    >(),
  mockForwardChannelMessage:
    vi.fn<
      (
        destinationChannelId: string,
        sourceMessageId: string,
        idempotencyKey: string,
        signal?: AbortSignal,
      ) => Promise<Message>
    >(),
  mockFetchDMMessages:
    vi.fn<
      (conversationId: string, beforeCursor?: string, signal?: AbortSignal) => Promise<MessagePage>
    >(),
  mockFetchDMMessage:
    vi.fn<(conversationId: string, messageId: string, signal?: AbortSignal) => Promise<Message>>(),
  mockFetchDMMessageSecuritySnapshots: vi.fn(),
  mockPostDMMessage:
    vi.fn<
      (
        conversationId: string,
        bodyText: string,
        parentMessageId?: string,
        referencedMessageId?: string,
        signal?: AbortSignal,
      ) => Promise<Message>
    >(),
  mockFetchAllowedReactionEmojis: vi.fn<() => Promise<string[]>>(),
  mockFavoriteMessage: vi.fn<(id: string) => Promise<void>>(),
  mockUnfavoriteMessage: vi.fn<(id: string) => Promise<void>>(),
  mockFetchPins:
    vi.fn<
      (target: { kind: "channel" | "dm"; id: string }, signal?: AbortSignal) => Promise<unknown[]>
    >(),
  mockPinMessage:
    vi.fn<(target: { kind: "channel" | "dm"; id: string }, messageId: string) => Promise<void>>(),
  mockUnpinMessage:
    vi.fn<(target: { kind: "channel" | "dm"; id: string }, messageId: string) => Promise<void>>(),
  mockEditMessage:
    vi.fn<(messageId: string, body: string, bodyFormat: number) => Promise<Message>>(),
  mockDeleteMessage: vi.fn<(messageId: string) => Promise<Message>>(),
  mockGetMessageHistory: vi.fn(),
  mockFetchChannelDetails: vi.fn(),
  mockFetchGroupDetails: vi.fn(),
  mockFetchDirectProfile: vi.fn(),
  mockFetchChannelAttachments: vi.fn(),
  mockGetOrCreateDirectDM:
    vi.fn<
      (
        otherUserId: string,
        signal?: AbortSignal,
      ) => Promise<{ conversationId: string; created: boolean }>
    >(),
  wsMockState: {
    capturedWSMessageCreated: null as ((event: WSMessageCreatedEvent) => void) | null,
    capturedWSMessageUpdated: null as ((event: WSMessageUpdatedEvent) => void) | null,
    capturedReactionUpdated: null as ((event: WSReactionUpdatedEvent) => void) | null,
    capturedReactionError: null as ((event: WSClientErrorEvent) => void) | null,
    capturedSubscribed: null as ((event: WSSubscribedEvent) => void) | null,
    capturedOnTypingUpdated: null as ((event: WSTypingUpdatedEvent) => void) | null,
    toggleReaction: vi.fn(() => true),
    sendTyping: vi.fn(() => true),
  },
}));

vi.mock("./chatApi", () => ({
  MessageEditError: class MessageEditError extends Error {
    readonly status: number;
    readonly reason: string;

    constructor(status: number, reason: string, message: string) {
      super(message);
      this.name = "MessageEditError";
      this.status = status;
      this.reason = reason;
    }
  },
  fetchSidebarData: vi.fn(),
  fetchChannels: vi.fn(),
  fetchDMs: vi.fn(),
  fetchChannelMessages: (channelId: string, beforeCursor?: string, signal?: AbortSignal) =>
    mockFetchChannelMessages(channelId, beforeCursor, signal),
  fetchChannelMessage: mockFetchChannelMessage,
  fetchChannelMessageSecuritySnapshots: mockFetchChannelMessageSecuritySnapshots,
  postChannelMessage: (
    channelId: string,
    bodyText: string,
    parentMessageId?: string,
    referencedMessageId?: string,
    signal?: AbortSignal,
  ) => mockPostChannelMessage(channelId, bodyText, parentMessageId, referencedMessageId, signal),
  forwardChannelMessage: (
    destinationChannelId: string,
    sourceMessageId: string,
    idempotencyKey: string,
    signal?: AbortSignal,
  ) => mockForwardChannelMessage(destinationChannelId, sourceMessageId, idempotencyKey, signal),
  fetchDMMessages: (conversationId: string, beforeCursor?: string, signal?: AbortSignal) =>
    mockFetchDMMessages(conversationId, beforeCursor, signal),
  postDMMessage: (
    conversationId: string,
    bodyText: string,
    parentMessageId?: string,
    referencedMessageId?: string,
    signal?: AbortSignal,
  ) => mockPostDMMessage(conversationId, bodyText, parentMessageId, referencedMessageId, signal),
  fetchDMMessage: mockFetchDMMessage,
  fetchDMMessageSecuritySnapshots: mockFetchDMMessageSecuritySnapshots,
  fetchAllowedReactionEmojis: mockFetchAllowedReactionEmojis,
  favoriteMessage: (id: string) => mockFavoriteMessage(id),
  unfavoriteMessage: (id: string) => mockUnfavoriteMessage(id),
  fetchPins: (target: { kind: "channel" | "dm"; id: string }, signal?: AbortSignal) =>
    mockFetchPins(target, signal),
  pinMessage: (target: { kind: "channel" | "dm"; id: string }, messageId: string) =>
    mockPinMessage(target, messageId),
  unpinMessage: (target: { kind: "channel" | "dm"; id: string }, messageId: string) =>
    mockUnpinMessage(target, messageId),
  editMessage: (messageId: string, body: string, bodyFormat: number) =>
    mockEditMessage(messageId, body, bodyFormat),
  deleteMessage: (messageId: string) => mockDeleteMessage(messageId),
  getMessageHistory: (...args: unknown[]) => mockGetMessageHistory(...args),
  fetchChannelDetails: (channelId: string, signal?: AbortSignal) =>
    mockFetchChannelDetails(channelId, signal),
  fetchGroupDetails: (conversationId: string, signal?: AbortSignal) =>
    mockFetchGroupDetails(conversationId, signal),
  fetchDirectProfile: (conversationId: string, signal?: AbortSignal) =>
    mockFetchDirectProfile(conversationId, signal),
  getOrCreateDirectDM: (otherUserId: string, signal?: AbortSignal) =>
    mockGetOrCreateDirectDM(otherUserId, signal),
}));

// file-service lives behind its own client; the panel's files section is mocked
// here exactly like the chat endpoints above.
vi.mock("./filesApi", () => ({
  fetchConversationAttachments: (
    target: { kind: "channel" | "dm"; id: string },
    limit: number,
    signal?: AbortSignal,
  ) => mockFetchChannelAttachments(target, limit, signal),
  // The panel's file rows render a thumbnail and a video player. Neither is
  // exercised here — no fixture is previewable or a video — but both modules
  // read the export at render time, so it has to exist.
  fetchAttachmentPreview: () => Promise.reject(new Error("not used")),
  fetchAttachmentContent: () => Promise.reject(new Error("not used")),
}));

// useChatWebSocket is a no-op in component tests — WS behaviour is tested in
// useChatWebSocket.test.ts.
vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: vi.fn(
    ({
      onMessageCreated,
      onMessageUpdated,
      onReactionUpdated,
      onReactionError,
      onSubscribed,
      onTypingUpdated,
    }: {
      onMessageCreated: (event: WSMessageCreatedEvent) => void;
      onMessageUpdated?: (event: WSMessageUpdatedEvent) => void;
      onReactionUpdated?: (event: WSReactionUpdatedEvent) => void;
      onReactionError?: (event: WSClientErrorEvent) => void;
      onSubscribed?: (event: WSSubscribedEvent) => void;
      onTypingUpdated?: (event: WSTypingUpdatedEvent) => void;
    }) => {
      wsMockState.capturedWSMessageCreated = onMessageCreated;
      wsMockState.capturedWSMessageUpdated = onMessageUpdated ?? null;
      wsMockState.capturedReactionUpdated = onReactionUpdated ?? null;
      wsMockState.capturedReactionError = onReactionError ?? null;
      wsMockState.capturedSubscribed = onSubscribed ?? null;
      wsMockState.capturedOnTypingUpdated = onTypingUpdated ?? null;
      return {
        toggleReaction: wsMockState.toggleReaction,
        sendTyping: wsMockState.sendTyping,
        connectionStatus: "connected",
      };
    },
  ),
}));

// ── Fixtures (test-only) ──────────────────────────────────────────────────────

const makeMessage = (overrides: Partial<Message> = {}): Message => ({
  id: "msg-1",
  senderId: "user-abc",
  senderDisplayName: "",
  senderEmail: "",
  kind: "user",
  bodyText: "Olá, mundo!",
  bodyFormat: "v1",
  isRemoved: false,
  status: "active",
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
  isEdited: false,
  editCount: 0,
  reactions: [],
  isFavorited: false,
  isForwarded: false,
  ...overrides,
});

const emptyPage: MessagePage = { messages: [], nextCursor: "" };

const messagePage = (messages: Message[]): MessagePage => ({ messages, nextCursor: "" });

// Channel-details payload keyed by channel, so a test that switches channels can
// assert the panel followed the switch rather than kept the first channel's data.
/**
 * jsdom performs no layout, so a test has to define the scroll container's
 * geometry by hand — and doing that in the same step as the scroll event makes
 * a simulated reader indistinguishable from an async reflow.
 *
 * #788: ChatMessageArea reads a reader's intent only from a scroll event whose
 * scrollHeight is unchanged since the previous one, because an event that also
 * grew the timeline describes a layout that shifted underneath a stationary
 * viewport, not a person. So a simulated scroll settles the geometry first,
 * the way a browser settles it at layout time before anyone can scroll.
 */
function settleListLayout(list: HTMLElement, scrollHeight: number, clientHeight: number) {
  Object.defineProperty(list, "scrollHeight", { configurable: true, value: scrollHeight });
  Object.defineProperty(list, "clientHeight", { configurable: true, value: clientHeight });
  fireEvent.scroll(list);
}

/** The reader's own scroll: only scrollTop moves, exactly as in a browser. */
function userScrollTo(list: HTMLElement, scrollTop: number) {
  Object.defineProperty(list, "scrollTop", {
    configurable: true,
    writable: true,
    value: scrollTop,
  });
  fireEvent.scroll(list);
}

function groupDetailsFor(conversationId: string) {
  return {
    id: conversationId,
    name: `Grupo ${conversationId}`,
    createdAt: "2024-03-04T15:00:00.000Z",
    participantCount: 4,
    participants: [
      { userId: "me-123", displayName: `Participante de ${conversationId}` },
      // Deliberately offline: a group lists everyone, connected or not.
      {
        userId: "other-1",
        displayName: `Offline de ${conversationId}`,
        presence: "offline" as const,
      },
    ],
  };
}

// The 1:1 profile payload, keyed by conversation, so a test that switches DMs
// can assert the panel followed the switch rather than kept the first person.
function directProfileFor(conversationId: string) {
  return {
    // The tag comes from the client, which validated the one the server sent.
    kind: "direct" as const,
    conversationId,
    profile: {
      userId: `outro-de-${conversationId}`,
      displayName: `Perfil de ${conversationId}`,
      email: `${conversationId}@nic.test`,
      presence: "online" as const,
    },
  };
}

function channelDetailsFor(channelId: string) {
  return {
    id: channelId,
    slug: channelId,
    name: `Canal ${channelId}`,
    type: "public" as const,
    createdAt: "2024-01-12T09:30:00.000Z",
    memberCount: 3,
    onlineCount: 1,
    onlineMembers: [
      {
        userId: "me-123",
        displayName: `Membro de ${channelId}`,
        role: "member" as const,
        presence: "online" as const,
      },
    ],
  };
}

// ── Render helpers ────────────────────────────────────────────────────────────

function renderChannelArea(channelId = "geral") {
  return render(
    <MemoryRouter initialEntries={[`/chat/channel/${encodeURIComponent(channelId)}`]}>
      <Routes>
        <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
      </Routes>
    </MemoryRouter>,
  );
}

function renderDMArea(dmId = "dm-juliane") {
  return render(
    <MemoryRouter initialEntries={[`/chat/dm/${encodeURIComponent(dmId)}`]}>
      <Routes>
        <Route path="/chat/dm/:id" element={<ChatMessageArea kind="dm" />} />
      </Routes>
    </MemoryRouter>,
  );
}

function renderChannelAreaForUser(currentUserId = "me-123") {
  return render(
    <MemoryRouter initialEntries={["/chat/channel/geral"]}>
      <Routes>
        <Route
          path="/chat"
          element={<ParentWithContext ctx={{ currentUserId, channels: [], dms: [] }} />}
        >
          <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const rf09SourceMessageID = "22222222-2222-4222-8222-222222222222";
const rf09SecondMessageID = "44444444-4444-4444-8444-444444444444";
const rf09SourceChannelID = "11111111-1111-4111-8111-111111111111";
const rf09SourceDMID = "33333333-3333-4333-8333-333333333333";

function renderPendingReference(
  messageId = rf09SourceMessageID,
  source: { kind: "channel" | "dm"; id: string } = {
    kind: "channel",
    id: rf09SourceChannelID,
  },
) {
  return renderPendingReferenceState({
    referencedMessageId: messageId,
    referenceTargetKind: source.kind,
    referenceTargetId: source.id,
  });
}

function renderPendingReferenceState(state: unknown) {
  return render(
    <MemoryRouter
      initialEntries={[
        {
          pathname: "/chat/channel/destination",
          state,
        },
      ]}
    >
      <Routes>
        <Route
          path="/chat"
          element={
            <ParentWithContext
              ctx={{
                currentUserId: "me-123",
                channels: [
                  { id: "destination", name: "Destino", type: "public", canWrite: true },
                  {
                    id: rf09SourceChannelID,
                    name: "Origem privada",
                    type: "private",
                    canWrite: true,
                  },
                ],
                dms: [
                  {
                    id: rf09SourceDMID,
                    type: "1:1",
                    name: "Conversa privada",
                    participants: [],
                  },
                ],
              }}
            />
          }
        >
          <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

/**
 * A DOMRect for the geometry tests: jsdom reports every box as zero-sized, so a
 * placement test has to say what the browser would have measured.
 */
function domRect(rect: Partial<DOMRect>): DOMRect {
  return {
    x: 0,
    y: 0,
    left: 0,
    right: 0,
    top: 0,
    bottom: 0,
    width: 0,
    height: 0,
    ...rect,
    toJSON: () => ({}),
  } as DOMRect;
}

async function openFullReactionPicker(messageIndex = 0) {
  const bubbles = await screen.findAllByTestId("chat-msg-bubble");
  fireEvent.mouseEnter(bubbles[messageIndex]);
  await userEvent.click(screen.getByRole("button", { name: "Mais reações" }));
}

// ── Setup / teardown ──────────────────────────────────────────────────────────

beforeEach(() => {
  setTokens("test-at");
  localStorage.clear();
  wsMockState.capturedWSMessageCreated = null;
  wsMockState.capturedWSMessageUpdated = null;
  wsMockState.capturedReactionUpdated = null;
  wsMockState.capturedReactionError = null;
  wsMockState.capturedSubscribed = null;
  wsMockState.capturedOnTypingUpdated = null;
  vi.clearAllMocks();
  vi.mocked(useChatWebSocket).mockImplementation(
    ({
      onMessageCreated,
      onMessageUpdated,
      onReactionUpdated,
      onReactionError,
      onSubscribed,
      onTypingUpdated,
    }) => {
      wsMockState.capturedWSMessageCreated = onMessageCreated;
      wsMockState.capturedWSMessageUpdated = onMessageUpdated ?? null;
      wsMockState.capturedReactionUpdated = onReactionUpdated ?? null;
      wsMockState.capturedReactionError = onReactionError ?? null;
      wsMockState.capturedSubscribed = onSubscribed ?? null;
      wsMockState.capturedOnTypingUpdated = onTypingUpdated ?? null;
      return {
        toggleReaction: wsMockState.toggleReaction,
        sendTyping: wsMockState.sendTyping,
        connectionStatus: "connected",
      };
    },
  );
  mockFetchAllowedReactionEmojis.mockResolvedValue([
    "👍",
    "❤️",
    "😂",
    "🎉",
    "😮",
    "😢",
    "👎",
    "🔥",
    "🙌",
    "👏",
    "✅",
    "👀",
    "🚀",
    "💯",
    "😍",
    "🤔",
  ]);
  mockFetchPins.mockResolvedValue([]);
  mockFetchChannelMessageSecuritySnapshots.mockResolvedValue([]);
  mockFetchDMMessageSecuritySnapshots.mockResolvedValue([]);
  mockPinMessage.mockResolvedValue(undefined);
  mockUnpinMessage.mockResolvedValue(undefined);
  mockGetMessageHistory.mockResolvedValue({ entries: [], nextCursor: undefined });
  mockFetchChannelDetails.mockImplementation((channelId: string) =>
    Promise.resolve(channelDetailsFor(channelId)),
  );
  mockFetchGroupDetails.mockRejectedValue(new Error("group details not stubbed for this test"));
  mockFetchChannelAttachments.mockResolvedValue([]);
  mockFetchGroupDetails.mockImplementation((conversationId: string) =>
    Promise.resolve(groupDetailsFor(conversationId)),
  );
  mockFetchDirectProfile.mockImplementation((conversationId: string) =>
    Promise.resolve(directProfileFor(conversationId)),
  );
  mockGetOrCreateDirectDM.mockResolvedValue({ conversationId: "dm-created", created: true });
  mockForwardChannelMessage.mockResolvedValue(makeMessage({ id: "forwarded", isForwarded: true }));
  // jsdom does not implement scrollIntoView; mock it so the branch is reachable.
  window.Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
  vi.useRealTimers();
  clearTokens();
});

function renderForwardingArea(messages: Message[]) {
  mockFetchChannelMessages.mockResolvedValue(messagePage(messages));
  return render(
    <MemoryRouter initialEntries={["/chat/channel/current"]}>
      <Routes>
        <Route
          path="/chat"
          element={
            <ParentWithContext
              ctx={{
                currentUserId: "me-123",
                channels: [
                  { id: "current", name: "Atual", type: "public", canWrite: true },
                  {
                    id: "destination-a",
                    name: "Destino Alfa",
                    type: "public",
                    canWrite: true,
                  },
                  {
                    id: "destination-b",
                    name: "Equipe Beta",
                    type: "private",
                    canWrite: true,
                  },
                ],
                dms: [],
              }}
            />
          }
        >
          <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("ChatMessageArea — RF-08 forwarding", () => {
  it("opens the searchable dialog, submits once, and closes only after success", async () => {
    let resolveForward!: (message: Message) => void;
    mockForwardChannelMessage.mockReturnValue(
      new Promise((resolve) => {
        resolveForward = resolve;
      }),
    );
    renderForwardingArea([makeMessage({ id: "source-1", senderId: "other" })]);
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    await userEvent.click(screen.getByRole("button", { name: "Encaminhar" }));

    const dialog = screen.getByRole("dialog", { name: "Encaminhar mensagem" });
    const dialogQueries = within(dialog);
    expect(dialog).toBeVisible();
    const search = dialogQueries.getByRole("searchbox", { name: "Buscar canal" });
    expect(search).toHaveFocus();
    expect(dialogQueries.queryByRole("button", { name: "Atual" })).not.toBeInTheDocument();
    const confirm = dialogQueries.getByRole("button", { name: "Encaminhar" });
    expect(confirm).toBeDisabled();

    await userEvent.type(search, "beta");
    expect(dialogQueries.queryByRole("button", { name: "Destino Alfa" })).not.toBeInTheDocument();
    await userEvent.click(dialogQueries.getByRole("button", { name: "Equipe Beta" }));
    expect(dialogQueries.getByRole("button", { name: "Equipe Beta" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await userEvent.dblClick(confirm);

    expect(mockForwardChannelMessage).toHaveBeenCalledTimes(1);
    expect(mockForwardChannelMessage).toHaveBeenCalledWith(
      "destination-b",
      "source-1",
      expect.any(String),
      expect.any(AbortSignal),
    );
    expect(dialogQueries.getByRole("button", { name: "Encaminhando…" })).toBeDisabled();
    expect(dialog).toBeVisible();

    resolveForward(makeMessage({ id: "forwarded", isForwarded: true }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Encaminhar mensagem" })).not.toBeInTheDocument(),
    );
  });

  it("cancels without calling the API and reports an empty search", async () => {
    renderForwardingArea([makeMessage({ id: "source-1" })]);
    fireEvent.mouseEnter(await screen.findByTestId("chat-msg-bubble"));
    await userEvent.click(screen.getByRole("button", { name: "Encaminhar" }));
    const dialog = screen.getByRole("dialog", { name: "Encaminhar mensagem" });
    const dialogQueries = within(dialog);
    await userEvent.type(
      dialogQueries.getByRole("searchbox", { name: "Buscar canal" }),
      "inexistente",
    );
    expect(dialogQueries.getByText("Nenhum canal encontrado para a busca.")).toBeVisible();
    await userEvent.click(dialogQueries.getByRole("button", { name: "Cancelar" }));
    expect(screen.queryByRole("dialog", { name: "Encaminhar mensagem" })).not.toBeInTheDocument();
    expect(mockForwardChannelMessage).not.toHaveBeenCalled();
  });

  it("keeps context after failure and allows retry", async () => {
    mockForwardChannelMessage
      .mockRejectedValueOnce(new Error("unavailable"))
      .mockResolvedValueOnce(makeMessage({ id: "forwarded", isForwarded: true }));
    renderForwardingArea([makeMessage({ id: "source-1" })]);
    fireEvent.mouseEnter(await screen.findByTestId("chat-msg-bubble"));
    await userEvent.click(screen.getByRole("button", { name: "Encaminhar" }));
    const dialogQueries = within(screen.getByRole("dialog", { name: "Encaminhar mensagem" }));
    await userEvent.click(dialogQueries.getByRole("button", { name: "Destino Alfa" }));
    await userEvent.click(dialogQueries.getByRole("button", { name: "Encaminhar" }));

    expect(await dialogQueries.findByRole("alert")).toHaveTextContent(
      "Não foi possível encaminhar",
    );
    expect(dialogQueries.getByRole("button", { name: "Destino Alfa" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await userEvent.click(dialogQueries.getByRole("button", { name: "Encaminhar" }));
    await waitFor(() => expect(mockForwardChannelMessage).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("uses the newly selected source and does not offer forwarding for removed messages", async () => {
    renderForwardingArea([
      makeMessage({ id: "source-1" }),
      makeMessage({ id: "source-2", bodyText: "segunda" }),
      makeMessage({ id: "removed", isRemoved: true, status: "deleted", bodyText: "" }),
    ]);
    const bubbles = await screen.findAllByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubbles[0]);
    await userEvent.click(screen.getByRole("button", { name: "Encaminhar" }));
    await userEvent.click(
      within(screen.getByRole("dialog", { name: "Encaminhar mensagem" })).getByRole("button", {
        name: "Cancelar",
      }),
    );
    fireEvent.mouseEnter(bubbles[1]);
    await userEvent.click(screen.getByRole("button", { name: "Encaminhar" }));
    const dialogQueries = within(screen.getByRole("dialog", { name: "Encaminhar mensagem" }));
    await userEvent.click(dialogQueries.getByRole("button", { name: "Destino Alfa" }));
    await userEvent.click(dialogQueries.getByRole("button", { name: "Encaminhar" }));
    await waitFor(() =>
      expect(mockForwardChannelMessage).toHaveBeenCalledWith(
        "destination-a",
        "source-2",
        expect.any(String),
        expect.any(AbortSignal),
      ),
    );
    fireEvent.mouseEnter(bubbles[2]);
    expect(screen.queryByRole("button", { name: "Encaminhar" })).not.toBeInTheDocument();
  });

  it("keeps the captured source channel excluded while navigation changes the active route", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "source-1", senderId: "other" })]),
    );
    const ctx: ChatOutletContext = {
      currentUserId: "me-123",
      channels: [
        { id: "current", name: "Atual", type: "public", canWrite: true },
        { id: "next", name: "Próximo", type: "public", canWrite: true },
        { id: "destination", name: "Destino", type: "public", canWrite: true },
      ],
      dms: [],
    };
    function NavigationParent() {
      const navigate = useNavigate();
      return (
        <>
          <button type="button" onClick={() => navigate("/chat/channel/next")}>
            Navegar
          </button>
          <Outlet context={ctx} />
        </>
      );
    }
    render(
      <MemoryRouter initialEntries={["/chat/channel/current"]}>
        <Routes>
          <Route path="/chat" element={<NavigationParent />}>
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    fireEvent.mouseEnter(await screen.findByTestId("chat-msg-bubble"));
    await userEvent.click(screen.getByRole("button", { name: "Encaminhar" }));
    expect(
      within(screen.getByRole("dialog", { name: "Encaminhar mensagem" })).queryByRole("button", {
        name: "Atual",
      }),
    ).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Navegar" }));

    const dialog = within(screen.getByRole("dialog", { name: "Encaminhar mensagem" }));
    expect(dialog.queryByRole("button", { name: "Atual" })).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Próximo" })).toBeVisible();
  });

  it("renders the server forwarding marker without changing the safe body renderer", async () => {
    renderForwardingArea([
      makeMessage({ id: "normal", bodyText: "normal" }),
      makeMessage({ id: "forwarded", bodyText: "<img src=x onerror=alert(1)>", isForwarded: true }),
    ]);
    expect(await screen.findByTestId("chat-message-forwarded")).toHaveTextContent(
      "Mensagem encaminhada",
    );
    expect(screen.getAllByTestId("chat-message-forwarded")).toHaveLength(1);
    expect(screen.getByText("<img src=x onerror=alert(1)>")).toBeVisible();
    expect(document.querySelector("img")).toBeNull();
  });
});

// ── Header rendering ──────────────────────────────────────────────────────────

describe("ChatMessageArea — channel header", () => {
  it("renders channel header with channel name", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    renderChannelArea("geral");

    const header = await screen.findByTestId("chat-msg-header");
    expect(header).toBeInTheDocument();
    expect(header).toHaveTextContent("geral");
  });

  it("renders DM header with DM name", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    renderDMArea("dm-juliane");

    const header = await screen.findByTestId("chat-msg-header");
    expect(header).toBeInTheDocument();
    expect(header).toHaveTextContent("dm-juliane");
  });

  it("resolves the DM name from outlet context", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [],
                  dms: [{ id: "dm-1", type: "1:1", name: "Juliane", participants: [] }],
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByTestId("chat-msg-header")).toHaveTextContent("Juliane");
  });

  // Header identity comes from the same sidebar payload the list uses, so these
  // also stand as evidence that no extra request is made per DM.
  const renderDMHeader = (dms: ChatOutletContext["dms"], dmId = "dm-1") => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    return render(
      <MemoryRouter initialEntries={[`/chat/dm/${dmId}`]}>
        <Routes>
          <Route
            path="/chat"
            element={<ParentWithContext ctx={{ currentUserId: "me-123", channels: [], dms }} />}
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  };

  it("shows the counterpart name and avatar in the DM header", async () => {
    renderDMHeader([
      {
        id: "dm-1",
        type: "1:1",
        name: "Juliane Lino",
        participants: [],
        counterpart: {
          userId: "user-2",
          displayName: "Juliane Lino",
          avatarUrl: "/media/avatars/juliane.png",
        },
      },
    ]);

    const header = await screen.findByTestId("chat-msg-header");
    expect(header).toHaveTextContent("Juliane Lino");
    expect(header.querySelector("img")).toHaveAttribute("src", "/media/avatars/juliane.png");
    expect(mockFetchDMMessages).toHaveBeenCalledTimes(1);
  });

  it("shows initials in the DM header when there is no avatar", async () => {
    renderDMHeader([
      {
        id: "dm-1",
        type: "1:1",
        name: "Juliane Lino",
        participants: [],
        counterpart: { userId: "user-2", displayName: "Juliane Lino" },
      },
    ]);

    const header = await screen.findByTestId("chat-msg-header");
    expect(header.querySelector("img")).toBeNull();
    expect(header).toHaveTextContent("JL");
  });

  it("falls back to initials when the DM header avatar fails to load", async () => {
    renderDMHeader([
      {
        id: "dm-1",
        type: "1:1",
        name: "Juliane Lino",
        participants: [],
        counterpart: { userId: "user-2", displayName: "Juliane Lino", avatarUrl: "/gone.png" },
      },
    ]);

    const header = await screen.findByTestId("chat-msg-header");
    fireEvent.error(header.querySelector("img") as HTMLImageElement);

    await waitFor(() => expect(header.querySelector("img")).toBeNull());
    expect(header).toHaveTextContent("JL");
  });

  it("keeps the group DM header on its title with no avatar image", async () => {
    renderDMHeader(
      [{ id: "dm-grp", type: "group", name: "Equipe Infra", participants: [] }],
      "dm-grp",
    );

    const header = await screen.findByTestId("chat-msg-header");
    expect(header).toHaveTextContent("Equipe Infra");
    expect(header.querySelector("img")).toBeNull();
  });

  it("RF-24: wires the channel header call buttons to joinResourceCall with resource_kind channel", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const joinResourceCall = vi.fn();
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat/channel"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  joinResourceCall,
                }}
              />
            }
          >
            <Route path=":id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Iniciar chamada" }));

    expect(joinResourceCall).toHaveBeenCalledExactlyOnceWith({
      kind: "channel",
      id: "geral",
      name: "geral",
    });
  });

  it("RF-24 follow-up: the channel header offers a single Chamada action, never separate Áudio/Vídeo", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat/channel"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  joinResourceCall: vi.fn(),
                }}
              />
            }
          >
            <Route path=":id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    await screen.findByRole("button", { name: "Iniciar chamada" });
    expect(
      screen.queryByRole("button", { name: "Iniciar chamada de áudio" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Iniciar chamada de vídeo" }),
    ).not.toBeInTheDocument();
  });

  it("RF-24: wires the group header call buttons to joinResourceCall with resource_kind dm, never for a 1:1", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    const joinResourceCall = vi.fn();
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-grp"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [],
                  dms: [{ id: "dm-grp", type: "group", name: "Equipe Infra", participants: [] }],
                  joinResourceCall,
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Iniciar chamada" }));

    expect(joinResourceCall).toHaveBeenCalledExactlyOnceWith({
      kind: "dm",
      id: "dm-grp",
      name: "Equipe Infra",
    });
  });

  it("RF-24: a 1:1 DM never offers joinResourceCall buttons even when the context provides it", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    const joinResourceCall = vi.fn();
    const startCall = vi.fn(() => true);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [],
                  dms: [
                    {
                      id: "dm-1",
                      type: "1:1",
                      name: "Juliane",
                      participants: [],
                      counterpart: { userId: "user-1", displayName: "Juliane" },
                    },
                  ],
                  joinResourceCall,
                  startCall,
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Iniciar chamada de áudio" }));

    expect(startCall).toHaveBeenCalledOnce();
    expect(joinResourceCall).not.toHaveBeenCalled();
  });

  // Issue #622 round 2 adversarial audit (section 9): a direct DM's
  // classification must come from the domain-authoritative `type` field,
  // never from "counterpart is present/absent" — a legacy or not-yet-
  // resolved counterpart is a real, reachable shape (see "names the control
  // after the conversation when no counterpart is known" above) and must
  // never be misread as a group, which would wrongly route it into the
  // resource-call flow.
  it("RF-24/#622: a 1:1 DM with a temporarily unresolved counterpart still never offers resource-call UI or discovery", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    const joinResourceCall = vi.fn();
    const getResourceCall = vi.fn(() => null);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [],
                  dms: [
                    {
                      id: "dm-1",
                      type: "1:1",
                      name: "Juliane",
                      participants: [],
                      // counterpart deliberately omitted — a real shape for a
                      // legacy or not-yet-resolved 1:1 DM.
                    },
                  ],
                  joinResourceCall,
                  getResourceCall,
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    await screen.findByTestId("chat-msg-header");
    expect(screen.queryByRole("button", { name: "Iniciar chamada" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Entrar na chamada" })).not.toBeInTheDocument();
    expect(screen.queryByText("Chamada ativa")).not.toBeInTheDocument();
    expect(getResourceCall).not.toHaveBeenCalled();
    expect(joinResourceCall).not.toHaveBeenCalled();
  });

  it("colours the header fallback deterministically from the counterpart id, matching the sidebar", async () => {
    const dmWithUser = (id: string, userId: string) => ({
      id,
      type: "1:1" as const,
      name: "Jane Doe",
      participants: [],
      counterpart: { userId, displayName: "Jane Doe" },
    });

    // Same user id → same colour class, across two separate conversations.
    renderDMHeader([dmWithUser("dm-1", "user-42")], "dm-1");
    const first = await screen.findByTestId("chat-msg-header");
    const firstAvatar = first.querySelector(".chat-msg-area__header-avatar");
    const colorClass = Array.from(firstAvatar?.classList ?? []).find((c) =>
      c.startsWith("chat-msg-area__header-avatar--"),
    );
    expect(colorClass).toBeTruthy();

    cleanup();
    renderDMHeader([dmWithUser("dm-2", "user-42")], "dm-2");
    const second = await screen.findByTestId("chat-msg-header");
    const secondAvatar = second.querySelector(".chat-msg-area__header-avatar");
    expect(Array.from(secondAvatar?.classList ?? [])).toContain(colorClass);

    // The same colour function the sidebar uses drives it.
    expect(colorClass).toBe(`chat-msg-area__header-avatar--${avatarColorFor("user-42")}`);
  });

  // The src-swap tests keep the SAME HeaderDM instance mounted and change only
  // the avatar URL, which is exactly what a naive `useState(false)` for the
  // failed flag gets wrong: after the first image errors, the flag would stay
  // true and block a subsequently valid image. Tracking the failed URL instead
  // lets a new src try again.
  const dmWith = (avatarUrl?: string, name = "Juliane Lino") => ({
    id: "dm-1",
    type: "1:1" as const,
    name,
    participants: [],
    counterpart: { userId: "user-2", displayName: name, avatarUrl },
  });

  const renderHeaderCtx = (avatarUrl?: string, name?: string) => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    const ctx: ChatOutletContext = {
      currentUserId: "me-123",
      channels: [],
      dms: [dmWith(avatarUrl, name)],
    };
    return render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route path="/chat" element={<ParentWithContext ctx={ctx} />}>
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  };

  const rerenderHeaderCtx = (
    rerender: ReturnType<typeof renderHeaderCtx>["rerender"],
    avatarUrl?: string,
    name?: string,
  ) => {
    const ctx: ChatOutletContext = {
      currentUserId: "me-123",
      channels: [],
      dms: [dmWith(avatarUrl, name)],
    };
    rerender(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route path="/chat" element={<ParentWithContext ctx={ctx} />}>
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  };

  it("header A → B → A: a failed A is retried after navigating to B and back", async () => {
    const { rerender } = renderHeaderCtx("/avatar-a.png");
    const header = await screen.findByTestId("chat-msg-header");

    // A fails → initials fallback.
    expect(header.querySelector("img")).toHaveAttribute("src", "/avatar-a.png");
    fireEvent.error(header.querySelector("img") as HTMLImageElement);
    await waitFor(() => expect(header.querySelector("img")).toBeNull());
    expect(header).toHaveTextContent("JL");

    // Navigate to B (same header instance) → B renders.
    rerenderHeaderCtx(rerender, "/avatar-b.png");
    await waitFor(() =>
      expect(header.querySelector("img")).toHaveAttribute("src", "/avatar-b.png"),
    );

    // Back to A → A must be tried again, not stuck on the earlier failure.
    rerenderHeaderCtx(rerender, "/avatar-a.png");
    await waitFor(() =>
      expect(header.querySelector("img")).toHaveAttribute("src", "/avatar-a.png"),
    );
  });

  it("keeps the header in fallback while the same failed src is retried", async () => {
    const { rerender } = renderHeaderCtx("/avatar-a.png");
    const header = await screen.findByTestId("chat-msg-header");
    fireEvent.error(header.querySelector("img") as HTMLImageElement);
    await waitFor(() => expect(header.querySelector("img")).toBeNull());

    // Re-render with the identical failed URL: no new attempt, stays initials.
    rerenderHeaderCtx(rerender, "/avatar-a.png");
    expect(header.querySelector("img")).toBeNull();
    expect(header).toHaveTextContent("JL");
  });

  it("falls back in the header when a valid avatar is replaced by an absent one", async () => {
    const { rerender } = renderHeaderCtx("/avatar-a.png");
    const header = await screen.findByTestId("chat-msg-header");
    expect(header.querySelector("img")).toHaveAttribute("src", "/avatar-a.png");

    rerenderHeaderCtx(rerender, undefined);
    await waitFor(() => expect(header.querySelector("img")).toBeNull());
    expect(header).toHaveTextContent("JL");
  });

  it("shows B's avatar and name even when A and B share a name but differ by URL", async () => {
    const { rerender } = renderHeaderCtx("/avatar-a.png", "Ana");
    const header = await screen.findByTestId("chat-msg-header");
    fireEvent.error(header.querySelector("img") as HTMLImageElement);
    await waitFor(() => expect(header.querySelector("img")).toBeNull());

    rerenderHeaderCtx(rerender, "/avatar-b.png", "Ana");
    const image = await waitFor(() => {
      const img = header.querySelector("img");
      expect(img).not.toBeNull();
      return img as HTMLImageElement;
    });
    expect(image).toHaveAttribute("src", "/avatar-b.png");
    expect(header).toHaveTextContent("Ana");
  });

  it("renders the chat message area container", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    renderChannelArea();

    expect(await screen.findByTestId("chat-message-area")).toBeInTheDocument();
  });

  it("renders DM area container", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    renderDMArea();

    expect(await screen.findByTestId("chat-message-area")).toBeInTheDocument();
  });
});

// ── #642 active resource call bar ──────────────────────────────────────────────

function makeResourceCall(overrides: Partial<Call> = {}): Call {
  return {
    call_id: "call-1",
    request_id: "req-1",
    caller_id: "user-an",
    callee_id: "",
    target_type: "channel",
    target_id: "geral",
    call_type: "audio",
    status: "active",
    version: 1,
    created_at: "2024-01-01T12:00:00.000Z",
    occurred_at: "2024-01-01T12:05:00.000Z",
    expires_at: "2024-01-01T13:00:00.000Z",
    ...overrides,
  };
}

function resourceCallSession(overrides: Partial<ActiveResourceCallSession> = {}) {
  const session: ActiveResourceCallSession = {
    // Matches makeResourceCall()'s own defaults — this is exactly what
    // CallSessionProvider's resourcePresentationCall would carry once it
    // proves the call_id match (issue #642 review, blocker 2).
    callId: "call-1",
    startedAt: "2024-01-01T12:00:00.000Z",
    participants: [{ identity: "user-jl", displayName: "Juliane Lino" } as never],
    localId: "user-an",
    localName: "Álvaro Neto (você)",
    localInitials: "AN",
    activeSpeakerId: null,
    microphoneEnabled: true,
    microphonePending: false,
    onToggleMicrophone: vi.fn(),
    onLeave: vi.fn(),
    onOpenFullCall: vi.fn(),
    ...overrides,
  };
  return session;
}

describe("ChatMessageArea — #642 active resource call bar", () => {
  it("shows the bar when participating in the current channel's resource call, with title/timer/count", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const call = makeResourceCall();
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => call,
                  isParticipatingIn: () => true,
                  resourceCallSession: resourceCallSession(),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-resource-call-bar");
    expect(bar).toHaveTextContent("Chamada de voz — #geral");
    expect(bar).toHaveTextContent("2 participantes");
  });

  it("shows the bar in 'available' mode when not participating in a resource call", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const call = makeResourceCall();
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => call,
                  isParticipatingIn: () => false,
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-resource-call-bar");
    expect(bar).toHaveTextContent("Chamada de voz — #geral");
    // Should have join button but no mute/leave controls
    expect(within(bar).getByRole("button", { name: "Entrar na chamada" })).toBeInTheDocument();
    expect(within(bar).queryByRole("button", { name: /Mutar/ })).not.toBeInTheDocument();

    // Header should not duplicate "Entrar na chamada"
    const header = screen.getByTestId("chat-msg-header");
    expect(
      within(header).queryByRole("button", { name: "Entrar na chamada" }),
    ).not.toBeInTheDocument();
  });

  it("keeps an available call visible but disables join when joinResourceCall is unavailable", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => makeResourceCall(),
                  isParticipatingIn: () => false,
                  joinResourceCall: undefined,
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const bar = await screen.findByTestId("active-resource-call-bar");
    expect(within(bar).getByRole("button", { name: "Entrar na chamada" })).toBeDisabled();
    const header = screen.getByTestId("chat-msg-header");
    expect(
      within(header).queryByRole("button", { name: "Entrar na chamada" }),
    ).not.toBeInTheDocument();
    expect(
      within(header).queryByRole("button", { name: "Iniciar chamada" }),
    ).not.toBeInTheDocument();
  });

  it("shows no bar when participating in a DIFFERENT resource target and this target has no active call", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [
                    { id: "geral", name: "geral", type: "public", canWrite: true },
                    { id: "outro", name: "outro", type: "public", canWrite: true },
                  ],
                  dms: [],
                  getResourceCall: () => null, // no call in "geral"
                  // Mirrors ChatShell's real isParticipatingIn semantics: true
                  // only for the exact target the user is in — "outro", not
                  // "geral", the one this view has open.
                  isParticipatingIn: (_kind, id) => id === "outro",
                  resourceCallSession: resourceCallSession(),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("chat-msg-header");
    expect(screen.queryByTestId("active-resource-call-bar")).not.toBeInTheDocument();
  });

  it("never shows the bar for a direct 1:1 DM, even if resourceCallSession is somehow present", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [
                    {
                      id: "dm-1",
                      type: "1:1",
                      name: "Juliane",
                      participants: [],
                      counterpart: { userId: "user-jl", displayName: "Juliane" },
                    },
                  ],
                  isParticipatingIn: () => true,
                  resourceCallSession: resourceCallSession(),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("chat-msg-header");
    expect(screen.queryByTestId("active-resource-call-bar")).not.toBeInTheDocument();
  });

  it("uses the group's bare name (no #) when participating in a group DM's resource call", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/dm/grupo-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [{ id: "grupo-1", type: "group", name: "Infra Squad", participants: [] }],
                  getResourceCall: () =>
                    makeResourceCall({ target_type: "dm", target_id: "grupo-1" }),
                  isParticipatingIn: () => true,
                  resourceCallSession: resourceCallSession(),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-resource-call-bar");
    expect(bar).toHaveTextContent("Chamada de voz — Infra Squad");
    expect(bar).not.toHaveTextContent("#Infra Squad");
  });

  it("shows an available group-DM call to an outsider without duplicating join in the header", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/dm/grupo-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [{ id: "grupo-1", type: "group", name: "Infra Squad", participants: [] }],
                  getResourceCall: () =>
                    makeResourceCall({ target_type: "dm", target_id: "grupo-1" }),
                  isParticipatingIn: () => false,
                  joinResourceCall: vi.fn(),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const bar = await screen.findByTestId("active-resource-call-bar");
    expect(bar).toHaveTextContent("Chamada de voz — Infra Squad");
    expect(bar).not.toHaveTextContent("#Infra Squad");
    expect(within(bar).getByRole("button", { name: "Entrar na chamada" })).toBeInTheDocument();
    expect(
      within(screen.getByTestId("chat-msg-header")).queryByRole("button", {
        name: "Entrar na chamada",
      }),
    ).not.toBeInTheDocument();
  });

  it("includes #<name> in the title when participating in a channel's resource call", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/channel/infraestrutura"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [
                    {
                      id: "infraestrutura",
                      name: "infraestrutura",
                      type: "public",
                      canWrite: true,
                    },
                  ],
                  dms: [],
                  getResourceCall: () => makeResourceCall({ target_id: "infraestrutura" }),
                  isParticipatingIn: () => true,
                  resourceCallSession: resourceCallSession(),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-resource-call-bar");
    expect(bar).toHaveTextContent("Chamada de voz — #infraestrutura");
  });

  it("wires the bar's leave button to ctx.resourceCallSession.onLeave", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const onLeave = vi.fn();
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => makeResourceCall(),
                  isParticipatingIn: () => true,
                  resourceCallSession: resourceCallSession({ onLeave }),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("active-resource-call-bar");
    fireEvent.click(screen.getByRole("button", { name: /Sair/ }));
    expect(onLeave).toHaveBeenCalledOnce();
  });

  it("wires the bar's mute button to ctx.resourceCallSession.onToggleMicrophone", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const onToggleMicrophone = vi.fn();
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => makeResourceCall(),
                  isParticipatingIn: () => true,
                  resourceCallSession: resourceCallSession({ onToggleMicrophone }),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("active-resource-call-bar");
    fireEvent.click(screen.getByRole("button", { name: "Mutar microfone" }));
    expect(onToggleMicrophone).toHaveBeenCalledOnce();
  });

  it("wires the bar's main area to ctx.resourceCallSession.onOpenFullCall", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const onOpenFullCall = vi.fn();
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => makeResourceCall(),
                  isParticipatingIn: () => true,
                  resourceCallSession: resourceCallSession({ onOpenFullCall }),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-resource-call-bar");
    fireEvent.click(within(bar).getByRole("button", { name: /Abrir chamada/ }));
    expect(onOpenFullCall).toHaveBeenCalledOnce();
  });

  it("never renders the chamada.html search bar as part of this feature", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => makeResourceCall(),
                  isParticipatingIn: () => true,
                  resourceCallSession: resourceCallSession(),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("active-resource-call-bar");
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    expect(document.querySelector(".voicebanner input")).toBeNull();
  });

  it("never renders the bar if discovery is terminal (not active), even if participatingHere is true (issue #657)", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const terminalCall = makeResourceCall({ status: "ended" });
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => terminalCall,
                  isParticipatingIn: () => true, // Still converging
                  resourceCallSession: resourceCallSession(),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("chat-msg-header");
    expect(screen.queryByTestId("active-resource-call-bar")).not.toBeInTheDocument();
  });

  it("removes the bar when the same view converges from an active call to an ended call", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    let call = makeResourceCall();
    const view = () => (
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => call,
                  isParticipatingIn: () => false,
                  joinResourceCall: vi.fn(),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    );
    const { rerender } = render(view());
    expect(await screen.findByTestId("active-resource-call-bar")).toBeInTheDocument();

    call = makeResourceCall({ status: "ended" });
    rerender(view());

    await waitFor(() =>
      expect(screen.queryByTestId("active-resource-call-bar")).not.toBeInTheDocument(),
    );
  });

  it("passes exact callId to joinResourceCall for an outsider joining an available call", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const joinResourceCall = vi.fn();
    const call = makeResourceCall({ call_id: "exact-call-123" });
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => call,
                  isParticipatingIn: () => false,
                  joinResourceCall,
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-resource-call-bar");
    fireEvent.click(within(bar).getByRole("button", { name: "Entrar na chamada" }));
    expect(joinResourceCall).toHaveBeenCalledWith({
      kind: "channel",
      id: "geral",
      name: "geral",
      callId: "exact-call-123",
    });
  });

  it("shows participating-info when participating but resourceCallSession is undefined", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => makeResourceCall(),
                  isParticipatingIn: () => true,
                  resourceCallSession: undefined, // Missing local session
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-resource-call-bar");
    expect(bar).toHaveTextContent("Chamada de voz — #geral");
    // Controls should not exist
    expect(
      within(bar).queryByRole("button", { name: "Entrar na chamada" }),
    ).not.toBeInTheDocument();
    expect(within(bar).queryByRole("button", { name: /Mutar/ })).not.toBeInTheDocument();
    expect(within(bar).queryByRole("button", { name: /Sair/ })).not.toBeInTheDocument();
  });
});

// ── #673: icon call controls (channel/group + DM) ───────────────────────────

describe("ChatMessageArea — #673 icon call controls", () => {
  it("channel header: no visible 'Chamada' text, an accessible icon button preserves the join callback", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const onCall = vi.fn();
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  getResourceCall: () => null,
                  isParticipatingIn: () => false,
                  joinResourceCall: () => onCall(),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const header = await screen.findByTestId("chat-msg-header");
    expect(within(header).queryByText("Chamada")).not.toBeInTheDocument();
    const button = within(header).getByRole("button", { name: "Iniciar chamada" });
    expect(button.tagName).toBe("BUTTON");
    fireEvent.click(button);
    expect(onCall).toHaveBeenCalledOnce();
  });

  it("DM header: no visible 'Áudio'/'Vídeo' text, two separate icon buttons each fire only their own call type", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    const startCall = vi.fn(() => true);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [
                    {
                      id: "dm-1",
                      type: "1:1",
                      name: "Juliane",
                      participants: [],
                      counterpart: { userId: "user-jl", displayName: "Juliane" },
                    },
                  ],
                  startCall,
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const header = await screen.findByTestId("chat-msg-header");
    expect(within(header).queryByText("Áudio")).not.toBeInTheDocument();
    expect(within(header).queryByText("Vídeo")).not.toBeInTheDocument();

    const audioBtn = within(header).getByRole("button", { name: "Iniciar chamada de áudio" });
    const videoBtn = within(header).getByRole("button", { name: "Iniciar chamada de vídeo" });
    expect(audioBtn.tagName).toBe("BUTTON");
    expect(videoBtn.tagName).toBe("BUTTON");

    fireEvent.click(audioBtn);
    expect(startCall).toHaveBeenNthCalledWith(1, "user-jl", "audio");
    fireEvent.click(videoBtn);
    expect(startCall).toHaveBeenNthCalledWith(2, "user-jl", "video");
    expect(startCall).toHaveBeenCalledTimes(2);
  });
});

// ── #673: active direct 1:1 call bar ────────────────────────────────────────

function directCallSession(overrides: Partial<ActiveDirectCallSession> = {}) {
  const session: ActiveDirectCallSession = {
    callId: "direct-call-1",
    startedAt: "2024-01-01T12:00:00.000Z",
    callType: "audio",
    peerUserId: "user-jl",
    microphoneEnabled: true,
    microphonePending: false,
    onToggleMicrophone: vi.fn(),
    onLeave: vi.fn(),
    onOpenFullCall: vi.fn(),
    ...overrides,
  };
  return session;
}

const directDM = {
  id: "dm-1",
  type: "1:1" as const,
  name: "Juliane",
  participants: [],
  counterpart: { userId: "user-jl", displayName: "Juliane Lino", avatarUrl: "/avatar.png" },
};

describe("ChatMessageArea — #673 active direct call bar", () => {
  it("shows the bar for an active direct call matching this DM's own counterpart", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [directDM],
                  directCallSession: directCallSession(),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-direct-call-bar");
    expect(bar).toHaveTextContent("Chamada de voz — Juliane Lino");

    // The header suppresses its own call actions once the bar takes over.
    const header = screen.getByTestId("chat-msg-header");
    expect(
      within(header).queryByRole("button", { name: /Iniciar chamada/ }),
    ).not.toBeInTheDocument();
  });

  it('shows "Chamada de vídeo" for a video call type', async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [directDM],
                  directCallSession: directCallSession({ callType: "video" }),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-direct-call-bar");
    expect(bar).toHaveTextContent("Chamada de vídeo — Juliane Lino");
  });

  it("never shows the bar for a DIFFERENT open DM, even when a direct call session is present", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-other"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [
                    directDM,
                    {
                      id: "dm-other",
                      type: "1:1" as const,
                      name: "Outra Pessoa",
                      participants: [],
                      counterpart: { userId: "user-other", displayName: "Outra Pessoa" },
                    },
                  ],
                  // The session's peerUserId ("user-jl") does not match this
                  // DM's own counterpart ("user-other") — a call belonging
                  // to a different 1:1 must never render here.
                  directCallSession: directCallSession(),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("chat-msg-header");
    expect(screen.queryByTestId("active-direct-call-bar")).not.toBeInTheDocument();
  });

  it("never shows the bar for a group DM, even if a directCallSession is somehow present", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-grp"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [{ id: "dm-grp", type: "group", name: "Equipe Infra", participants: [] }],
                  directCallSession: directCallSession(),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("chat-msg-header");
    expect(screen.queryByTestId("active-direct-call-bar")).not.toBeInTheDocument();
  });

  it("never shows the bar for a channel, even if a directCallSession is somehow present", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [{ id: "geral", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                  directCallSession: directCallSession(),
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("chat-msg-header");
    expect(screen.queryByTestId("active-direct-call-bar")).not.toBeInTheDocument();
  });

  it("shows no bar when directCallSession is absent (e.g. only ringing — IncomingCallPopup owns that surface)", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [directDM],
                  directCallSession: undefined,
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("chat-msg-header");
    expect(screen.queryByTestId("active-direct-call-bar")).not.toBeInTheDocument();
  });

  it("wires the bar's leave button to ctx.directCallSession.onLeave", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    const onLeave = vi.fn();
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [directDM],
                  directCallSession: directCallSession({ onLeave }),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-direct-call-bar");
    fireEvent.click(within(bar).getByRole("button", { name: /Sair/ }));
    expect(onLeave).toHaveBeenCalledOnce();
  });

  it("wires the bar's mute button to ctx.directCallSession.onToggleMicrophone and reflects the real mic state", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    const onToggleMicrophone = vi.fn();
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [directDM],
                  directCallSession: directCallSession({
                    onToggleMicrophone,
                    microphoneEnabled: false,
                  }),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-direct-call-bar");
    const muteButton = within(bar).getByRole("button", { name: "Ativar microfone" });
    fireEvent.click(muteButton);
    expect(onToggleMicrophone).toHaveBeenCalledOnce();
  });

  it("wires the bar's main area to ctx.directCallSession.onOpenFullCall", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    const onOpenFullCall = vi.fn();
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [directDM],
                  directCallSession: directCallSession({ onOpenFullCall }),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    const bar = await screen.findByTestId("active-direct-call-bar");
    fireEvent.click(within(bar).getByRole("button", { name: /Abrir chamada/ }));
    expect(onOpenFullCall).toHaveBeenCalledOnce();
  });

  it("re-render/navigation away and back reuses the same bar — never duplicated", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    const { rerender } = render(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [directDM],
                  directCallSession: directCallSession(),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("active-direct-call-bar");

    rerender(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "user-an",
                  channels: [],
                  dms: [directDM],
                  directCallSession: directCallSession(),
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getAllByTestId("active-direct-call-bar")).toHaveLength(1);
  });
});

// ── Loading state ─────────────────────────────────────────────────────────────

describe("ChatMessageArea — loading state", () => {
  it("shows loading skeleton while messages are fetching", async () => {
    let resolve: (p: MessagePage) => void;
    mockFetchChannelMessages.mockReturnValue(new Promise((r) => (resolve = r)));
    renderChannelArea();

    // Before promise resolves, skeleton should be visible.
    expect(await screen.findByRole("status", { name: /carregando/i })).toBeInTheDocument();

    resolve!(emptyPage);
  });

  it("loading state disables composer", async () => {
    let resolve: (p: MessagePage) => void;
    mockFetchChannelMessages.mockReturnValue(new Promise((r) => (resolve = r)));
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    expect(input).toHaveAttribute("aria-disabled", "true");

    resolve!(emptyPage);
  });
});

// ── Error state ───────────────────────────────────────────────────────────────

describe("ChatMessageArea — error state", () => {
  it("shows error state when API fails", async () => {
    mockFetchChannelMessages.mockRejectedValue(new Error("network error"));
    renderChannelArea();

    await waitFor(() => {
      expect(screen.getByTestId("chat-msg-error")).toBeInTheDocument();
    });
  });

  it("error state shows retry button", async () => {
    mockFetchChannelMessages.mockRejectedValue(new Error("network error"));
    renderChannelArea();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /tentar novamente/i })).toBeInTheDocument();
    });
  });

  it("retry button triggers reload", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockRejectedValueOnce(new Error("fail")).mockResolvedValue(emptyPage);

    renderChannelArea();

    const retryBtn = await screen.findByRole("button", { name: /tentar novamente/i });
    await user.click(retryBtn);

    await waitFor(() => {
      expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2);
    });
  });

  it("inaccessible channel route shows safe error state and disables composer", async () => {
    mockFetchChannelMessages.mockRejectedValue(new Error("not_found"));
    renderChannelArea("private-target");

    expect(await screen.findByTestId("chat-msg-error")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-msg-empty")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-composer-input")).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
  });

  it("inaccessible DM route shows safe error state and disables composer", async () => {
    mockFetchDMMessages.mockRejectedValue(new Error("not_found"));
    renderDMArea("dm-private-target");

    expect(await screen.findByTestId("chat-msg-error")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-msg-empty")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-composer-input")).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
  });

  it("shows a discreet realtime instability banner without technical details", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const sensitiveError = "HTTP 429 " + "tok" + "en=" + "sec" + "ret payload=body";
    mockFetchChannelMessage.mockRejectedValue(new Error(sensitiveError));
    renderChannelArea("geral");

    await screen.findByTestId("chat-msg-empty");

    act(() => {
      wsMockState.capturedWSMessageCreated?.({
        type: "message.created",
        schema_version: 1,
        workspace_id: "ws-1",
        target_type: "channel",
        target_id: "geral",
        message_id: "msg-realtime-fallback",
        event_id: "evt-1",
        created_at: new Date().toISOString(),
      });
    });

    const banner = await screen.findByTestId("chat-realtime-error");
    expect(banner).toHaveTextContent("Conexão em tempo real instável. Tentando reconectar...");
    expect(banner).not.toHaveTextContent("tok" + "en=" + "sec" + "ret");
    expect(banner).not.toHaveTextContent("payload=body");
  });
});

// ── Empty state ───────────────────────────────────────────────────────────────

describe("ChatMessageArea — empty state", () => {
  it("shows empty state for channel with no messages", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    renderChannelArea("geral");

    const empty = await screen.findByTestId("chat-msg-empty");
    expect(empty).toBeInTheDocument();
  });

  it("shows empty state for DM with no messages", async () => {
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    renderDMArea("dm-juliane");

    const empty = await screen.findByTestId("chat-msg-empty");
    expect(empty).toBeInTheDocument();
  });
});

// ── Message list ──────────────────────────────────────────────────────────────

describe("ChatMessageArea — message list", () => {
  it("shows one hover menu with three recent emojis and no persistent add button", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1" }), makeMessage({ id: "m2" })]),
    );
    renderChannelAreaForUser();
    const bubbles = await screen.findAllByTestId("chat-msg-bubble");

    expect(screen.queryByRole("button", { name: "Mais reações" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Adicionar reação" })).not.toBeInTheDocument();
    fireEvent.mouseEnter(bubbles[0]);

    expect(screen.getAllByRole("button", { name: /Reagir rapidamente com/ })).toHaveLength(3);
    expect(screen.getByRole("button", { name: "Responder" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Mais reações" })).toBeVisible();
    fireEvent.mouseEnter(bubbles[1]);
    expect(screen.getAllByRole("button", { name: "Mais reações" })).toHaveLength(1);
    fireEvent.mouseLeave(bubbles[1]);
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Mais reações" })).not.toBeInTheDocument(),
    );
  });

  it("keeps the grouped message hover menu active when the pointer moves onto its toolbar", async () => {
    const user = userEvent.setup();
    const sameMinute = "2024-01-15T10:04";
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          senderId: "user-alice",
          senderDisplayName: "Alice",
          bodyText: "Primeira",
          createdAt: `${sameMinute}:10.000Z`,
        }),
        makeMessage({
          id: "m2",
          senderId: "user-alice",
          senderDisplayName: "Alice",
          bodyText: "Segunda",
          createdAt: `${sameMinute}:20.000Z`,
        }),
        makeMessage({
          id: "m3",
          senderId: "user-alice",
          senderDisplayName: "Alice",
          bodyText: "Terceira",
          createdAt: `${sameMinute}:30.000Z`,
        }),
      ]),
    );
    renderChannelAreaForUser();
    const bubbles = await screen.findAllByTestId("chat-msg-bubble");

    await user.hover(bubbles[1]);
    const toolbar = screen.getByRole("toolbar", { name: "Reagir à mensagem" });
    await user.hover(toolbar);

    expect(screen.getByRole("button", { name: "Mais reações" })).toBeVisible();
    expect(screen.getAllByRole("toolbar", { name: "Reagir à mensagem" })).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));
    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m2", "👍");
    expect(wsMockState.toggleReaction).not.toHaveBeenCalledWith("m1", expect.any(String));
  });

  it("cleans up a pending hover-menu close timer on unmount", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    const { unmount } = renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    vi.useFakeTimers();
    const clearTimeoutSpy = vi.spyOn(window, "clearTimeout");

    fireEvent.mouseEnter(bubble);
    fireEvent.mouseLeave(bubble);
    unmount();

    expect(clearTimeoutSpy).toHaveBeenCalled();
    clearTimeoutSpy.mockRestore();
  });

  it("reveals the reaction menu by keyboard focus and touch", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");

    fireEvent.focus(bubble);
    expect(screen.getByRole("button", { name: "Mais reações" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Responder" })).toBeVisible();
    fireEvent.blur(bubble, { relatedTarget: document.body });
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Mais reações" })).not.toBeInTheDocument(),
    );
    fireEvent.touchStart(bubble);
    expect(screen.getByRole("button", { name: "Mais reações" })).toBeVisible();
  });

  it("puts the reader's own recent emoji first in the quick row", async () => {
    localStorage.setItem(
      "nchat_emoji_usage:me-123",
      JSON.stringify({
        v: 1,
        tone: 0,
        entries: [{ emoji: "🛑", count: 3, usedAt: 20 }],
      }),
    );
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");

    fireEvent.mouseEnter(bubble);

    const quick = screen
      .getAllByRole("button", { name: /^Reagir rapidamente com/ })
      .map((button) => button.getAttribute("aria-label"));
    // The reader's own history leads; the server's shortlist fills the rest.
    expect(quick[0]).toBe("Reagir rapidamente com 🛑");
    expect(quick).toHaveLength(3);
  });

  // One history, two surfaces: what the reader reacts with and what they type
  // into a message feed the same "Recentes" (issue #496).
  it("lends the same emoji history to the composer's picker", async () => {
    localStorage.setItem(
      "nchat_emoji_usage:me-123",
      JSON.stringify({ v: 1, tone: 0, entries: [{ emoji: "🛑", count: 3, usedAt: 20 }] }),
    );
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelAreaForUser();

    await userEvent.click(await screen.findByTestId("toolbar-emoji-btn"));
    const picker = await screen.findByTestId("toolbar-emoji-picker");
    await within(picker).findByRole("searchbox", { name: "Buscar emoji" });

    // The history tab is offered at all, and it opens on the emoji the reader
    // reacted with — which only this conversation's own usage could supply.
    expect(within(picker).getByRole("tab", { name: "Recentes" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(within(picker).getAllByRole("button", { name: "sinal de pare" })).not.toHaveLength(0);
  });

  it("ignores a corrupted local emoji preference instead of losing the quick row", async () => {
    localStorage.setItem("nchat_emoji_usage:me-123", "{not json");
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");

    fireEvent.mouseEnter(bubble);

    expect(screen.getByRole("button", { name: "Reagir rapidamente com 👍" })).toBeVisible();
  });

  it("toggles a quick reaction directly and opens the full grid only from more", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);

    await userEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));
    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "👍");
    expect(screen.queryByRole("dialog", { name: "Escolher reação" })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Mais reações" }));
    expect(screen.getByRole("dialog", { name: "Escolher reação" })).toBeVisible();
    fireEvent.mouseLeave(bubble);
    expect(screen.getByRole("button", { name: "Mais reações" })).toBeVisible();
  });

  // The badge's emoji is not a value this client picked: it came back on the
  // message the server sent, and the server validates it again on the toggle.
  // The lazy catalog is a picker's index, so whether it happens to be in memory
  // must not decide whether an existing reaction can be joined (issue #496).
  it("allows toggling an existing server reaction before the lazy catalog has loaded", async () => {
    resetEmojiCatalogCache();
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [{ emoji: "🏳️‍🌈", count: 1, reactedByMe: false, users: [] }],
        }),
      ]),
    );
    renderChannelAreaForUser();

    const badge = await screen.findByRole("button", { name: "Adicionar reação 🏳️‍🌈" });
    // The reproduction depends on this: nothing has loaded the chunk, so the
    // catalog cannot answer for an emoji outside the server's quick row.
    expect(isCatalogedEmoji("🏳️‍🌈")).toBe(false);
    await userEvent.click(badge);

    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "🏳️‍🌈");
    expect(screen.queryByText("Emoji não permitido para reações.")).toBeNull();
  });

  it("toggles the same existing reaction once the catalog is loaded", async () => {
    resetEmojiCatalogCache();
    await loadEmojiCatalog();
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [{ emoji: "🏳️‍🌈", count: 2, reactedByMe: true, users: [] }],
        }),
      ]),
    );
    renderChannelAreaForUser();

    await userEvent.click(await screen.findByRole("button", { name: "Remover reação 🏳️‍🌈" }));

    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "🏳️‍🌈");
    expect(screen.queryByText("Emoji não permitido para reações.")).toBeNull();
  });

  it("toggles a quick reaction before the lazy catalog has loaded", async () => {
    resetEmojiCatalogCache();
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    fireEvent.mouseEnter(await screen.findByTestId("chat-msg-bubble"));

    expect(isCatalogedEmoji("👍")).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));

    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "👍");
    expect(screen.queryByText("Emoji não permitido para reações.")).toBeNull();
  });

  // A use is what the reader chose *here* and the server confirmed, so the
  // reaction has to start as a local toggle: an event with no intent behind it
  // came from somewhere else and does not shape this client's history (#496).
  it("stores a confirmed own reaction as the most recent allowed emoji", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    await userEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 😂" }));

    act(() =>
      wsMockState.capturedReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "geral",
        message_id: "m1",
        reaction: {
          message_id: "m1",
          actor_user_id: "me-123",
          emoji: "😂",
          added: true,
          reactions: [{ emoji: "😂", count: 1 }],
        },
      }),
    );

    await waitFor(() =>
      expect(
        JSON.parse(localStorage.getItem("nchat_emoji_usage:me-123") ?? "null").entries[0].emoji,
      ).toBe("😂"),
    );
  });

  it("renders a confirmed reaction when local preference storage is unavailable", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("quota", "QuotaExceededError");
    });
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    await userEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));

    act(() =>
      wsMockState.capturedReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "geral",
        message_id: "m1",
        reaction: {
          message_id: "m1",
          actor_user_id: "me-123",
          emoji: "👍",
          added: true,
          reactions: [{ emoji: "👍", count: 1 }],
        },
      }),
    );

    expect(await screen.findByRole("button", { name: "Remover reação 👍" })).toBeVisible();
    setItem.mockRestore();
  });

  it("renders reaction counts and reacts with an emoji chosen from the full picker", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [{ emoji: "🎉", count: 3, reactedByMe: true, users: [] }],
        }),
      ]),
    );
    renderChannelArea();

    expect(await screen.findByRole("button", { name: "Remover reação 🎉" })).toHaveTextContent("3");
    await openFullReactionPicker();
    await userEvent.type(await screen.findByRole("searchbox", { name: "Buscar emoji" }), "polegar");
    // 👍 has skin tones, so choosing it asks which one — against the emoji
    // itself, not a control in the header.
    await userEvent.click(await screen.findByRole("button", { name: "polegar para cima" }));
    await userEvent.click(
      await screen.findByRole("button", { name: "polegar para cima — Padrão" }),
    );

    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "👍");
    expect(screen.queryByRole("dialog", { name: "Escolher reação" })).not.toBeInTheDocument();
    // The picker is served by the bundled catalog: choosing from it costs no
    // request beyond the one configuration call the conversation already made.
    expect(mockFetchAllowedReactionEmojis).toHaveBeenCalledTimes(1);
  });

  it("does not render a persistent add-reaction button when the message has zero reactions", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelArea();

    await screen.findByTestId("chat-msg-bubble");
    expect(screen.queryByRole("button", { name: "Adicionar reação" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mais reações" })).not.toBeInTheDocument();
  });

  it("closes the controlled reaction picker on Escape and outside click", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelArea();

    await openFullReactionPicker();
    expect(screen.getByRole("dialog", { name: "Escolher reação" })).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Escolher reação" })).not.toBeInTheDocument();

    await openFullReactionPicker();
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole("dialog", { name: "Escolher reação" })).not.toBeInTheDocument();
  });

  // Escape is a keyboard gesture, so it has to leave the keyboard somewhere
  // usable: the button the reader opened the picker from.
  it("returns focus to the opener when the picker is closed with Escape", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelArea();

    await openFullReactionPicker();
    await screen.findByRole("searchbox", { name: "Buscar emoji" });
    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.getByRole("button", { name: "Mais reações" })).toHaveFocus();
  });

  it("returns focus to the opener after an emoji is chosen", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelArea();

    await openFullReactionPicker();
    await userEvent.type(await screen.findByRole("searchbox", { name: "Buscar emoji" }), "foguete");
    await userEvent.click(await screen.findByRole("button", { name: "foguete" }));

    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "🚀");
    expect(screen.getByRole("button", { name: "Mais reações" })).toHaveFocus();
  });

  it("names who reacted on the badge, and keeps that name current in realtime", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [
            {
              emoji: "🎉",
              count: 1,
              reactedByMe: false,
              users: [{ userId: "u-2", displayName: "Caio Almeida" }],
            },
          ],
        }),
      ]),
    );
    renderChannelAreaForUser();

    const badge = await screen.findByRole("button", { name: "Adicionar reação 🎉" });
    expect(badge).toHaveAccessibleDescription("🎉: Caio Almeida");

    act(() =>
      wsMockState.capturedReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "geral",
        message_id: "m1",
        reaction: {
          message_id: "m1",
          actor_user_id: "me-123",
          emoji: "🎉",
          added: true,
          reactions: [
            {
              emoji: "🎉",
              count: 2,
              users: [
                { user_id: "u-2", display_name: "Caio Almeida" },
                { user_id: "me-123", display_name: "Álvaro Neto" },
              ],
            },
          ],
        },
      }),
    );

    // No reload, no refetch and no request per hover: the event carried the
    // names, and the reader is named "Você" rather than by their own name.
    const updated = await screen.findByRole("button", { name: "Remover reação 🎉" });
    expect(updated).toHaveAccessibleDescription("🎉: Você e Caio Almeida");
    expect(updated).toHaveTextContent("2");
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(1);
  });

  it("drops an author from the badge when their reaction is removed in realtime", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [
            {
              emoji: "🎉",
              count: 2,
              reactedByMe: false,
              users: [
                { userId: "u-2", displayName: "Caio Almeida" },
                { userId: "u-3", displayName: "Bruna Dias" },
              ],
            },
          ],
        }),
      ]),
    );
    renderChannelAreaForUser();
    await screen.findByRole("button", { name: "Adicionar reação 🎉" });

    act(() =>
      wsMockState.capturedReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "geral",
        message_id: "m1",
        reaction: {
          message_id: "m1",
          actor_user_id: "u-3",
          emoji: "🎉",
          added: false,
          reactions: [
            { emoji: "🎉", count: 1, users: [{ user_id: "u-2", display_name: "Caio Almeida" }] },
          ],
        },
      }),
    );

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Adicionar reação 🎉" }),
      ).toHaveAccessibleDescription("🎉: Caio Almeida"),
    );
  });

  it.each([
    ["middle", 400, 426, 243],
    ["viewport footer", 730, 756, 573],
  ])(
    "portals and positions the picker above a message near the %s",
    async (_name, top, bottom, expectedTop) => {
      mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
      const rectSpy = vi
        .spyOn(Element.prototype, "getBoundingClientRect")
        .mockImplementation(function (this: Element) {
          if (this.getAttribute("aria-label") === "Mais reações") {
            return {
              x: 450,
              y: top,
              left: 450,
              right: 480,
              top,
              bottom,
              width: 30,
              height: bottom - top,
              toJSON: () => ({}),
            };
          }
          if (this.classList.contains("chat-emoji-surface")) {
            return {
              x: 0,
              y: 0,
              left: 0,
              right: 188,
              top: 0,
              bottom: 150,
              width: 188,
              height: 150,
              toJSON: () => ({}),
            };
          }
          return {
            x: 0,
            y: 0,
            left: 0,
            right: 0,
            top: 0,
            bottom: 0,
            width: 0,
            height: 0,
            toJSON: () => ({}),
          };
        });
      renderChannelArea();

      await openFullReactionPicker();

      const dialog = screen.getByRole("dialog", { name: "Escolher reação" });
      expect(dialog.parentElement).toBe(document.body);
      expect(dialog).toHaveStyle({ left: "292px", top: `${expectedTop}px`, visibility: "visible" });
      rectSpy.mockRestore();
    },
  );

  // The picker opens at the size of its Suspense fallback and grows when the
  // lazily-imported catalog lands. Without a second placement it would keep the
  // position computed for the small box and hang off the viewport.
  it("re-places the picker when its lazily-loaded content changes size", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    let pickerHeight = 40;
    const rectSpy = vi
      .spyOn(Element.prototype, "getBoundingClientRect")
      .mockImplementation(function (this: Element) {
        if (this.getAttribute("aria-label") === "Mais reações") {
          return domRect({ left: 450, right: 480, top: 100, bottom: 130, width: 30, height: 30 });
        }
        if (this.classList.contains("chat-emoji-surface")) {
          return domRect({ right: 188, bottom: pickerHeight, width: 188, height: pickerHeight });
        }
        return domRect({});
      });
    renderChannelArea();

    await openFullReactionPicker();
    const dialog = screen.getByRole("dialog", { name: "Escolher reação" });
    // 100 (anchor top) - 40 (fallback height) - 7 (gap): it fits above.
    expect(dialog).toHaveStyle({ top: "53px" });

    pickerHeight = 400;
    act(() => flushResizeObservers());

    // Too tall to fit above now, so it flips below the anchor — and stays whole
    // inside the viewport instead of keeping the stale placement.
    expect(dialog).toHaveStyle({ top: "137px" });
    expect(137 + pickerHeight).toBeLessThanOrEqual(window.innerHeight);
    rectSpy.mockRestore();
  });

  // The finding this covers: removing the last reaction used to make the badge
  // vanish between two frames. It now leaves, and is unmounted by its own
  // animation rather than by a timer.
  it("plays the badge out before removing it, end to end", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [{ emoji: "🎉", count: 1, reactedByMe: true, users: [] }],
        }),
      ]),
    );
    renderChannelAreaForUser();
    const badge = await screen.findByRole("button", { name: "Remover reação 🎉" });
    const slot = badge.closest(".chat-msg-area__reaction-slot") as HTMLElement;

    act(() =>
      wsMockState.capturedReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "geral",
        message_id: "m1",
        reaction: {
          message_id: "m1",
          actor_user_id: "me-123",
          emoji: "🎉",
          added: false,
          reactions: [],
        },
      }),
    );

    await waitFor(() => expect(slot).toHaveAttribute("data-exiting", "true"));
    expect(screen.queryByRole("button", { name: /reação 🎉/ })).toBeNull();

    fireEvent.animationEnd(slot);

    await waitFor(() => expect(document.querySelector(".chat-msg-area__reaction-slot")).toBeNull());
  });

  it("stops observing the picker once it is closed", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelArea();
    await screen.findAllByTestId("chat-msg-bubble");
    // Baseline includes MessageList's own #788 tail-lock observer (the
    // timeline content wrapper), which mounts with the message list itself
    // and is unrelated to the picker.
    const baseline = observedElements().length;

    await openFullReactionPicker();
    expect(observedElements()).toHaveLength(baseline + 1);

    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("dialog", { name: "Escolher reação" })).not.toBeInTheDocument();
    expect(observedElements()).toHaveLength(baseline);
  });

  it("closes the reaction picker when its anchor leaves the viewport", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    const rectSpy = vi
      .spyOn(Element.prototype, "getBoundingClientRect")
      .mockImplementation(function (this: Element) {
        if (this.getAttribute("aria-label") === "Mais reações") {
          return {
            x: 0,
            y: 900,
            left: 0,
            right: 30,
            top: 900,
            bottom: 930,
            width: 30,
            height: 30,
            toJSON: () => ({}),
          };
        }
        return {
          x: 0,
          y: 0,
          left: 0,
          right: 0,
          top: 0,
          bottom: 0,
          width: 0,
          height: 0,
          toJSON: () => ({}),
        };
      });
    renderChannelArea();

    await openFullReactionPicker();

    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Escolher reação" })).not.toBeInTheDocument(),
    );
    rectSpy.mockRestore();
  });

  it("keeps the portaled reaction picker anchored when the message list scrolls", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelArea();

    await openFullReactionPicker();
    fireEvent.scroll(screen.getByRole("log", { name: "Mensagens" }));

    expect(screen.getByRole("dialog", { name: "Escolher reação" })).toBeInTheDocument();
  });

  it("keeps only one message reaction picker open", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1" }), makeMessage({ id: "m2" })]),
    );
    renderChannelArea();

    await openFullReactionPicker(0);
    expect(screen.getAllByRole("dialog", { name: "Escolher reação" })).toHaveLength(1);

    fireEvent.keyDown(document, { key: "Escape" });
    await openFullReactionPicker(1);
    expect(screen.getAllByRole("dialog", { name: "Escolher reação" })).toHaveLength(1);
  });

  it("renders the reaction pill after the authoritative WS update", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();

    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    await userEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));
    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "👍");

    act(() =>
      wsMockState.capturedReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "geral",
        message_id: "m1",
        reaction: {
          message_id: "m1",
          actor_user_id: "me-123",
          emoji: "👍",
          added: true,
          reactions: [{ emoji: "👍", count: 1 }],
        },
      }),
    );

    expect(await screen.findByRole("button", { name: "Remover reação 👍" })).toHaveTextContent("1");
  });

  // Somebody else reacting with the same emoji moves the confirmed count, but it
  // is not this reader's confirmation: their own toggle stays on top of it.
  it("stacks the reader's pending reaction on another reader's confirmed one", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();

    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    await userEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));

    act(() =>
      wsMockState.capturedReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "geral",
        message_id: "m1",
        reaction: {
          message_id: "m1",
          actor_user_id: "other-user",
          emoji: "👍",
          added: true,
          reactions: [
            { emoji: "👍", count: 1, users: [{ user_id: "other-user", display_name: "Bruna" }] },
          ],
        },
      }),
    );

    const badge = await screen.findByRole("button", { name: "Remover reação 👍" });
    expect(badge).toHaveTextContent("2");
    expect(badge).toHaveAccessibleDescription("👍: Você e Bruna");
  });

  // Whether a reaction may exist is the server's decision, and nothing this
  // client draws can pre-empt it: the toggle below is as legitimate as one gets
  // — the server's own quick row — and the server still refuses it. What the
  // reader must not be left with is the reaction they never got: the optimistic
  // badge goes away again and the refusal is read.
  it("rolls the optimistic reaction back when the server refuses the toggle", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    fireEvent.mouseEnter(await screen.findByTestId("chat-msg-bubble"));
    expect(screen.queryByRole("button", { name: /reação 👍/ })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));
    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "👍");

    // Drawn before any confirmation: that is the whole point of the optimism,
    // and the whole reason a refusal has to undo it.
    const optimistic = await screen.findByRole("button", { name: "Remover reação 👍" });
    expect(optimistic).toHaveAttribute("aria-pressed", "true");
    expect(optimistic).toHaveTextContent("1");

    act(() =>
      wsMockState.capturedReactionError?.({
        type: "error",
        operation: "reaction",
        code: "invalid_emoji",
      }),
    );

    // The reaction only ever existed locally, so back to confirmed state is back
    // to no badge at all — not a badge showing zero.
    await waitFor(() => expect(screen.queryByRole("button", { name: /reação 👍/ })).toBeNull());
    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível atualizar a reação.");
  });

  it("renders N reactions and toggles an existing pill without opening the grid", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [
            { emoji: "👍", count: 2, reactedByMe: false, users: [] },
            { emoji: "🎉", count: 4, reactedByMe: true, users: [] },
          ],
        }),
      ]),
    );
    renderChannelArea();

    await userEvent.click(await screen.findByRole("button", { name: "Adicionar reação 👍" }));

    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "👍");
    expect(screen.getByRole("button", { name: "Remover reação 🎉" })).toHaveTextContent("4");
    expect(screen.queryByRole("dialog", { name: "Escolher reação" })).not.toBeInTheDocument();
  });

  it("throttles repeated clicks on the same reaction for 300ms", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [{ emoji: "👍", count: 1, reactedByMe: false, users: [] }],
        }),
      ]),
    );
    const now = vi.spyOn(Date, "now").mockReturnValue(1_000);
    renderChannelArea();
    const reaction = await screen.findByRole("button", { name: "Adicionar reação 👍" });

    await userEvent.click(reaction);
    await userEvent.click(reaction);
    now.mockReturnValue(1_301);
    await userEvent.click(reaction);

    expect(wsMockState.toggleReaction).toHaveBeenCalledTimes(2);
    now.mockRestore();
  });

  it("shows visible feedback when the reaction socket is unavailable", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [{ emoji: "👍", count: 1, reactedByMe: false, users: [] }],
        }),
      ]),
    );
    wsMockState.toggleReaction.mockReturnValueOnce(false);
    renderChannelArea();

    await userEvent.click(await screen.findByRole("button", { name: "Adicionar reação 👍" }));

    expect(screen.getByRole("alert")).toHaveTextContent(/tempo real indisponível/i);
  });

  it("renders messages from API response", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ bodyText: "Olá, mundo!" })]),
    );
    renderChannelArea();

    await waitFor(() => {
      expect(screen.getByText("Olá, mundo!")).toBeInTheDocument();
    });
  });

  it("renders multiple messages", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({ id: "m1", bodyText: "Primeira mensagem" }),
        makeMessage({ id: "m2", bodyText: "Segunda mensagem" }),
      ]),
    );
    renderChannelArea();

    await waitFor(() => {
      expect(screen.getByText("Primeira mensagem")).toBeInTheDocument();
      expect(screen.getByText("Segunda mensagem")).toBeInTheDocument();
    });
  });

  it("shows placeholder for removed messages instead of body text", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", isRemoved: true, bodyText: "" })]),
    );
    renderChannelArea();

    await waitFor(() => {
      expect(screen.getByText(/mensagem removida/i)).toBeInTheDocument();
    });
  });

  it("removed message does not render body text", async () => {
    const secretBody = "CONTEÚDO-SECRETO";
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", isRemoved: true, bodyText: secretBody })]),
    );
    renderChannelArea();

    await waitFor(() => {
      expect(screen.queryByText(secretBody)).not.toBeInTheDocument();
    });
  });
});

// ── RF-12 typing indicator display name ───────────────────────────────────────

describe("ChatMessageArea — RF-12 typing indicator display name", () => {
  it("shows the server-provided display name even when the local heuristic has no name for that user", async () => {
    // Channel conversation, no messages loaded from "user-elias" and no DM
    // roster at all (this is a channel) — the old client-side heuristic
    // (typingNameByUserId) has no way to resolve this user, and would have
    // fallen back to "Alguém".
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", senderId: "me-123", senderDisplayName: "Me" })]),
    );
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    act(() =>
      wsMockState.capturedOnTypingUpdated?.({
        type: "typing.updated",
        target_type: "channel",
        target_id: "geral",
        typing: {
          user_id: "user-elias",
          user_display_name: "Elias Rocha",
          is_typing: true,
        },
      }),
    );

    const indicator = await screen.findByTestId("chat-typing-indicator");
    expect(indicator).toHaveTextContent("Elias Rocha está digitando");
    expect(indicator).not.toHaveTextContent("Alguém");
  });

  it("shows an aggregate label for two, then three or more, simultaneous typists", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", senderId: "me-123", senderDisplayName: "Me" })]),
    );
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    act(() =>
      wsMockState.capturedOnTypingUpdated?.({
        type: "typing.updated",
        target_type: "channel",
        target_id: "geral",
        typing: { user_id: "user-a", user_display_name: "Ana", is_typing: true },
      }),
    );
    act(() =>
      wsMockState.capturedOnTypingUpdated?.({
        type: "typing.updated",
        target_type: "channel",
        target_id: "geral",
        typing: { user_id: "user-b", user_display_name: "Bruno", is_typing: true },
      }),
    );
    expect(await screen.findByTestId("chat-typing-indicator")).toHaveTextContent(
      "Ana e Bruno estão digitando",
    );

    act(() =>
      wsMockState.capturedOnTypingUpdated?.({
        type: "typing.updated",
        target_type: "channel",
        target_id: "geral",
        typing: { user_id: "user-c", user_display_name: "Carla", is_typing: true },
      }),
    );
    expect(await screen.findByTestId("chat-typing-indicator")).toHaveTextContent(
      "3 pessoas estão digitando",
    );
  });

  it("in a DM, falls back to the roster's display name when the server sends none", async () => {
    mockFetchDMMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", senderId: "me-123", senderDisplayName: "Me" })]),
    );
    render(
      <MemoryRouter initialEntries={["/chat/dm/dm-juliane"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [],
                  dms: [
                    {
                      id: "dm-juliane",
                      type: "1:1",
                      name: "Juliane",
                      participants: [
                        {
                          id: "user-elias",
                          displayName: "Elias Rocha",
                          initials: "ER",
                          color: "purple",
                          status: "online",
                        },
                      ],
                    },
                  ],
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("chat-msg-bubble");

    act(() =>
      wsMockState.capturedOnTypingUpdated?.({
        type: "typing.updated",
        target_type: "dm",
        target_id: "dm-juliane",
        typing: { user_id: "user-elias", is_typing: true },
      }),
    );

    const indicator = await screen.findByTestId("chat-typing-indicator");
    expect(indicator).toHaveTextContent("Elias Rocha está digitando");
  });
});

// ── Message grouping (same sender / same minute) ──────────────────────────────

describe("ChatMessageArea — message grouping", () => {
  // Helper: build an ISO timestamp at a specific "YYYY-MM-DDTHH:MM" minute.
  function isoAt(minute: string, seconds = "00") {
    return `${minute}:${seconds}.000Z`;
  }

  const SENDER_A = "user-alice";
  const SENDER_B = "user-bob";

  it("consecutive messages from the same sender in the same minute — second has no sender label", async () => {
    const sameMinute = "2024-01-15T10:04";
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          senderId: SENDER_A,
          senderDisplayName: "Alice",
          bodyText: "Primeira",
          createdAt: isoAt(sameMinute, "10"),
        }),
        makeMessage({
          id: "m2",
          senderId: SENDER_A,
          senderDisplayName: "Alice",
          bodyText: "Segunda",
          createdAt: isoAt(sameMinute, "45"),
        }),
      ]),
    );
    renderChannelArea();

    await waitFor(() => expect(screen.getByText("Primeira")).toBeInTheDocument());

    // Both messages visible
    expect(screen.getByText("Segunda")).toBeInTheDocument();

    // Alice's name appears only ONCE (only the first message shows sender)
    const senderLabels = screen.getAllByTestId("chat-msg-sender");
    expect(senderLabels).toHaveLength(1);
    expect(senderLabels[0]).toHaveTextContent("Alice");
  });

  it("different sender — sender label appears on every message", async () => {
    const sameMinute = "2024-01-15T10:04";
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          senderId: SENDER_A,
          senderDisplayName: "Alice",
          bodyText: "Olá",
          createdAt: isoAt(sameMinute, "10"),
        }),
        makeMessage({
          id: "m2",
          senderId: SENDER_B,
          senderDisplayName: "Bob",
          bodyText: "Oi",
          createdAt: isoAt(sameMinute, "45"),
        }),
      ]),
    );
    renderChannelArea();

    await waitFor(() => expect(screen.getByText("Olá")).toBeInTheDocument());

    // Both senders must have their label visible
    const senderLabels = screen.getAllByTestId("chat-msg-sender");
    expect(senderLabels).toHaveLength(2);
    expect(senderLabels[0]).toHaveTextContent("Alice");
    expect(senderLabels[1]).toHaveTextContent("Bob");
  });

  it("same sender but different minute — second message shows sender label", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          senderId: SENDER_A,
          senderDisplayName: "Alice",
          bodyText: "Mensagem das 10:04",
          createdAt: isoAt("2024-01-15T10:04", "00"),
        }),
        makeMessage({
          id: "m2",
          senderId: SENDER_A,
          senderDisplayName: "Alice",
          bodyText: "Mensagem das 10:05",
          createdAt: isoAt("2024-01-15T10:05", "00"),
        }),
      ]),
    );
    renderChannelArea();

    await waitFor(() => expect(screen.getByText("Mensagem das 10:04")).toBeInTheDocument());

    // Different minutes → no grouping → two sender labels
    const senderLabels = screen.getAllByTestId("chat-msg-sender");
    expect(senderLabels).toHaveLength(2);
  });

  it("day divider resets grouping — first message after divider shows sender label", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          senderId: SENDER_A,
          senderDisplayName: "Alice",
          bodyText: "Ontem",
          createdAt: "2024-01-14T10:04:00.000Z",
        }),
        makeMessage({
          id: "m2",
          senderId: SENDER_A,
          senderDisplayName: "Alice",
          bodyText: "Hoje",
          createdAt: "2024-01-15T10:04:00.000Z",
        }),
      ]),
    );
    renderChannelArea();

    await waitFor(() => expect(screen.getByText("Ontem")).toBeInTheDocument());

    // Day divider breaks grouping — both messages must show sender label
    const senderLabels = screen.getAllByTestId("chat-msg-sender");
    expect(senderLabels).toHaveLength(2);
  });
});

describe("ChatMessageArea — send message", () => {
  it("send button calls postChannelMessage with correct args", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockPostChannelMessage.mockResolvedValue(
      makeMessage({ id: "new-1", bodyText: "Nova mensagem" }),
    );
    renderChannelArea("geral");

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "Nova mensagem");

    const sendBtn = screen.getByTestId("chat-send-btn");
    await user.click(sendBtn);

    await waitFor(() => {
      expect(mockPostChannelMessage).toHaveBeenCalledTimes(1);
      // Must not include author_id — the mock call args should only be (channelId, bodyText)
      const [channelId, bodyText] = mockPostChannelMessage.mock.calls[0];
      expect(channelId).toBe("geral");
      expect(bodyText).toBe("Nova mensagem");
    });
  });

  it("send DM calls postDMMessage", async () => {
    const user = userEvent.setup();
    mockFetchDMMessages.mockResolvedValue(emptyPage);
    mockPostDMMessage.mockResolvedValue(makeMessage({ id: "new-2", bodyText: "Oi!" }));
    renderDMArea("dm-juliane");

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "Oi!");

    const sendBtn = screen.getByTestId("chat-send-btn");
    await user.click(sendBtn);

    await waitFor(() => {
      expect(mockPostDMMessage).toHaveBeenCalledTimes(1);
      const [dmId, bodyText] = mockPostDMMessage.mock.calls[0];
      expect(dmId).toBe("dm-juliane");
      expect(bodyText).toBe("Oi!");
    });
  });

  it("input cleared after successful send", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockPostChannelMessage.mockResolvedValue(makeMessage({ bodyText: "Teste" }));
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "Teste");
    await user.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => {
      expect(input.textContent?.trim()).toBe("");
    });
  });

  it("send button is disabled when input is empty", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    renderChannelArea();

    const sendBtn = await screen.findByTestId("chat-send-btn");
    expect(sendBtn).toBeDisabled();
  });

  it("Enter key sends message via TipTap keyboard shortcut (exercises submitOnEnter extension)", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockPostChannelMessage.mockResolvedValue(makeMessage({ bodyText: "Enter send" }));
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "Enter send");

    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });

    await waitFor(() => {
      expect(mockPostChannelMessage).toHaveBeenCalledTimes(1);
    });
  });

  it("Shift+Enter creates a hard break instead of sending", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockPostChannelMessage.mockResolvedValue(makeMessage({ bodyText: "line one\nline two" }));
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "line one");

    fireEvent.keyDown(input, { key: "Enter", code: "Enter", shiftKey: true });

    await waitFor(() => expect(input.querySelector("br")).not.toBeNull());
    await fillEditor(input, "line two");
    fireEvent.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => {
      expect(mockPostChannelMessage).toHaveBeenCalledTimes(1);
      expect(mockPostChannelMessage.mock.calls[0]?.[1]).toBe("line one\nline two");
    });
  });

  it("failed send shows error banner and keeps draft", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockPostChannelMessage.mockRejectedValue(new Error("Servidor indisponível"));
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "mensagem com erro");
    await user.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => {
      expect(screen.getByTestId("chat-send-error")).toBeInTheDocument();
    });

    // Content must be preserved so the user can retry or edit.
    expect(input).toHaveTextContent("mensagem com erro");
  });

  it("non-Error rejection shows generic error message", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    // Reject with a non-Error value (e.g. a plain string) to cover the fallback error message.
    mockPostChannelMessage.mockRejectedValue("raw string failure");
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "test");
    await user.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => {
      expect(screen.getByTestId("chat-send-error")).toBeInTheDocument();
    });
  });

  it("reply action opens the composer quote and cancel keeps the draft", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          senderDisplayName: "Ana",
          bodyText: "**texto citado**",
          bodyFormat: "v2",
        }),
      ]),
    );
    renderChannelAreaForUser();

    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    await user.click(screen.getByRole("button", { name: "Responder" }));

    const input = screen.getByTestId("chat-composer-input");
    await waitFor(() => expect(input).toHaveFocus());
    expect(screen.getByTestId("chat-composer-quote")).toHaveTextContent("Ana");
    expect(screen.getByTestId("chat-composer-quote")).toHaveTextContent("texto citado");

    await fillEditor(input, "rascunho");
    await user.click(screen.getByRole("button", { name: "Cancelar resposta" }));

    expect(screen.queryByTestId("chat-composer-quote")).not.toBeInTheDocument();
    expect(input).toHaveTextContent("rascunho");
  });

  it("Escape closes the composer quote", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", senderDisplayName: "Ana" })]),
    );
    renderChannelAreaForUser();

    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    await userEvent.click(screen.getByRole("button", { name: "Responder" }));

    const input = screen.getByTestId("chat-composer-input");
    fireEvent.keyDown(input, { key: "Escape", code: "Escape" });

    await waitFor(() =>
      expect(screen.queryByTestId("chat-composer-quote")).not.toBeInTheDocument(),
    );
  });

  it("send with active reply includes parent_message_id and renders the returned quote", async () => {
    const user = userEvent.setup();
    const parent = makeMessage({
      id: "m1",
      senderId: "user-parent",
      senderDisplayName: "Ana",
      bodyText: "mensagem original",
      bodyFormat: "v2",
    });
    mockFetchChannelMessages.mockResolvedValue(messagePage([parent]));
    mockPostChannelMessage.mockResolvedValue(
      makeMessage({
        id: "m2",
        senderId: "me-123",
        bodyText: "resposta",
        bodyFormat: "v3",
        quoted: {
          id: "m1",
          authorId: "user-parent",
          bodyText: "mensagem original",
          bodyFormat: "v2",
          isRemoved: false,
          deletedAt: null,
          createdAt: parent.createdAt,
          linkSafetyState: "",
        },
      }),
    );
    renderChannelAreaForUser();

    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    await user.click(screen.getByRole("button", { name: "Responder" }));
    await fillEditor(screen.getByTestId("chat-composer-input"), "resposta");
    await user.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => expect(mockPostChannelMessage).toHaveBeenCalledTimes(1));
    expect(mockPostChannelMessage.mock.calls[0]?.[2]).toMatchObject({ parentMessageId: "m1" });
    await waitFor(() =>
      expect(screen.queryByTestId("chat-composer-quote")).not.toBeInTheDocument(),
    );
    expect(screen.getByTestId("chat-message-quote")).toHaveTextContent("Ana");
    expect(screen.getByTestId("chat-message-quote")).toHaveTextContent("mensagem original");
  });

  it("renders unavailable quoted messages without fetching the original", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m2",
          quoted: {
            id: "missing-parent",
            authorId: "user-parent",
            bodyText: "",
            bodyFormat: "v2",
            isRemoved: true,
            deletedAt: new Date().toISOString(),
            createdAt: new Date().toISOString(),
            linkSafetyState: "",
          },
        }),
      ]),
    );
    renderChannelAreaForUser();

    const quote = await screen.findByTestId("chat-message-quote");

    expect(quote).toHaveTextContent("Mensagem original indisponível.");
    expect(quote).toHaveAttribute("aria-disabled", "true");
    const scrollIntoViewMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    scrollIntoViewMock.mockClear();
    fireEvent.keyDown(quote, { key: "Enter" });
    expect(scrollIntoViewMock).not.toHaveBeenCalled();
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
  });

  it("uses a safe author fallback for an active quote whose parent is unavailable", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m2",
          quoted: {
            id: "missing-parent",
            authorId: "",
            bodyText: "trecho preservado",
            bodyFormat: "v3",
            isRemoved: false,
            deletedAt: null,
            createdAt: new Date().toISOString(),
            linkSafetyState: "",
          },
        }),
      ]),
    );
    renderChannelAreaForUser();

    const quote = await screen.findByTestId("chat-message-quote");
    expect(quote).toHaveTextContent("Usuário desconhecido");
    expect(quote).toHaveTextContent("trecho preservado");
  });

  it("clicking a loaded quote scrolls to the original message", async () => {
    const parent = makeMessage({ id: "m1", senderDisplayName: "Ana", bodyText: "original" });
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        parent,
        makeMessage({
          id: "m2",
          bodyText: "resposta",
          quoted: {
            id: "m1",
            authorId: parent.senderId,
            bodyText: parent.bodyText,
            bodyFormat: parent.bodyFormat,
            isRemoved: false,
            deletedAt: null,
            createdAt: parent.createdAt,
            linkSafetyState: "",
          },
        }),
      ]),
    );
    renderChannelAreaForUser();

    const quotes = await screen.findAllByTestId("chat-message-quote");
    const originalMessageElement = document.querySelector('[data-message-id="m1"]');
    expect(originalMessageElement).not.toBeNull();
    const scrollIntoViewMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    scrollIntoViewMock.mockClear();

    await userEvent.click(quotes[0]);

    expect(scrollIntoViewMock).toHaveBeenCalledWith({
      behavior: "smooth",
      block: "center",
    });
    expect(scrollIntoViewMock.mock.contexts[0]).toBe(originalMessageElement);
  });

  it("keyboard jump highlights the original message briefly", async () => {
    const parent = makeMessage({ id: "m1", senderDisplayName: "Ana", bodyText: "original" });
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        parent,
        makeMessage({
          id: "m2",
          bodyText: "resposta",
          quoted: {
            id: "m1",
            authorId: parent.senderId,
            bodyText: parent.bodyText,
            bodyFormat: parent.bodyFormat,
            isRemoved: false,
            deletedAt: null,
            createdAt: parent.createdAt,
            linkSafetyState: "",
          },
        }),
      ]),
    );
    renderChannelAreaForUser();

    const quotes = await screen.findAllByTestId("chat-message-quote");
    const originalMessageElement = document.querySelector('[data-message-id="m1"]');
    expect(originalMessageElement).not.toBeNull();
    const scrollIntoViewMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    scrollIntoViewMock.mockClear();
    vi.useFakeTimers();

    fireEvent.keyDown(quotes[0], { key: "Enter" });

    expect(scrollIntoViewMock).toHaveBeenCalledWith({
      behavior: "smooth",
      block: "center",
    });
    expect(originalMessageElement).toHaveClass("chat-msg-area__msg--highlight");

    act(() => vi.advanceTimersByTime(1_200));

    expect(originalMessageElement).not.toHaveClass("chat-msg-area__msg--highlight");
  });
});

describe("ChatMessageArea — RF-09 cross-channel references", () => {
  it("renders only the generic unavailable state", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ bodyText: "destino", reference: { available: false } })]),
    );
    renderChannelAreaForUser();

    const reference = await screen.findByTestId("chat-message-reference");
    expect(reference).toHaveTextContent("citação indisponível");
    expect(reference).toHaveAttribute("aria-disabled", "true");
    expect(reference).not.toHaveAttribute("role");
    expect(reference).not.toHaveAttribute("tabindex");
    expect(reference).not.toHaveAttribute("title");
    expect(reference).not.toHaveAttribute("data-message-id");
  });

  it("renders authorized rich text as safe React text", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          reference: {
            available: true,
            messageId: "source-1",
            targetType: "channel",
            targetId: "private-1",
            targetLabel: "privado",
            authorDisplayName: "Ana",
            bodyText: "<img src=x onerror=alert(1)>",
            bodyFormat: "v3",
            createdAt: new Date().toISOString(),
            linkSafetyState: "",
          },
        }),
      ]),
    );
    renderChannelAreaForUser();

    const reference = await screen.findByTestId("chat-message-reference");
    expect(reference).toHaveTextContent("#privado");
    expect(reference).toHaveTextContent("<img src=x onerror=alert(1)>");
    expect(reference.querySelector("img")).toBeNull();
    expect(reference.querySelector("script")).toBeNull();
    const referenceLink = screen.getByRole("link", { name: "Ir para mensagem citada" });
    expect(referenceLink).not.toHaveAttribute("aria-label", expect.stringContaining("privado"));
    expect(referenceLink).not.toHaveAttribute("aria-label", expect.stringContaining("Ana"));
    referenceLink.focus();
    await userEvent.keyboard(" ");
    await waitFor(() =>
      expect(mockFetchChannelMessages).toHaveBeenCalledWith(
        "private-1",
        undefined,
        expect.any(AbortSignal),
      ),
    );
  });

  it("ignores unrelated keys and follows an authorized reference by click", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          reference: {
            available: true,
            messageId: "source-1",
            targetType: "channel",
            targetId: "private-1",
            targetLabel: "privado",
            authorDisplayName: "Ana",
            bodyText: "origem",
            bodyFormat: "v3",
            createdAt: new Date().toISOString(),
            linkSafetyState: "",
          },
        }),
      ]),
    );
    renderChannelAreaForUser();
    const reference = await screen.findByRole("link", { name: "Ir para mensagem citada" });

    fireEvent.keyDown(reference, { key: "x" });
    expect(mockFetchChannelMessages).not.toHaveBeenCalledWith(
      "private-1",
      undefined,
      expect.any(AbortSignal),
    );
    await userEvent.click(reference);

    await waitFor(() =>
      expect(mockFetchChannelMessages).toHaveBeenCalledWith(
        "private-1",
        undefined,
        expect.any(AbortSignal),
      ),
    );
  });

  it("uses generic visible labels for an authorized DM reference without optional names", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          reference: {
            available: true,
            messageId: "source-dm",
            targetType: "dm",
            targetId: "dm-private",
            targetLabel: "",
            authorDisplayName: "",
            bodyText: "- um\n- dois",
            bodyFormat: "v3",
            createdAt: new Date().toISOString(),
            linkSafetyState: "",
          },
        }),
      ]),
    );
    renderChannelAreaForUser();

    const reference = await screen.findByTestId("chat-message-reference");
    expect(reference).toHaveTextContent("Conversa");
    expect(reference).toHaveTextContent("Usuário");
    expect(reference.querySelector(".chat-msg-area__reference-body ul")).not.toBeNull();
    expect(reference.querySelector("button ul")).toBeNull();
  });

  it.each([
    ["null", null],
    ["primitive", "not-an-object"],
    [
      "empty message ID",
      {
        referencedMessageId: "",
        referenceTargetKind: "channel",
        referenceTargetId: rf09SourceChannelID,
      },
    ],
    [
      "whitespace message ID",
      {
        referencedMessageId: "   ",
        referenceTargetKind: "channel",
        referenceTargetId: rf09SourceChannelID,
      },
    ],
    [
      "malformed message ID",
      {
        referencedMessageId: "../../private-message?leak=true",
        referenceTargetKind: "channel",
        referenceTargetId: rf09SourceChannelID,
      },
    ],
    [
      "oversized message ID",
      {
        referencedMessageId: "a".repeat(10_000),
        referenceTargetKind: "channel",
        referenceTargetId: rf09SourceChannelID,
      },
    ],
    [
      "nil message UUID",
      {
        referencedMessageId: "00000000-0000-0000-0000-000000000000",
        referenceTargetKind: "channel",
        referenceTargetId: rf09SourceChannelID,
      },
    ],
    [
      "empty target ID",
      {
        referencedMessageId: rf09SourceMessageID,
        referenceTargetKind: "channel",
        referenceTargetId: "",
      },
    ],
    [
      "malformed target ID",
      {
        referencedMessageId: rf09SourceMessageID,
        referenceTargetKind: "channel",
        referenceTargetId: "javascript:alert(1)",
      },
    ],
    [
      "unknown target kind",
      {
        referencedMessageId: rf09SourceMessageID,
        referenceTargetKind: "thread",
        referenceTargetId: rf09SourceChannelID,
      },
    ],
    [
      "missing target kind",
      {
        referencedMessageId: rf09SourceMessageID,
        referenceTargetId: rf09SourceChannelID,
      },
    ],
  ])("ignores %s without resolving a reference", async (_name, state) => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    renderPendingReferenceState(state);

    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalled());
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
    expect(mockFetchDMMessage).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-composer-reference")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private-message");
    expect(mockPostChannelMessage).not.toHaveBeenCalled();
  });

  it("does not send an invalid pending reference", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockPostChannelMessage.mockResolvedValue(makeMessage({ id: "created", bodyText: "seguro" }));
    renderPendingReferenceState({
      referencedMessageId: "not-a-uuid",
      referenceTargetKind: "channel",
      referenceTargetId: rf09SourceChannelID,
    });

    await screen.findByTestId("chat-msg-empty");
    await fillEditor(screen.getByTestId("chat-composer-input"), "seguro");
    await userEvent.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => expect(mockPostChannelMessage).toHaveBeenCalledTimes(1));
    expect(mockPostChannelMessage.mock.calls[0]?.slice(0, 3)).toEqual([
      "destination",
      "seguro",
      {
        parentMessageId: undefined,
        referencedMessageId: undefined,
        attachmentIds: undefined,
        idempotencyKey: expect.any(String),
      },
    ]);
    expect(mockFetchChannelMessage).not.toHaveBeenCalled();
    expect(mockFetchDMMessage).not.toHaveBeenCalled();
  });

  it("loads an authorized preview once without expanding or persisting nested data", async () => {
    let resolveSource!: (message: Message) => void;
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockReturnValue(
      new Promise<Message>((resolve) => {
        resolveSource = resolve;
      }),
    );
    renderPendingReference();

    const loading = await screen.findByTestId("chat-composer-reference");
    expect(loading).toHaveTextContent("Carregando citação…");
    expect(screen.getByRole("status")).toHaveTextContent("Carregando citação…");
    expect(mockFetchChannelMessage).toHaveBeenCalledTimes(1);
    expect(mockFetchChannelMessage).toHaveBeenCalledWith(
      rf09SourceChannelID,
      rf09SourceMessageID,
      expect.any(AbortSignal),
    );

    resolveSource(
      makeMessage({
        id: rf09SourceMessageID,
        senderDisplayName: "Autora protegida",
        bodyText: "**conteúdo autorizado**",
        bodyFormat: "v3",
        reference: {
          available: true,
          messageId: "nested-secret-id",
          targetType: "channel",
          targetId: "nested-private-channel",
          targetLabel: "Canal aninhado secreto",
          authorDisplayName: "Autor aninhado",
          bodyText: "segredo aninhado",
          bodyFormat: "v3",
          createdAt: new Date().toISOString(),
          linkSafetyState: "",
        },
      }),
    );

    const preview = await screen.findByTestId("chat-composer-reference");
    expect(preview).toHaveTextContent("Autora protegida · #Origem privada");
    expect(preview).toHaveTextContent("conteúdo autorizado");
    expect(preview.querySelector("strong")).not.toBeNull();
    expect(preview).not.toHaveTextContent("segredo aninhado");
    expect(preview).not.toHaveTextContent("Canal aninhado secreto");
    const persisted = [localStorage, sessionStorage].flatMap((storage) =>
      Array.from({ length: storage.length }, (_, index) =>
        storage.getItem(storage.key(index) ?? ""),
      ),
    );
    expect(persisted.join(" ")).not.toContain("conteúdo autorizado");
    expect(persisted.join(" ")).not.toContain("Autora protegida");
  });

  it.each([403, 404])("fails closed when preview GET returns %i", async (status) => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockRejectedValue(Object.assign(new Error("protected"), { status }));
    renderPendingReference();

    const preview = await screen.findByTestId("chat-composer-reference");
    await waitFor(() => expect(preview).toHaveTextContent("citação indisponível"));
    const html = preview.outerHTML;
    expect(html).not.toContain("Origem privada");
    expect(html).not.toContain("protected");
    expect(html).not.toContain(rf09SourceMessageID);
    expect(preview).not.toHaveAttribute("title");
  });

  it("uses the authorized DM single-message GET for a DM origin", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchDMMessage.mockResolvedValue(
      makeMessage({
        id: rf09SourceMessageID,
        senderDisplayName: "Bruno",
        bodyText: "origem em DM",
      }),
    );
    renderPendingReference(rf09SourceMessageID, { kind: "dm", id: rf09SourceDMID });

    expect(await screen.findByTestId("chat-composer-reference")).toHaveTextContent(
      "Bruno · Conversa privada",
    );
    expect(mockFetchDMMessage).toHaveBeenCalledWith(
      rf09SourceDMID,
      rf09SourceMessageID,
      expect.any(AbortSignal),
    );
  });

  it("accepts canonical UUIDs with uppercase hexadecimal digits", async () => {
    const messageID = "ABCDEF12-3456-4789-ABCD-EF1234567890";
    const channelID = "ABCDEF12-3456-4789-8BCD-EF1234567891";
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockResolvedValue(
      makeMessage({ id: messageID, senderDisplayName: "Ana", bodyText: "origem válida" }),
    );
    renderPendingReference(messageID, { kind: "channel", id: channelID });

    expect(await screen.findByTestId("chat-composer-reference")).toHaveTextContent("origem válida");
    expect(mockFetchChannelMessage).toHaveBeenCalledWith(
      channelID,
      messageID,
      expect.any(AbortSignal),
    );
  });

  it("aborts the preview request on unmount", async () => {
    let signal: AbortSignal | undefined;
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockImplementation((_channelId, _messageId, requestSignal) => {
      signal = requestSignal;
      return new Promise<Message>(() => undefined);
    });
    const view = renderPendingReference();

    await waitFor(() => expect(signal).toBeDefined());
    view.unmount();
    expect(signal?.aborted).toBe(true);
  });

  it("cancels an in-flight preview and ignores its late response", async () => {
    let signal: AbortSignal | undefined;
    let resolveSource!: (message: Message) => void;
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockImplementation((_channelId, _messageId, requestSignal) => {
      signal = requestSignal;
      return new Promise<Message>((resolve) => {
        resolveSource = resolve;
      });
    });
    renderPendingReference();
    await screen.findByText("Carregando citação…");

    await userEvent.click(screen.getByRole("button", { name: "Cancelar citação" }));
    expect(signal?.aborted).toBe(true);
    resolveSource(makeMessage({ id: rf09SourceMessageID, bodyText: "conteúdo que chegou tarde" }));
    await act(async () => undefined);
    expect(screen.queryByTestId("chat-composer-reference")).not.toBeInTheDocument();
    expect(screen.queryByText("conteúdo que chegou tarde")).not.toBeInTheDocument();
  });

  it("aborts a stale ID request and renders only the latest selection", async () => {
    let firstSignal: AbortSignal | undefined;
    let resolveFirst!: (message: Message) => void;
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockImplementation((_channelId, messageId, signal) => {
      if (messageId === rf09SourceMessageID) {
        firstSignal = signal;
        return new Promise<Message>((resolve) => {
          resolveFirst = resolve;
        });
      }
      return Promise.resolve(
        makeMessage({
          id: rf09SecondMessageID,
          senderDisplayName: "Nova",
          bodyText: "nova origem",
        }),
      );
    });

    function ReferenceSwitcher() {
      const navigate = useNavigate();
      return (
        <>
          <button
            type="button"
            onClick={() =>
              navigate("/chat/channel/destination", {
                state: {
                  referencedMessageId: rf09SecondMessageID,
                  referenceTargetKind: "channel",
                  referenceTargetId: rf09SourceChannelID,
                },
              })
            }
          >
            Trocar origem
          </button>
          <Outlet
            context={{
              currentUserId: "me-123",
              channels: [
                { id: "destination", name: "Destino", type: "public" },
                { id: rf09SourceChannelID, name: "Origem privada", type: "private" },
              ],
              dms: [],
            }}
          />
        </>
      );
    }

    render(
      <MemoryRouter
        initialEntries={[
          {
            pathname: "/chat/channel/destination",
            state: {
              referencedMessageId: rf09SourceMessageID,
              referenceTargetKind: "channel",
              referenceTargetId: rf09SourceChannelID,
            },
          },
        ]}
      >
        <Routes>
          <Route path="/chat" element={<ReferenceSwitcher />}>
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByText("Carregando citação…");

    await userEvent.click(screen.getByRole("button", { name: "Trocar origem" }));
    expect(firstSignal?.aborted).toBe(true);
    expect(await screen.findByText("nova origem")).toBeInTheDocument();
    resolveFirst(makeMessage({ id: rf09SourceMessageID, bodyText: "origem obsoleta" }));
    await act(async () => undefined);
    expect(screen.queryByText("origem obsoleta")).not.toBeInTheDocument();
    expect(mockFetchChannelMessage).toHaveBeenCalledTimes(2);
  });

  it("preserves the authorized reference after a failed send", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockResolvedValue(
      makeMessage({ id: rf09SourceMessageID, senderDisplayName: "Ana", bodyText: "origem" }),
    );
    mockPostChannelMessage.mockRejectedValue(new Error("send failed"));
    renderPendingReference();
    await screen.findByText("origem");

    await fillEditor(screen.getByTestId("chat-composer-input"), "tentar enviar");
    await userEvent.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => expect(mockPostChannelMessage).toHaveBeenCalledTimes(1));
    expect(screen.getByTestId("chat-composer-reference")).toHaveTextContent("origem");
  });

  it("selects another channel and sends only the referenced message ID", async () => {
    const source = makeMessage({
      id: rf09SourceMessageID,
      senderDisplayName: "Ana",
      bodyText: "**origem autorizada**",
      bodyFormat: "v3",
    });
    mockFetchChannelMessages.mockImplementation(async (id) =>
      id === rf09SourceChannelID ? messagePage([source]) : emptyPage,
    );
    mockFetchChannelMessage.mockResolvedValue(source);
    mockPostChannelMessage.mockResolvedValue(makeMessage({ id: "created", bodyText: "veja" }));
    render(
      <MemoryRouter initialEntries={[`/chat/channel/${rf09SourceChannelID}`]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [
                    {
                      id: rf09SourceChannelID,
                      name: "Origem",
                      type: "public",
                      canWrite: true,
                    },
                    { id: "destination", name: "Destino", type: "public", canWrite: true },
                  ],
                  dms: [],
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    fireEvent.mouseEnter(await screen.findByTestId("chat-msg-bubble"));
    const referenceAction = screen.getByRole("button", { name: "Citar em outra conversa" });
    await userEvent.click(referenceAction);
    const closeDialog = screen.getByRole("button", { name: "Fechar" });
    const destinationButton = screen.getByRole("button", { name: /Destino/ });
    expect(closeDialog).toHaveFocus();
    destinationButton.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(closeDialog).toHaveFocus();
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(destinationButton).toHaveFocus();
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(referenceAction).toHaveFocus();

    await userEvent.click(referenceAction);
    await userEvent.click(screen.getByRole("button", { name: /Destino/ }));
    const preview = await screen.findByTestId("chat-composer-reference");
    expect(preview).toHaveTextContent("Ana · #Origem");
    expect(preview).toHaveTextContent("origem autorizada");
    expect(preview.querySelector("strong")).not.toBeNull();
    expect(mockFetchChannelMessage).toHaveBeenCalledTimes(1);
    expect(mockFetchChannelMessage).toHaveBeenCalledWith(
      rf09SourceChannelID,
      rf09SourceMessageID,
      expect.any(AbortSignal),
    );
    await fillEditor(screen.getByTestId("chat-composer-input"), "veja");
    await userEvent.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => expect(mockPostChannelMessage).toHaveBeenCalledTimes(1));
    expect(mockPostChannelMessage.mock.calls[0]?.slice(0, 3)).toEqual([
      "destination",
      "veja",
      {
        parentMessageId: undefined,
        referencedMessageId: rf09SourceMessageID,
        attachmentIds: undefined,
        idempotencyKey: expect.any(String),
      },
    ]);
    await waitFor(() =>
      expect(screen.queryByTestId("chat-composer-reference")).not.toBeInTheDocument(),
    );
  });

  it("loads and highlights a directly navigated source outside the latest page", async () => {
    const focused = makeMessage({ id: "older-message", bodyText: "mensagem antiga" });
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockFetchChannelMessage.mockResolvedValue(focused);
    render(
      <MemoryRouter initialEntries={["/chat/channel/source?message=older-message"]}>
        <Routes>
          <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("mensagem antiga")).toBeInTheDocument();
    expect(mockFetchChannelMessage).toHaveBeenCalledWith(
      "source",
      "older-message",
      expect.any(AbortSignal),
    );
    expect(window.Element.prototype.scrollIntoView).toHaveBeenCalledWith({
      behavior: "smooth",
      block: "center",
    });
  });
});

// ── Stale response guard ──────────────────────────────────────────────────────

describe("ChatMessageArea — stale response guard", () => {
  it("focuses the new composer only after the switched conversation finishes loading", async () => {
    const user = userEvent.setup();
    let resolveChannel2!: (page: MessagePage) => void;
    mockFetchChannelMessages
      .mockResolvedValueOnce(emptyPage)
      .mockReturnValueOnce(new Promise((resolve) => (resolveChannel2 = resolve)));

    function TwoChannelTest() {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate("/chat/channel/canal-2")}>Ir para canal 2</button>
          <Routes>
            <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Routes>
        </div>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/canal-1"]}>
        <TwoChannelTest />
      </MemoryRouter>,
    );

    const channel1Input = await screen.findByTestId("chat-composer-input");
    await waitFor(() => expect(channel1Input).toHaveFocus());

    const switchButton = screen.getByRole("button", { name: "Ir para canal 2" });
    await user.click(switchButton);

    const channel2Input = await screen.findByTestId("chat-composer-input");
    expect(channel2Input).not.toBe(channel1Input);
    await waitFor(() => expect(channel2Input).toHaveAttribute("aria-disabled", "true"));
    expect(switchButton).toHaveFocus();

    const focus = vi.spyOn(channel2Input, "focus");
    await act(async () => resolveChannel2(emptyPage));

    await waitFor(() => expect(channel2Input).toHaveFocus());
    expect(focus).toHaveBeenCalledOnce();
    expect(focus).toHaveBeenCalledWith({ preventScroll: true });
  });

  it("allows only the final composer to autofocus during a rapid conversation switch", async () => {
    const user = userEvent.setup();
    let resolveChannel2!: (page: MessagePage) => void;
    let resolveChannel3!: (page: MessagePage) => void;
    mockFetchChannelMessages
      .mockResolvedValueOnce(emptyPage)
      .mockReturnValueOnce(new Promise((resolve) => (resolveChannel2 = resolve)))
      .mockReturnValueOnce(new Promise((resolve) => (resolveChannel3 = resolve)));

    function ThreeChannelTest() {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate("/chat/channel/canal-2")}>Ir para canal 2</button>
          <button onClick={() => navigate("/chat/channel/canal-3")}>Ir para canal 3</button>
          <Routes>
            <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Routes>
        </div>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/canal-1"]}>
        <ThreeChannelTest />
      </MemoryRouter>,
    );

    await waitFor(() => expect(screen.getByTestId("chat-composer-input")).toHaveFocus());
    await user.click(screen.getByRole("button", { name: "Ir para canal 2" }));
    const channel2Input = await screen.findByTestId("chat-composer-input");
    await waitFor(() => expect(channel2Input).toHaveAttribute("aria-disabled", "true"));
    const channel2Focus = vi.spyOn(channel2Input, "focus");

    const channel3Button = screen.getByRole("button", { name: "Ir para canal 3" });
    await user.click(channel3Button);
    const channel3Input = await screen.findByTestId("chat-composer-input");
    await waitFor(() => expect(channel3Input).toHaveAttribute("aria-disabled", "true"));
    const channel3Focus = vi.spyOn(channel3Input, "focus");

    await act(async () => resolveChannel2(emptyPage));
    expect(channel2Focus).not.toHaveBeenCalled();
    expect(channel3Button).toHaveFocus();

    await act(async () => resolveChannel3(emptyPage));
    await waitFor(() => expect(channel3Input).toHaveFocus());
    expect(channel2Focus).not.toHaveBeenCalled();
    expect(channel3Focus).toHaveBeenCalledOnce();
  });

  it("response for old target does not render when target changes", async () => {
    const user = userEvent.setup();

    let resolveChannel1: (p: MessagePage) => void;
    const channel1Promise = new Promise<MessagePage>((r) => (resolveChannel1 = r));
    const channel2Page = messagePage([makeMessage({ bodyText: "Mensagem do canal 2" })]);

    // First call → hangs; second call → resolves immediately.
    mockFetchChannelMessages
      .mockReturnValueOnce(channel1Promise)
      .mockResolvedValueOnce(channel2Page);

    // Use a nav button to switch channels inside the same Router.
    function TwoChannelTest() {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate("/chat/channel/canal-2")}>Ir para canal 2</button>
          <Routes>
            <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Routes>
        </div>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/canal-1"]}>
        <TwoChannelTest />
      </MemoryRouter>,
    );

    // Switch to canal-2 while canal-1 is still loading.
    await user.click(screen.getByRole("button", { name: "Ir para canal 2" }));

    const currentInput = await screen.findByTestId("chat-composer-input");
    await waitFor(() => expect(currentInput).toHaveFocus());

    // Now let canal-1's stale request resolve.
    resolveChannel1!(messagePage([makeMessage({ bodyText: "Mensagem do canal 1" })]));

    // Canal-2 content must appear.
    await waitFor(() => {
      expect(screen.getByText("Mensagem do canal 2")).toBeInTheDocument();
    });

    // Canal-1 stale message must NOT appear.
    expect(screen.queryByText("Mensagem do canal 1")).not.toBeInTheDocument();
  });

  it("stale send error from old target does not appear in new target", async () => {
    const user = userEvent.setup();

    // Canal-1 messages load immediately; canal-2 messages load immediately.
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    let rejectSendA: (err: Error) => void;
    const sendAPromise = new Promise<never>((_, rej) => (rejectSendA = rej));
    // Prevent Node.js / Vitest from reporting this as unhandled before
    // sendMessage's catch block runs. The no-op .catch marks the promise as
    // handled so the unhandledRejection event never fires.
    sendAPromise.catch(() => undefined);
    mockPostChannelMessage.mockReturnValueOnce(sendAPromise);

    function TwoChannelTest() {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate("/chat/channel/canal-2")}>Ir para canal 2</button>
          <Routes>
            <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Routes>
        </div>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/canal-1"]}>
        <TwoChannelTest />
      </MemoryRouter>,
    );

    // Wait for canal-1 to be ready, then type + send (POST hangs).
    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "mensagem A");
    await user.click(screen.getByTestId("chat-send-btn"));

    // Navigate to canal-2 while POST for canal-1 is still in-flight.
    await user.click(screen.getByRole("button", { name: "Ir para canal 2" }));

    // Fail the stale POST for canal-1 after navigation.
    rejectSendA!(new Error("Erro do canal A"));

    // Allow microtasks to settle.
    await waitFor(() => {
      expect(screen.queryByTestId("chat-send-error")).not.toBeInTheDocument();
    });
  });

  it("successful send from old target does not add message to new target", async () => {
    const user = userEvent.setup();

    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    let resolveSendA: (m: ReturnType<typeof makeMessage>) => void;
    const sendAPromise = new Promise<ReturnType<typeof makeMessage>>((res) => (resolveSendA = res));
    mockPostChannelMessage.mockReturnValueOnce(sendAPromise);

    function TwoChannelTest() {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate("/chat/channel/canal-2")}>Ir para canal 2</button>
          <Routes>
            <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Routes>
        </div>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/canal-1"]}>
        <TwoChannelTest />
      </MemoryRouter>,
    );

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "mensagem A sucesso");
    await user.click(screen.getByTestId("chat-send-btn"));

    // Navigate to canal-2 while POST for canal-1 is still in-flight.
    await user.click(screen.getByRole("button", { name: "Ir para canal 2" }));

    // Successfully resolve the stale POST for canal-1.
    resolveSendA!(makeMessage({ bodyText: "mensagem A sucesso" }));

    await waitFor(() => {
      // Canal-2 is ready (empty state or loading done) and shows none of canal-1's content.
      const bubbles = screen.queryAllByTestId("chat-msg-bubble");
      expect(bubbles.some((b) => b.textContent?.includes("mensagem A sucesso"))).toBe(false);
    });
  });

  it("stale send success leaves the new target's composer empty and usable", async () => {
    const user = userEvent.setup();

    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    let resolveSendA: (m: ReturnType<typeof makeMessage>) => void;
    const sendAPromise = new Promise<ReturnType<typeof makeMessage>>((res) => (resolveSendA = res));
    mockPostChannelMessage.mockReturnValueOnce(sendAPromise);

    function TwoChannelTest() {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate("/chat/channel/canal-2")}>Ir para canal 2</button>
          <Routes>
            <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Routes>
        </div>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/canal-1"]}>
        <TwoChannelTest />
      </MemoryRouter>,
    );

    // Wait for canal-1 to be ready, type message, and send (POST hangs).
    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "rascunho canal 1");
    await user.click(screen.getByTestId("chat-send-btn"));

    // Navigate to canal-2 while the POST for canal-1 is still in-flight. The
    // composer is keyed by target, so canal-1's draft is destroyed with it.
    await user.click(screen.getByRole("button", { name: "Ir para canal 2" }));
    const currentInput = await screen.findByTestId("chat-composer-input");
    await waitFor(() => expect(currentInput).toHaveFocus());

    // Resolve the stale POST for canal-1 — should return { status: "stale" }.
    await act(async () => {
      resolveSendA!(makeMessage({ bodyText: "rascunho canal 1" }));
    });

    expect(currentInput).toHaveFocus();

    // Canal-1's draft must never surface in canal-2 — neither in the composer
    // nor in the timeline — and the stale completion must not touch either.
    expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("");
    const bubbles = screen.queryAllByTestId("chat-msg-bubble");
    expect(bubbles.some((b) => b.textContent?.includes("rascunho canal 1"))).toBe(false);
    // No error banner in canal-2.
    expect(screen.queryByTestId("chat-send-error")).not.toBeInTheDocument();
    // The fresh composer is not stuck in a sending state: new content can be sent.
    await fillEditor(screen.getByTestId("chat-composer-input"), "rascunho canal 2");
    await waitFor(() => expect(screen.getByTestId("chat-send-btn")).not.toBeDisabled());
  });

  it("stale send failure shows no error and leaves the new target's composer empty", async () => {
    const user = userEvent.setup();

    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    let rejectSendA: (err: Error) => void;
    const sendAPromise = new Promise<never>((_, rej) => (rejectSendA = rej));
    // Prevent Node.js / Vitest from reporting this as unhandled before
    // sendMessage's catch block runs. The no-op .catch marks the promise as
    // handled so the unhandledRejection event never fires.
    sendAPromise.catch(() => undefined);
    mockPostChannelMessage.mockReturnValueOnce(sendAPromise);

    function TwoChannelTest() {
      const navigate = useNavigate();
      return (
        <div>
          <button onClick={() => navigate("/chat/channel/canal-2")}>Ir para canal 2</button>
          <Routes>
            <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Routes>
        </div>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/canal-1"]}>
        <TwoChannelTest />
      </MemoryRouter>,
    );

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "rascunho canal 1");
    await user.click(screen.getByTestId("chat-send-btn"));

    // Navigate to canal-2 while POST is still in-flight.
    await user.click(screen.getByRole("button", { name: "Ir para canal 2" }));

    // Reject the stale POST for canal-1 — should return { status: "stale" }, no throw.
    await act(async () => {
      rejectSendA!(new Error("Erro do canal A"));
    });

    // Canal-1's draft was destroyed with its composer and must not reappear.
    expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("");
    // No error banner in canal-2.
    expect(screen.queryByTestId("chat-send-error")).not.toBeInTheDocument();
    // The fresh composer is not stuck in a sending state: new content can be sent.
    await fillEditor(screen.getByTestId("chat-composer-input"), "rascunho canal 2");
    await waitFor(() => expect(screen.getByTestId("chat-send-btn")).not.toBeDisabled());
  });
});

// ── Security: no HTML injection ───────────────────────────────────────────────

describe("ChatMessageArea — XSS safety", () => {
  it("message text is not rendered as HTML", async () => {
    const xssBody = '<img src="x" onerror="window.XSS_FIRED=true">';
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ bodyText: xssBody })]));
    renderChannelArea();

    // Wait for messages to load.
    await waitFor(() => {
      expect(screen.getAllByTestId("chat-msg-bubble").length).toBeGreaterThan(0);
    });

    // No <img> element should have been injected into the DOM.
    expect(document.querySelector('img[src="x"]')).toBeNull();

    // The bubble text content should be the raw string, not parsed HTML.
    const bubble = screen.getAllByTestId("chat-msg-bubble")[0];
    expect(bubble.textContent).toContain("<img");
  });
});

// ── Security: no storage writes ───────────────────────────────────────────────

describe("ChatMessageArea — storage safety", () => {
  it("mounting message area writes nothing to localStorage or sessionStorage", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ bodyText: "Seguro" })]));

    setTokens("test-at");
    const setItemSpy = vi.spyOn(Storage.prototype, "setItem");

    renderChannelArea();

    await waitFor(() => {
      expect(screen.getByText("Seguro")).toBeInTheDocument();
    });

    expect(setItemSpy).not.toHaveBeenCalled();
    setItemSpy.mockRestore();
  });
});

// ── Infinite scroll (load older messages) ─────────────────────────────────────

// capturedIOCallback is scoped to this describe so that it is isolated from
// tests that don't interact with IntersectionObserver.
describe("ChatMessageArea — infinite scroll", () => {
  let capturedIOCallback: IntersectionObserverCallback | null = null;

  beforeEach(() => {
    capturedIOCallback = null;
    // jsdom does not implement IntersectionObserver — provide a class-based stub.
    // Arrow functions cannot be used as constructors with `new`, so a class is required.
    class MockIO {
      constructor(cb: IntersectionObserverCallback) {
        capturedIOCallback = cb;
      }
      observe = vi.fn();
      disconnect = vi.fn();
      unobserve = vi.fn();
    }
    vi.stubGlobal("IntersectionObserver", MockIO);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("loads older messages when top sentinel becomes visible and renders them", async () => {
    const cursor = "dGVzdC1jdXJzb3I";
    const oldMsg = makeMessage({ id: "old-1", bodyText: "Mensagem antiga" });
    const newMsg = makeMessage({ id: "new-1", bodyText: "Mensagem recente" });

    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: [newMsg], nextCursor: cursor })
      .mockResolvedValueOnce({ messages: [oldMsg], nextCursor: "" });

    renderChannelArea();

    // Wait for first page to render.
    await waitFor(() => {
      expect(screen.getByText("Mensagem recente")).toBeInTheDocument();
    });

    // Simulate user scrolling to top — IntersectionObserver fires.
    act(() => {
      capturedIOCallback?.(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
    });

    // Older message should appear.
    await waitFor(() => {
      expect(screen.getByText("Mensagem antiga")).toBeInTheDocument();
    });
    // Recent message still visible (no replacement).
    expect(screen.getByText("Mensagem recente")).toBeInTheDocument();
    // fetchChannelMessages called twice: initial + loadMore with cursor.
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2);
    expect(mockFetchChannelMessages).toHaveBeenNthCalledWith(
      2,
      "geral",
      cursor,
      expect.any(AbortSignal),
    );
  });

  it("shows loading indicator while older messages are fetching", async () => {
    const cursor = "dGVzdA";
    let resolveLoadMore: (p: MessagePage) => void;
    const loadMorePromise = new Promise<MessagePage>((r) => (resolveLoadMore = r));

    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: [makeMessage({ id: "m1" })], nextCursor: cursor })
      .mockReturnValueOnce(loadMorePromise);

    renderChannelArea();
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble")).toHaveLength(1));

    act(() => {
      capturedIOCallback?.(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
    });

    expect(await screen.findByTestId("load-more-indicator")).toBeInTheDocument();

    resolveLoadMore!({ messages: [], nextCursor: "" });
    await waitFor(() =>
      expect(screen.queryByTestId("load-more-indicator")).not.toBeInTheDocument(),
    );
  });

  it("does not call loadMore when nextCursor is empty (hasMore=false)", async () => {
    mockFetchChannelMessages.mockResolvedValue({ messages: [makeMessage()], nextCursor: "" });

    renderChannelArea();
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble")).toHaveLength(1));

    // With hasMore=false no IntersectionObserver is created, so callback is null.
    // Invoking it (a no-op) should not trigger a second fetch.
    act(() => {
      capturedIOCallback?.(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
    });

    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(1);
  });

  it("does not duplicate messages returned by both pages", async () => {
    const cursor = "dGVzdC1jdXJzb3I";
    const dupMsg = makeMessage({ id: "dup-1", bodyText: "Mensagem duplicada" });
    const uniqueOld = makeMessage({ id: "old-2", bodyText: "Mensagem única antiga" });

    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: [dupMsg], nextCursor: cursor })
      .mockResolvedValueOnce({ messages: [uniqueOld, dupMsg], nextCursor: "" });

    renderChannelArea();
    await waitFor(() => expect(screen.getByText("Mensagem duplicada")).toBeInTheDocument());

    act(() => {
      capturedIOCallback?.(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
    });

    await waitFor(() => expect(screen.getByText("Mensagem única antiga")).toBeInTheDocument());

    // Duplicate message should appear exactly once despite being in both pages.
    expect(screen.getAllByText("Mensagem duplicada")).toHaveLength(1);
  });

  it("DM: loads older messages on scroll to top", async () => {
    const cursor = "ZG0tY3Vyc29y";
    const oldMsg = makeMessage({ id: "dm-old-1", bodyText: "DM antiga" });
    const newMsg = makeMessage({ id: "dm-new-1", bodyText: "DM recente" });

    mockFetchDMMessages
      .mockResolvedValueOnce({ messages: [newMsg], nextCursor: cursor })
      .mockResolvedValueOnce({ messages: [oldMsg], nextCursor: "" });

    renderDMArea();

    await waitFor(() => expect(screen.getByText("DM recente")).toBeInTheDocument());

    act(() => {
      capturedIOCallback?.(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
    });

    await waitFor(() => expect(screen.getByText("DM antiga")).toBeInTheDocument());
    expect(mockFetchDMMessages).toHaveBeenCalledTimes(2);
    expect(mockFetchDMMessages).toHaveBeenNthCalledWith(
      2,
      "dm-juliane",
      cursor,
      expect.any(AbortSignal),
    );
  });

  // ── IO loop prevention ──────────────────────────────────────────────────────

  it("does not loop-fetch when sentinel stays visible after loadingMore resets", async () => {
    // This test uses an auto-firing mock: observe() immediately triggers the callback
    // to simulate a sentinel that never leaves the viewport. With the old implementation
    // (loadingMore in effect deps), recreating the observer after each fetch would cause
    // an extra fetch per cycle. With the new implementation ([hasMore] deps only), the
    // observer is not recreated when loadingMore changes, so no extra fetch occurs.
    let observeCallCount = 0;
    let localCaptured: IntersectionObserverCallback | null = null;
    class AutoFireMockIO {
      constructor(cb: IntersectionObserverCallback) {
        localCaptured = cb;
      }
      observe = vi.fn(() => {
        observeCallCount++;
        localCaptured?.(
          [{ isIntersecting: true } as IntersectionObserverEntry],
          {} as IntersectionObserver,
        );
      });
      disconnect = vi.fn();
      unobserve = vi.fn();
    }
    vi.stubGlobal("IntersectionObserver", AutoFireMockIO);

    const cursor = "bG9vcC1jdXJzb3I";
    mockFetchChannelMessages
      .mockResolvedValueOnce({
        messages: [makeMessage({ id: "n1", bodyText: "Nova" })],
        nextCursor: cursor,
      })
      .mockResolvedValueOnce({
        messages: [makeMessage({ id: "o1", bodyText: "Antiga" })],
        nextCursor: "",
      });

    renderChannelArea();

    // Wait for both fetches to complete (initial + one loadMore triggered by auto-fire).
    await waitFor(() => expect(screen.getByText("Antiga")).toBeInTheDocument());

    // The top (loadMore) sentinel's observer was created once (when hasMore
    // became true) and observe() fired once; after prepend, hasMore=false →
    // effect re-runs with !hasMore → returns early, no new observer. The
    // bottom (#492) sentinel mounts exactly once regardless of hasMore and
    // contributes its own single observe() call — it does not loop either,
    // since its effect has no hasMore/messages dependency to re-run on.
    expect(observeCallCount).toBe(2);
    // Exactly two fetches: initial load + one loadMore. The bottom sentinel's
    // auto-fire never triggers a fetch of its own.
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2);
  });

  it("fires only one loadMore fetch when IO callback fires twice in the same tick", async () => {
    // Verifies that stateRef.current.loadingMore is updated synchronously inside
    // loadMore() so that a second IO callback in the same act() — before React
    // re-renders — fails the guard and does not dispatch a duplicate request.
    const cursor = "ZG91YmxlY3Vyc29y";
    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: [makeMessage({ id: "n1" })], nextCursor: cursor })
      .mockResolvedValueOnce({ messages: [makeMessage({ id: "o1" })], nextCursor: "" });

    renderChannelArea();
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble")).toHaveLength(1));

    // Fire the IO callback twice in the same act() — simulates two rapid sentinel
    // visibility events before the next React render. Only one loadMore should start.
    act(() => {
      capturedIOCallback?.(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
      capturedIOCallback?.(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
    });

    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble")).toHaveLength(2));

    // Initial load (1) + exactly one loadMore (1) = 2 total calls.
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2);
  });

  // ── loadMore error recovery ─────────────────────────────────────────────────

  it("loadingMore resets to false when loadMore fetch fails", async () => {
    const cursor = "ZXJyb3JDdXJzb3I";
    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: [makeMessage({ id: "m1" })], nextCursor: cursor })
      .mockRejectedValueOnce(new Error("network error"));

    renderChannelArea();
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble")).toHaveLength(1));

    act(() => {
      capturedIOCallback?.(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
    });

    // Loading indicator appears then disappears as error resets loadingMore.
    await waitFor(() =>
      expect(screen.queryByTestId("load-more-indicator")).not.toBeInTheDocument(),
    );
    // Component is still usable — messages still visible.
    expect(screen.getAllByTestId("chat-msg-bubble")).toHaveLength(1);
  });

  // ── Scroll behavior ─────────────────────────────────────────────────────────

  it("does not call scrollIntoView on prepend (scroll delta is used instead)", async () => {
    const cursor = "cHJlcGVuZEN1cnNvcg";
    mockFetchChannelMessages
      .mockResolvedValueOnce({ messages: [makeMessage({ id: "n1" })], nextCursor: cursor })
      .mockResolvedValueOnce({ messages: [makeMessage({ id: "o1" })], nextCursor: "" });

    renderChannelArea();

    // Wait for first page AND for the IntersectionObserver to be registered (useEffect
    // has run). In slow CI environments waitFor can return as soon as the DOM condition
    // is met, before the [hasMore] effect has fully committed. Combining both checks in
    // one waitFor guarantees capturedIOCallback is non-null before we fire it.
    await waitFor(() => {
      expect(screen.getAllByTestId("chat-msg-bubble")).toHaveLength(1);
      expect(capturedIOCallback).not.toBeNull();
    });

    // beforeEach sets window.Element.prototype.scrollIntoView = vi.fn().
    // Clear its call history here to only count calls triggered by prepend.
    const scrollMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    scrollMock.mockClear();

    act(() => {
      capturedIOCallback!(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver,
      );
    });

    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble")).toHaveLength(2));

    // Prepend must not call scrollIntoView (it uses scrollTop delta instead).
    expect(scrollMock).not.toHaveBeenCalled();
  });

  it("calls scrollIntoView when a new message is sent (append)", async () => {
    mockFetchChannelMessages.mockResolvedValue({
      messages: [makeMessage({ id: "m1" })],
      nextCursor: "",
    });
    mockPostChannelMessage.mockResolvedValue(makeMessage({ id: "m2", bodyText: "Enviada" }));

    renderChannelArea();
    await waitFor(() => expect(screen.getAllByTestId("chat-msg-bubble")).toHaveLength(1));

    // beforeEach sets window.Element.prototype.scrollIntoView = vi.fn().
    // Clear its call history here to only count calls triggered by the send.
    const scrollMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    scrollMock.mockClear();

    const input = screen.getByTestId("chat-composer-input");
    await fillEditor(input, "Enviada");
    await userEvent.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => expect(screen.getByText("Enviada")).toBeInTheDocument());

    // Append (sent) must scroll to bottom.
    expect(scrollMock).toHaveBeenCalledTimes(1);
  });
});

// ── Security: no runtime fixture import ──────────────────────────────────────

describe("ChatMessageArea — no runtime fixture import", () => {
  it("ChatMessageArea.tsx does not import from chatFixtures", async () => {
    const { readFileSync } = await import("node:fs");
    const { resolve } = await import("node:path");
    const src = readFileSync(resolve(__dirname, "ChatMessageArea.tsx"), "utf-8");
    expect(src).not.toMatch(/from\s+["'].*chatFixtures/);
    expect(src).not.toMatch(/import\s*\(["'].*chatFixtures/);
  });

  it("useMessages.ts does not import from chatFixtures", async () => {
    const { readFileSync } = await import("node:fs");
    const { resolve } = await import("node:path");
    const src = readFileSync(resolve(__dirname, "useMessages.ts"), "utf-8");
    expect(src).not.toMatch(/from\s+["'].*chatFixtures/);
    expect(src).not.toMatch(/import\s*\(["'].*chatFixtures/);
  });
});

// ── Route decoding ────────────────────────────────────────────────────────────

describe("ChatMessageArea — route decoding", () => {
  it("uses the canonical UUID identity for REST and target-scoped data", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    const canonical = "550e8400-e29b-41d4-a716-446655440000";

    renderChannelArea("550E8400-E29B-41D4-A716-446655440000");

    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalled());
    expect(mockFetchChannelMessages).toHaveBeenCalledWith(
      canonical,
      undefined,
      expect.any(AbortSignal),
    );
    expect(mockFetchPins).toHaveBeenCalledWith(
      { kind: "channel", id: canonical },
      expect.any(AbortSignal),
    );
  });

  it("decodes percent-encoded channel ID correctly", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    render(
      <MemoryRouter initialEntries={["/chat/channel/equipe%20infra"]}>
        <Routes>
          <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
        </Routes>
      </MemoryRouter>,
    );

    const header = await screen.findByTestId("chat-msg-header");
    expect(header).toHaveTextContent("equipe infra");
  });

  it("does not crash with malformed percent-encoded ID", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    render(
      <MemoryRouter initialEntries={["/chat/channel/%"]}>
        <Routes>
          <Route path="/chat/channel/:id" element={<ChatMessageArea kind="channel" />} />
        </Routes>
      </MemoryRouter>,
    );

    // Must not throw; area should mount with whatever raw value is available.
    expect(await screen.findByTestId("chat-message-area")).toBeInTheDocument();
  });
});

// ── Outlet context helper ─────────────────────────────────────────────────────

function ParentWithContext({ ctx }: { ctx: ChatOutletContext }) {
  return (
    <div>
      <Outlet context={ctx} />
    </div>
  );
}

function CurrentPath() {
  return <span data-testid="current-path">{useLocation().pathname}</span>;
}

// ── Message alignment ─────────────────────────────────────────────────────────

describe("ChatMessageArea — message alignment", () => {
  it("own message has --mine modifier class", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "me-123", senderDisplayName: "Me" })]),
    );

    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat/channel"
            element={<ParentWithContext ctx={{ currentUserId: "me-123", channels: [], dms: [] }} />}
          >
            <Route path=":id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const bubble = await screen.findByTestId("chat-msg-bubble");
    expect(bubble.className).toContain("chat-msg-area__msg--mine");
  });

  it("other user message does not have --mine modifier class", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Outro" })]),
    );

    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat/channel"
            element={<ParentWithContext ctx={{ currentUserId: "me-123", channels: [], dms: [] }} />}
          >
            <Route path=":id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const bubble = await screen.findByTestId("chat-msg-bubble");
    expect(bubble.className).not.toContain("chat-msg-area__msg--mine");
  });
});

// ── Sender display name ───────────────────────────────────────────────────────

describe("ChatMessageArea — sender display", () => {
  it("shows senderDisplayName in sender span", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          senderId: "other-456",
          senderDisplayName: "Fernanda Nicácio",
          senderEmail: "",
        }),
      ]),
    );
    renderChannelArea();

    await waitFor(() => {
      expect(screen.getByTestId("chat-msg-sender")).toHaveTextContent("Fernanda Nicácio");
    });
  });

  it("falls back to senderEmail when senderDisplayName is empty", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          senderId: "other-456",
          senderDisplayName: "",
          senderEmail: "fernanda@example.com",
        }),
      ]),
    );
    renderChannelArea();

    await waitFor(() => {
      expect(screen.getByTestId("chat-msg-sender")).toHaveTextContent("fernanda@example.com");
    });
  });

  it("falls back to senderId prefix when both display fields are empty", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          senderId: "abcdef12-0000-0000-0000-000000000000",
          senderDisplayName: "",
          senderEmail: "",
        }),
      ]),
    );
    renderChannelArea();

    await waitFor(() => {
      expect(screen.getByTestId("chat-msg-sender")).toHaveTextContent("abcdef12");
    });
  });

  it("renders another sender name as an accessible DM action", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda" })]),
    );
    renderChannelAreaForUser("me-123");

    const sender = await screen.findByRole("button", {
      name: "Abrir conversa com Fernanda",
    });
    expect(sender).toHaveAttribute("type", "button");
    sender.focus();
    expect(sender).toHaveFocus();
  });

  it("opens the canonical DM from the sender id, refreshes sidebar, and navigates", async () => {
    const refreshConversations = vi.fn();
    mockGetOrCreateDirectDM.mockResolvedValue({
      conversationId: "dm/with space",
      created: false,
    });
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda" })]),
    );

    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{ currentUserId: "me-123", channels: [], dms: [], refreshConversations }}
              />
            }
          >
            <Route
              path="channel/:id"
              element={
                <>
                  <ChatMessageArea kind="channel" />
                  <CurrentPath />
                </>
              }
            />
            <Route path="dm/:id" element={<CurrentPath />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "Abrir conversa com Fernanda" }),
    );

    await waitFor(() => expect(mockGetOrCreateDirectDM).toHaveBeenCalledTimes(1));
    expect(mockGetOrCreateDirectDM).toHaveBeenCalledWith("other-456", expect.any(AbortSignal));
    await waitFor(() => expect(refreshConversations).toHaveBeenCalledTimes(1));
    expect(await screen.findByTestId("current-path")).toHaveTextContent(
      "/chat/dm/dm%2Fwith%20space",
    );
  });

  it("keyboard activation opens a DM", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda" })]),
    );
    renderChannelAreaForUser("me-123");

    const sender = await screen.findByRole("button", { name: "Abrir conversa com Fernanda" });
    sender.focus();
    await user.keyboard("{Enter}");

    await waitFor(() =>
      expect(mockGetOrCreateDirectDM).toHaveBeenCalledWith("other-456", expect.any(AbortSignal)),
    );
  });

  it("coalesces rapid repeated opens per recipient and communicates pending state", async () => {
    let resolveOpen!: (value: { conversationId: string; created: boolean }) => void;
    mockGetOrCreateDirectDM.mockReturnValue(
      new Promise((resolve) => {
        resolveOpen = resolve;
      }),
    );
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda" })]),
    );
    renderChannelAreaForUser("me-123");

    const sender = await screen.findByRole("button", { name: "Abrir conversa com Fernanda" });
    fireEvent.click(sender);
    fireEvent.click(sender);

    expect(mockGetOrCreateDirectDM).toHaveBeenCalledTimes(1);
    expect(sender).toHaveAttribute("aria-busy", "true");
    expect(sender).toBeDisabled();

    resolveOpen({ conversationId: "dm-456", created: true });
    await waitFor(() => expect(sender).not.toBeDisabled());
  });

  it("allows independent pending opens for different recipients", async () => {
    mockGetOrCreateDirectDM.mockImplementation(() => new Promise(() => {}));
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({ id: "msg-fernanda", senderId: "user-1", senderDisplayName: "Fernanda" }),
        makeMessage({ id: "msg-marcos", senderId: "user-2", senderDisplayName: "Marcos" }),
      ]),
    );
    renderChannelAreaForUser("me-123");

    const fernanda = await screen.findByRole("button", {
      name: "Abrir conversa com Fernanda",
    });
    const marcos = screen.getByRole("button", { name: "Abrir conversa com Marcos" });
    fireEvent.click(fernanda);
    fireEvent.click(marcos);

    expect(mockGetOrCreateDirectDM).toHaveBeenCalledTimes(2);
    expect(fernanda).toBeDisabled();
    expect(marcos).toBeDisabled();
  });

  it("invalidates a pending author DM open when the reused area changes channel", async () => {
    const refreshConversations = vi.fn();
    let resolveOpen!: (value: { conversationId: string; created: boolean }) => void;
    let capturedSignal: AbortSignal | undefined;
    mockGetOrCreateDirectDM.mockImplementation((_otherUserId, signal) => {
      capturedSignal = signal;
      return new Promise((resolve) => {
        resolveOpen = resolve;
      });
    });
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda" })]),
    );

    function SwitchChannel() {
      const switchChannel = useNavigate();
      return (
        <>
          <button type="button" onClick={() => switchChannel("/chat/channel/B")}>
            Trocar canal
          </button>
          <CurrentPath />
        </>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/A"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{ currentUserId: "me-123", channels: [], dms: [], refreshConversations }}
              />
            }
          >
            <Route
              path="channel/:id"
              element={
                <>
                  <ChatMessageArea kind="channel" />
                  <SwitchChannel />
                </>
              }
            />
            <Route path="dm/:id" element={<CurrentPath />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const area = await screen.findByTestId("chat-message-area");
    fireEvent.click(screen.getByRole("button", { name: "Abrir conversa com Fernanda" }));
    fireEvent.click(screen.getByRole("button", { name: "Trocar canal" }));

    await waitFor(() =>
      expect(screen.getByTestId("current-path")).toHaveTextContent("/chat/channel/B"),
    );
    expect(screen.getByTestId("chat-message-area")).toBe(area);
    expect(capturedSignal?.aborted).toBe(true);

    await act(async () => {
      resolveOpen({ conversationId: "dm-from-A", created: true });
    });
    expect(refreshConversations).not.toHaveBeenCalled();
    expect(screen.getByTestId("current-path")).toHaveTextContent("/chat/channel/B");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps a new open registered when the invalidated operation settles late", async () => {
    const refreshConversations = vi.fn();
    const resolvers: Array<(value: { conversationId: string; created: boolean }) => void> = [];
    const signals: AbortSignal[] = [];
    mockGetOrCreateDirectDM.mockImplementation((_otherUserId, signal) => {
      if (signal) signals.push(signal);
      return new Promise((resolve) => resolvers.push(resolve));
    });
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda" })]),
    );

    function SwitchChannel() {
      const switchChannel = useNavigate();
      return (
        <>
          <button type="button" onClick={() => switchChannel("/chat/channel/B")}>
            Trocar canal
          </button>
          <CurrentPath />
        </>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/A"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{ currentUserId: "me-123", channels: [], dms: [], refreshConversations }}
              />
            }
          >
            <Route
              path="channel/:id"
              element={
                <>
                  <ChatMessageArea kind="channel" />
                  <SwitchChannel />
                </>
              }
            />
            <Route path="dm/:id" element={<CurrentPath />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByRole("button", { name: "Abrir conversa com Fernanda" }));
    fireEvent.click(screen.getByRole("button", { name: "Trocar canal" }));
    await waitFor(() => expect(signals[0]?.aborted).toBe(true));

    const senderInB = await screen.findByRole("button", {
      name: "Abrir conversa com Fernanda",
    });
    fireEvent.click(senderInB);
    expect(mockGetOrCreateDirectDM).toHaveBeenCalledTimes(2);
    expect(senderInB).toBeDisabled();

    await act(async () => {
      resolvers[0]({ conversationId: "dm-from-A", created: true });
    });
    expect(senderInB).toBeDisabled();
    expect(refreshConversations).not.toHaveBeenCalled();

    await act(async () => {
      resolvers[1]({ conversationId: "dm-from-B", created: true });
    });
    await waitFor(() => expect(refreshConversations).toHaveBeenCalledTimes(1));
    expect(await screen.findByTestId("current-path")).toHaveTextContent("/chat/dm/dm-from-B");
  });

  it("aborts a pending author DM open and skips refresh/navigation after unmount", async () => {
    const refreshConversations = vi.fn();
    let resolveOpen!: (value: { conversationId: string; created: boolean }) => void;
    let capturedSignal: AbortSignal | undefined;
    mockGetOrCreateDirectDM.mockImplementation((_otherUserId, signal) => {
      capturedSignal = signal;
      return new Promise((resolve) => {
        resolveOpen = resolve;
      });
    });
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda" })]),
    );

    function LeaveConversation() {
      const navigateAway = useNavigate();
      return (
        <>
          <button type="button" onClick={() => navigateAway("/else")}>
            Sair da conversa
          </button>
          <CurrentPath />
        </>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{ currentUserId: "me-123", channels: [], dms: [], refreshConversations }}
              />
            }
          >
            <Route
              path="channel/:id"
              element={
                <>
                  <ChatMessageArea kind="channel" />
                  <LeaveConversation />
                </>
              }
            />
            <Route path="dm/:id" element={<CurrentPath />} />
          </Route>
          <Route path="/else" element={<CurrentPath />} />
        </Routes>
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByRole("button", { name: "Abrir conversa com Fernanda" }));
    expect(mockGetOrCreateDirectDM).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: "Sair da conversa" }));
    expect(await screen.findByTestId("current-path")).toHaveTextContent("/else");
    expect(capturedSignal?.aborted).toBe(true);

    await act(async () => {
      resolveOpen({ conversationId: "dm-after-unmount", created: true });
    });
    expect(refreshConversations).not.toHaveBeenCalled();
    expect(screen.getByTestId("current-path")).toHaveTextContent("/else");
  });

  it("keeps the current conversation usable after a generic open failure", async () => {
    mockGetOrCreateDirectDM.mockRejectedValue(new ApiRequestError(403, "forbidden", "Forbidden"));
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          senderId: "other-456",
          senderDisplayName: "Fernanda",
          bodyText: "mensagem atual",
        }),
      ]),
    );
    renderChannelAreaForUser("me-123");

    await userEvent.click(
      await screen.findByRole("button", { name: "Abrir conversa com Fernanda" }),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent("abrir a conversa");
    expect(screen.getByText("mensagem atual")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Abrir conversa com Fernanda" })).toBeEnabled();
  });

  it("renders another sender name as a DM action in group DMs", async () => {
    mockFetchDMMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda" })]),
    );

    render(
      <MemoryRouter initialEntries={["/chat/dm/group-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [],
                  dms: [{ id: "group-1", type: "group", name: "Grupo", participants: [] }],
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(
      await screen.findByRole("button", { name: "Abrir conversa com Fernanda" }),
    ).toBeInTheDocument();
  });

  it("does not add the issue #445 author action inside an open 1:1 DM", async () => {
    mockFetchDMMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda" })]),
    );

    render(
      <MemoryRouter initialEntries={["/chat/dm/direct-1"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "me-123",
                  channels: [],
                  dms: [
                    {
                      id: "direct-1",
                      type: "1:1",
                      name: "Fernanda",
                      participants: [],
                      counterpart: { userId: "other-456", displayName: "Fernanda" },
                    },
                  ],
                }}
              />
            }
          >
            <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByTestId("chat-msg-sender")).toHaveTextContent("Fernanda");
    expect(
      screen.queryByRole("button", { name: "Abrir conversa com Fernanda" }),
    ).not.toBeInTheDocument();
  });

  it("own message does not show sender span", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ senderId: "me-123", senderDisplayName: "Me" })]),
    );

    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat/channel"
            element={<ParentWithContext ctx={{ currentUserId: "me-123", channels: [], dms: [] }} />}
          >
            <Route path=":id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    await screen.findByTestId("chat-msg-bubble");
    expect(screen.queryByTestId("chat-msg-sender")).not.toBeInTheDocument();
  });
});

// ── Header and composer use resolved display name ─────────────────────────────

describe("ChatMessageArea — resolved display name", () => {
  it("channel header shows display_name from context instead of raw UUID", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    render(
      <MemoryRouter initialEntries={["/chat/channel/ch-uuid-001"]}>
        <Routes>
          <Route
            path="/chat/channel"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "",
                  channels: [{ id: "ch-uuid-001", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                }}
              />
            }
          >
            <Route path=":id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const header = await screen.findByTestId("chat-msg-header");
    expect(header).toHaveTextContent("geral");
    expect(header).not.toHaveTextContent("ch-uuid-001");
  });

  it("composer placeholder uses resolved channel display_name", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);

    render(
      <MemoryRouter initialEntries={["/chat/channel/ch-uuid-001"]}>
        <Routes>
          <Route
            path="/chat/channel"
            element={
              <ParentWithContext
                ctx={{
                  currentUserId: "",
                  channels: [{ id: "ch-uuid-001", name: "geral", type: "public", canWrite: true }],
                  dms: [],
                }}
              />
            }
          >
            <Route path=":id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    const input = await screen.findByTestId("chat-composer-input");
    expect(input).toHaveAttribute("aria-label", "Mensagem para #geral…");
  });

  it("falls back to raw targetId when channel not found in context", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    renderChannelArea("geral");

    const header = await screen.findByTestId("chat-msg-header");
    expect(header).toHaveTextContent("geral");
  });
});

// ── WS realtime scroll behavior ───────────────────────────────────────────────
//
// ws_append (from WS message.created events) must NOT call scrollIntoView when
// the user is reading history (not near the bottom), but SHOULD call it when
// the user is already near the bottom.
//
// useChatWebSocket is overridden per-test to capture the onMessageCreated
// callback — this is NOT a no-op mock; it exercises the real callback path.

describe("ChatMessageArea — WS message scroll behavior", () => {
  let capturedOnMessageCreated: ((evt: WSMessageCreatedEvent) => void) | null = null;

  beforeEach(() => {
    capturedOnMessageCreated = null;
    // Override to capture the onMessageCreated callback instead of dropping it.
    vi.mocked(useChatWebSocket).mockImplementation(
      ({
        onMessageCreated,
      }: {
        kind: string;
        targetId: string;
        onMessageCreated: (evt: WSMessageCreatedEvent) => void;
      }) => {
        capturedOnMessageCreated = onMessageCreated;
        return {
          toggleReaction: wsMockState.toggleReaction,
          sendTyping: wsMockState.sendTyping,
          connectionStatus: "connected",
        };
      },
    );
  });

  afterEach(() => {
    // Restore to the default no-op so other test suites are not affected.
    vi.mocked(useChatWebSocket).mockImplementation(() => ({
      toggleReaction: wsMockState.toggleReaction,
      sendTyping: wsMockState.sendTyping,
      connectionStatus: "connected",
    }));
  });

  it("does NOT call scrollIntoView when user is reading history (not near bottom)", async () => {
    // Provide an initial message so MessageList renders (it only renders when
    // messages.length > 0, which exposes the role="log" list element).
    const initialMsg = makeMessage({ id: "msg-initial", bodyText: "Initial message" });
    const wsMsg = makeMessage({ id: "msg-ws-hist", bodyText: "WS history message" });
    mockFetchChannelMessages.mockResolvedValue({ messages: [initialMsg], nextCursor: "" });
    vi.mocked(chatApi.fetchChannelMessage).mockResolvedValue(wsMsg);

    renderChannelArea("geral");
    // Wait for MessageList to render with the initial message.
    await waitFor(() => expect(screen.getByText("Initial message")).toBeInTheDocument());

    // Get the scrollable list element.
    const list = screen.getByRole("log");

    // Simulate user scrolled far up: scrollHeight=1000, clientHeight=400, scrollTop=0
    // → distance from bottom = 1000 - 0 - 400 = 600 > 150 → not near bottom.
    settleListLayout(list, 1000, 400);
    userScrollTo(list, 0);

    // Clear any scrollIntoView calls from the initial load.
    const scrollMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    scrollMock.mockClear();

    // Simulate a WS message.created event.
    expect(capturedOnMessageCreated).not.toBeNull();
    await act(async () => {
      capturedOnMessageCreated?.({
        type: "message.created",
        event_id: "evt-1",
        created_at: new Date().toISOString(),
        workspace_id: "ws-1",
        target_type: "channel",
        target_id: "geral",
        message_id: "msg-ws-hist",
      });
    });

    // Message appears in the list.
    await waitFor(() => expect(screen.getByText("WS history message")).toBeInTheDocument());

    // scrollIntoView must NOT be called — user is reading history.
    expect(scrollMock).not.toHaveBeenCalled();
  });

  it("calls scrollIntoView when user is near bottom", async () => {
    const initialMsg = makeMessage({ id: "msg-initial-bot", bodyText: "Initial bot message" });
    const wsMsg = makeMessage({ id: "msg-ws-bot", bodyText: "Near bottom message" });
    mockFetchChannelMessages.mockResolvedValue({ messages: [initialMsg], nextCursor: "" });
    vi.mocked(chatApi.fetchChannelMessage).mockResolvedValue(wsMsg);

    renderChannelArea("geral");
    await waitFor(() => expect(screen.getByText("Initial bot message")).toBeInTheDocument());

    const list = screen.getByRole("log");
    // Simulate near-bottom scroll: scrollHeight=500, clientHeight=400, scrollTop=99
    // → distance from bottom = 500 - 99 - 400 = 1 ≤ 150 → near bottom.
    settleListLayout(list, 500, 400);
    userScrollTo(list, 99);

    const scrollMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    scrollMock.mockClear();

    expect(capturedOnMessageCreated).not.toBeNull();
    await act(async () => {
      capturedOnMessageCreated?.({
        type: "message.created",
        event_id: "evt-1",
        created_at: new Date().toISOString(),
        workspace_id: "ws-1",
        target_type: "channel",
        target_id: "geral",
        message_id: "msg-ws-bot",
      });
    });

    // Message appears.
    await waitFor(() => expect(screen.getByText("Near bottom message")).toBeInTheDocument());

    // scrollIntoView SHOULD be called — user is near the bottom.
    await waitFor(() => expect(scrollMock).toHaveBeenCalledTimes(1));
  });
});

// ── #492: scroll navigation & read-state separation ──────────────────────────
//
// bottomSentinelCallback() locates the IntersectionObserver instance observing
// the bottom sentinel (data-testid="chat-bottom-sentinel") specifically, so
// these tests never depend on construction order relative to the top
// (loadMore) sentinel, which infinite-scroll's own MockIO already owns.

describe("ChatMessageArea — #492 scroll navigation & read-state", () => {
  interface IOInstance {
    element: Element;
    callback: IntersectionObserverCallback;
  }
  let ioInstances: IOInstance[] = [];
  let capturedOnMessageCreatedForBadge: ((evt: WSMessageCreatedEvent) => void) | null = null;

  beforeEach(() => {
    ioInstances = [];
    capturedOnMessageCreatedForBadge = null;
    class MultiMockIO {
      #callback: IntersectionObserverCallback;
      constructor(cb: IntersectionObserverCallback) {
        this.#callback = cb;
      }
      observe = vi.fn((el: Element) => {
        ioInstances.push({ element: el, callback: this.#callback });
      });
      disconnect = vi.fn();
      unobserve = vi.fn();
    }
    vi.stubGlobal("IntersectionObserver", MultiMockIO);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function bottomSentinelCallback(): IntersectionObserverCallback {
    const sentinel = screen.getByTestId("chat-bottom-sentinel");
    const instance = ioInstances.find((i) => i.element === sentinel);
    if (!instance) throw new Error("bottom sentinel IntersectionObserver not registered yet");
    return instance.callback;
  }

  function fireBottomSentinel(isIntersecting: boolean) {
    act(() => {
      bottomSentinelCallback()([{ isIntersecting } as IntersectionObserverEntry], {
        disconnect: vi.fn(),
      } as unknown as IntersectionObserver);
    });
  }

  function scrollAwayFromBottom(list: HTMLElement) {
    settleListLayout(list, 1000, 400);
    userScrollTo(list, 0);
  }

  function scrollBackToBottom(list: HTMLElement) {
    settleListLayout(list, 1000, 400);
    userScrollTo(list, 900);
  }

  function renderWithContext(
    channelId: string,
    ctx: Partial<ChatOutletContext> & { currentUserId: string },
  ) {
    return render(
      <MemoryRouter initialEntries={[`/chat/channel/${channelId}`]}>
        <Routes>
          <Route
            path="/chat"
            element={<ParentWithContext ctx={{ channels: [], dms: [], ...ctx }} />}
          >
            <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  }

  it("opens directly at the bottom, without smooth scroll, when there is no unread", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", bodyText: "Última mensagem" })]),
    );

    renderWithContext("geral", {
      currentUserId: "me-123",
      channels: [{ id: "geral", name: "Geral", type: "public", canWrite: true, unreadCount: 0 }],
    });

    await waitFor(() => expect(screen.getByText("Última mensagem")).toBeInTheDocument());

    const scrollMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    expect(scrollMock).toHaveBeenCalledWith({ behavior: "auto" });
    expect(scrollMock).not.toHaveBeenCalledWith(expect.objectContaining({ behavior: "smooth" }));
  });

  it("opens directly at the first unread message with a 'Novas mensagens' separator when unreadCount > 0", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({ id: "m1", senderId: "other-1", bodyText: "Lida 1" }),
        makeMessage({ id: "m2", senderId: "other-1", bodyText: "Não lida 1" }),
        makeMessage({ id: "m3", senderId: "other-1", bodyText: "Não lida 2" }),
      ]),
    );

    renderWithContext("geral", {
      currentUserId: "me-123",
      channels: [{ id: "geral", name: "Geral", type: "public", canWrite: true, unreadCount: 2 }],
    });

    await waitFor(() => expect(screen.getByText("Não lida 1")).toBeInTheDocument());

    const separator = screen.getByRole("separator", { name: "Novas mensagens" });
    expect(separator).toBeInTheDocument();
    // The separator sits immediately before the first unread message ("Não
    // lida 1", the earliest of the last 2 eligible messages) — not before the
    // already-read one.
    const listContent = screen.getByRole("log").querySelector(".chat-msg-area__list-content")!;
    const listItems = Array.from(listContent.children);
    const separatorIndex = listItems.indexOf(separator);
    const firstUnreadIndex = listItems.findIndex((el) => el.textContent?.includes("Não lida 1"));
    expect(separatorIndex).toBeGreaterThan(-1);
    expect(separatorIndex).toBeLessThan(firstUnreadIndex);

    const scrollMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    expect(scrollMock).not.toHaveBeenCalledWith(expect.objectContaining({ behavior: "smooth" }));
  });

  it("does not call mark-read just from opening a conversation with unread", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({ id: "m1", senderId: "other-1", bodyText: "Não lida 1" }),
        makeMessage({ id: "m2", senderId: "other-1", bodyText: "Não lida 2" }),
      ]),
    );
    const markRead = vi.fn();

    renderWithContext("geral", {
      currentUserId: "me-123",
      channels: [{ id: "geral", name: "Geral", type: "public", canWrite: true, unreadCount: 2 }],
      markRead,
    });

    await screen.findByText("Não lida 1");
    expect(markRead).not.toHaveBeenCalled();
  });

  it("calls markRead once the bottom sentinel confirms the real tail was reached", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", senderId: "other-1", bodyText: "Última" })]),
    );
    const markRead = vi.fn();

    renderWithContext("geral", {
      currentUserId: "me-123",
      channels: [{ id: "geral", name: "Geral", type: "public", canWrite: true, unreadCount: 1 }],
      markRead,
    });

    await screen.findByText("Última");
    expect(markRead).not.toHaveBeenCalled();

    fireBottomSentinel(true);

    expect(markRead).toHaveBeenCalledWith({ kind: "channel", targetId: "geral" });
  });

  it("shows the go-to-bottom button once the user scrolls away from the bottom threshold", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", bodyText: "Msg" })]),
    );
    renderWithContext("geral", { currentUserId: "me-123" });
    await screen.findByText("Msg");

    expect(
      screen.queryByRole("button", { name: "Ir para o final da conversa" }),
    ).not.toBeInTheDocument();

    scrollAwayFromBottom(screen.getByRole("log"));

    expect(
      await screen.findByRole("button", { name: "Ir para o final da conversa" }),
    ).toBeInTheDocument();
  });

  it("hides the go-to-bottom button once the user manually scrolls back within the threshold", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", bodyText: "Msg" })]),
    );
    renderWithContext("geral", { currentUserId: "me-123" });
    const list = await screen.findByRole("log");
    await screen.findByText("Msg");

    scrollAwayFromBottom(list);
    await screen.findByRole("button", { name: "Ir para o final da conversa" });

    scrollBackToBottom(list);

    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Ir para o final da conversa" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("keeps the go-to-bottom button visible after restoring a history position left on a previous visit", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({ id: "m1", bodyText: "Antiga" }),
        makeMessage({ id: "m2", bodyText: "Mais antiga ainda" }),
      ]),
    );

    const { unmount } = renderWithContext("geral", { currentUserId: "me-123" });
    const list = await screen.findByRole("log");
    await screen.findByText("Antiga");
    scrollAwayFromBottom(list);
    await screen.findByRole("button", { name: "Ir para o final da conversa" });
    unmount();

    renderWithContext("geral", { currentUserId: "me-123" });
    await screen.findByText("Antiga");

    // Returning to a conversation left mid-history must not silently default
    // to the bottom — the button stays visible because the restored phase is
    // not AT_BOTTOM.
    expect(
      await screen.findByRole("button", { name: "Ir para o final da conversa" }),
    ).toBeInTheDocument();
  });

  it("does not end SCROLLING_TO_BOTTOM (hide the button) until the bottom sentinel actually confirms arrival", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", bodyText: "Msg" })]),
    );
    renderWithContext("geral", { currentUserId: "me-123" });
    const list = await screen.findByRole("log");
    await screen.findByText("Msg");

    scrollAwayFromBottom(list);
    const button = await screen.findByRole("button", { name: "Ir para o final da conversa" });
    await userEvent.click(button);

    // A layout shift (e.g. an image finishing its load) can bounce the
    // sentinel through a non-intersecting state before the real arrival —
    // the button must survive that instead of disappearing early.
    fireBottomSentinel(false);
    expect(screen.getByRole("button", { name: "Ir para o final da conversa" })).toBeInTheDocument();

    fireBottomSentinel(true);
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Ir para o final da conversa" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("uses instant positioning, never smooth, when prefers-reduced-motion is set", async () => {
    const matchMediaMock = vi.fn().mockReturnValue({
      matches: true,
      media: "(prefers-reduced-motion: reduce)",
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal("matchMedia", matchMediaMock);

    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", bodyText: "Msg" })]),
    );
    renderWithContext("geral", { currentUserId: "me-123" });
    const list = await screen.findByRole("log");
    await screen.findByText("Msg");

    scrollAwayFromBottom(list);
    const button = await screen.findByRole("button", { name: "Ir para o final da conversa" });

    const scrollMock = window.Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>;
    scrollMock.mockClear();
    await userEvent.click(button);

    expect(scrollMock).toHaveBeenCalledWith({ behavior: "auto" });
    expect(scrollMock).not.toHaveBeenCalledWith(expect.objectContaining({ behavior: "smooth" }));
  });

  it("names the button with the pending count once new messages arrive while reading history", async () => {
    const initialMsg = makeMessage({ id: "m1", bodyText: "Msg" });
    const wsMsg = makeMessage({ id: "m2", senderId: "other-1", bodyText: "Nova enquanto lia" });
    mockFetchChannelMessages.mockResolvedValue(messagePage([initialMsg]));
    vi.mocked(chatApi.fetchChannelMessage).mockResolvedValue(wsMsg);
    vi.mocked(useChatWebSocket).mockImplementation(
      ({ onMessageCreated }: { onMessageCreated: (evt: WSMessageCreatedEvent) => void }) => {
        capturedOnMessageCreatedForBadge = onMessageCreated;
        return {
          toggleReaction: wsMockState.toggleReaction,
          sendTyping: wsMockState.sendTyping,
          connectionStatus: "connected",
        };
      },
    );

    renderWithContext("geral", { currentUserId: "me-123" });
    const list = await screen.findByRole("log");
    await screen.findByText("Msg");
    scrollAwayFromBottom(list);
    await screen.findByRole("button", { name: "Ir para o final da conversa" });

    await act(async () => {
      capturedOnMessageCreatedForBadge?.({
        type: "message.created",
        event_id: "evt-1",
        created_at: new Date().toISOString(),
        workspace_id: "ws-1",
        target_type: "channel",
        target_id: "geral",
        message_id: "m2",
      });
    });

    await waitFor(() => expect(screen.getByText("Nova enquanto lia")).toBeInTheDocument());
    expect(
      await screen.findByRole("button", { name: "Ir para o final da conversa, 1 novas mensagens" }),
    ).toBeInTheDocument();
  });

  it("falls back safely when the saved anchor message no longer exists", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", bodyText: "Única mensagem" })]),
    );

    // Simulate a stale sessionStorage anchor pointing at a message that is no
    // longer part of the loaded window (deleted, or from before retention).
    sessionStorage.setItem(
      "nchat.chat.viewport.v1:me-123:channel:geral",
      JSON.stringify({
        atBottom: false,
        anchorMessageId: "long-gone",
        anchorOffsetPx: 40,
        savedAt: Date.now(),
      }),
    );

    renderWithContext("geral", { currentUserId: "me-123" });

    // Must not hang or crash — falls back to a defined state (bottom, since
    // there is no unread either) instead of an infinite search.
    await screen.findByText("Única mensagem");
    expect(screen.getByTestId("chat-message-area")).toBeInTheDocument();
  });

  it("ignores a stale resolution from a conversation left mid-search after a rapid switch", async () => {
    mockFetchChannelMessages.mockImplementation((channelId: string) => {
      if (channelId === "slow") {
        return new Promise((resolve) =>
          setTimeout(
            () => resolve(messagePage([makeMessage({ id: "slow-1", bodyText: "Lenta" })])),
            50,
          ),
        );
      }
      return Promise.resolve(messagePage([makeMessage({ id: "fast-1", bodyText: "Rápida" })]));
    });

    function SwitchButton() {
      const navigate = useNavigate();
      return (
        <button type="button" onClick={() => navigate("/chat/channel/fast")}>
          trocar
        </button>
      );
    }

    render(
      <MemoryRouter initialEntries={["/chat/channel/slow"]}>
        <Routes>
          <Route
            path="/chat"
            element={<ParentWithContext ctx={{ currentUserId: "me-123", channels: [], dms: [] }} />}
          >
            <Route
              path="channel/:id"
              element={
                <div>
                  <SwitchButton />
                  <ChatMessageArea kind="channel" />
                </div>
              }
            />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    // Switch away before the slow fetch resolves — must not crash when it
    // eventually does, and must never render the stale conversation's data.
    await new Promise((resolve) => setTimeout(resolve, 10));
    await userEvent.click(screen.getByRole("button", { name: "trocar" }));
    await screen.findByText("Rápida");

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 60));
    });

    expect(screen.queryByText("Lenta")).not.toBeInTheDocument();
  });

  // #788: an async reflow (an attachment/document/media preview finishing its
  // layout well after the initial positioning) must not strand the viewport
  // away from the real bottom, nor invent a false reading-history position —
  // but it also must never override a genuine reading-history position. The
  // regression is reproduced generically via the content wrapper's own
  // ResizeObserver (flushResizeObservers, from setupTests) — never coupled to
  // any specific attachment component.
  describe("tail-lock across async reflow (#788)", () => {
    const anchorKey = "nchat.chat.viewport.v1:me-123:channel:geral";

    it("re-pins to the real bottom when content grows asynchronously after opening at the bottom", async () => {
      sessionStorage.removeItem(anchorKey);
      mockFetchChannelMessages.mockResolvedValue(
        messagePage([makeMessage({ id: "m1", bodyText: "Última mensagem" })]),
      );
      renderWithContext("geral", {
        currentUserId: "me-123",
        channels: [{ id: "geral", name: "Geral", type: "public", canWrite: true, unreadCount: 0 }],
      });
      const list = await screen.findByRole("log");
      await screen.findByText("Última mensagem");

      // Content grew (scrollHeight increased) but scrollTop is stale — the
      // geometry an attachment finishing its layout leaves behind.
      Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1000 });
      Object.defineProperty(list, "clientHeight", { configurable: true, value: 400 });
      Object.defineProperty(list, "scrollTop", { configurable: true, writable: true, value: 400 });

      act(() => flushResizeObservers());

      expect(list.scrollTop).toBe(600);
      expect(
        screen.queryByRole("button", { name: "Ir para o final da conversa" }),
      ).not.toBeInTheDocument();
    });

    it("does not pull the viewport back when the user scrolled up but stayed inside the near-bottom threshold", async () => {
      // The phase alone cannot gate the tail-lock: #492's scroll handler
      // assigns AT_BOTTOM anywhere within BOTTOM_THRESHOLD_PX (150) of the
      // end, so a deliberate small scroll up leaves the phase AT_BOTTOM while
      // the viewport is no longer at the real tail. A reflow must respect
      // that intent instead of yanking the reader down.
      sessionStorage.removeItem(anchorKey);
      mockFetchChannelMessages.mockResolvedValue(
        messagePage([makeMessage({ id: "m1", bodyText: "Msg" })]),
      );
      renderWithContext("geral", { currentUserId: "me-123" });
      const list = await screen.findByRole("log");
      await screen.findByText("Msg");

      // Real tail is scrollTop 600; the user scrolls up 100px — still within
      // the 150px near-bottom threshold, so the phase stays AT_BOTTOM.
      settleListLayout(list, 1000, 400);
      userScrollTo(list, 500);
      expect(
        screen.queryByRole("button", { name: "Ir para o final da conversa" }),
      ).not.toBeInTheDocument();

      // Historical content grows underneath them.
      Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1600 });
      act(() => flushResizeObservers());

      expect(list.scrollTop).toBe(500);
    });

    it("keeps following the tail when a scroll event carries a scrollHeight an async reflow already moved", async () => {
      // #788 root cause, in miniature. The tail-lock corrects a reflow with a
      // direct scrollTop write; the scroll event that write produces is
      // delivered a frame later, and a second reflow can land in between. The
      // handler then sees a distance-from-the-tail that belongs to the new
      // layout, not to anything the reader did. Treating it as intent is what
      // disarmed the tail-lock permanently — after that, every later reflow
      // went uncorrected (measured in DEV: 2051px from the tail).
      sessionStorage.removeItem(anchorKey);
      mockFetchChannelMessages.mockResolvedValue(
        messagePage([makeMessage({ id: "m1", bodyText: "Msg" })]),
      );
      renderWithContext("geral", { currentUserId: "me-123" });
      const list = await screen.findByRole("log");
      await screen.findByText("Msg");

      // Settled, and genuinely at the real tail.
      settleListLayout(list, 1000, 400);
      userScrollTo(list, 600);

      // The timeline grew before this scroll event was delivered.
      Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1600 });
      fireEvent.scroll(list);

      // A layout shift alone never becomes READING_HISTORY...
      expect(
        screen.queryByRole("button", { name: "Ir para o final da conversa" }),
      ).not.toBeInTheDocument();
      // ...and the tail-lock is still armed for the reflow that follows.
      act(() => flushResizeObservers());
      expect(list.scrollTop).toBe(1200);
    });

    it("stops following the tail on the reader's first real scroll once the layout has stabilised", async () => {
      // #788: the scrollHeight gate must not be a one-way street. It exists to
      // stop a reflow from being mistaken for intent — but the moment the
      // layout settles, an ordinary scroll has to count again, or the reader
      // would be pinned to the tail forever after any attachment loads.
      //
      // The whole sequence in one test, because it is the composition that
      // matters: ignoring the contaminated event (covered above) and honouring
      // a later real one (covered elsewhere) are each true in isolation while
      // the gate could still be stuck between them.
      sessionStorage.removeItem(anchorKey);
      mockFetchChannelMessages.mockResolvedValue(
        messagePage([makeMessage({ id: "m1", bodyText: "Msg" })]),
      );
      renderWithContext("geral", { currentUserId: "me-123" });
      const list = await screen.findByRole("log");
      await screen.findByText("Msg");

      // Following the tail, settled at the real bottom.
      settleListLayout(list, 1000, 400);
      userScrollTo(list, 600);

      // A reflow, and a scroll event delivered against the grown scrollHeight:
      // ignored as intent, so the tail-lock re-pins to the new tail.
      Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1600 });
      fireEvent.scroll(list);
      act(() => flushResizeObservers());
      expect(list.scrollTop).toBe(1200);

      // The pin's own scroll event, now with a stable scrollHeight: still the
      // real tail, so the intent is simply re-affirmed.
      fireEvent.scroll(list);

      // The layout has stabilised and the reader scrolls up ~100px — an
      // ordinary event, stable scrollHeight. It stays inside the 150px
      // courtesy threshold, so the phase remains AT_BOTTOM and no button
      // appears: the phase alone could never express what just happened.
      userScrollTo(list, 1100);
      expect(
        screen.queryByRole("button", { name: "Ir para o final da conversa" }),
      ).not.toBeInTheDocument();

      // Another reflow must respect that: no pull back to the tail.
      Object.defineProperty(list, "scrollHeight", { configurable: true, value: 2200 });
      act(() => flushResizeObservers());
      expect(list.scrollTop).toBe(1100);
    });

    it("does not pull the viewport back to the bottom when the user is reading history and content resizes", async () => {
      sessionStorage.removeItem(anchorKey);
      mockFetchChannelMessages.mockResolvedValue(
        messagePage([makeMessage({ id: "m1", bodyText: "Msg" })]),
      );
      renderWithContext("geral", { currentUserId: "me-123" });
      const list = await screen.findByRole("log");
      await screen.findByText("Msg");

      scrollAwayFromBottom(list);
      await screen.findByRole("button", { name: "Ir para o final da conversa" });
      const scrollTopBefore = list.scrollTop;

      Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1400 });
      act(() => flushResizeObservers());

      expect(list.scrollTop).toBe(scrollTopBefore);
      expect(
        screen.getByRole("button", { name: "Ir para o final da conversa" }),
      ).toBeInTheDocument();
    });

    it("does not pull a restored reading-history position back to the bottom when content resizes", async () => {
      sessionStorage.setItem(
        anchorKey,
        JSON.stringify({
          atBottom: false,
          anchorMessageId: "m1",
          anchorOffsetPx: 0,
          savedAt: Date.now(),
        }),
      );
      mockFetchChannelMessages.mockResolvedValue(
        messagePage([
          makeMessage({ id: "m1", bodyText: "Antiga" }),
          makeMessage({ id: "m2", bodyText: "Mais recente" }),
        ]),
      );
      renderWithContext("geral", { currentUserId: "me-123" });
      const list = await screen.findByRole("log");
      await screen.findByText("Antiga");

      Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1000 });
      Object.defineProperty(list, "clientHeight", { configurable: true, value: 400 });
      Object.defineProperty(list, "scrollTop", { configurable: true, writable: true, value: 0 });

      act(() => flushResizeObservers());

      expect(list.scrollTop).toBe(0);
      expect(
        await screen.findByRole("button", { name: "Ir para o final da conversa" }),
      ).toBeInTheDocument();
    });

    it("re-pins to the real bottom during an in-flight 'Ir para o final' animation if content grows mid-flight", async () => {
      sessionStorage.removeItem(anchorKey);
      mockFetchChannelMessages.mockResolvedValue(
        messagePage([makeMessage({ id: "m1", bodyText: "Msg" })]),
      );
      renderWithContext("geral", { currentUserId: "me-123" });
      const list = await screen.findByRole("log");
      await screen.findByText("Msg");

      scrollAwayFromBottom(list); // scrollHeight=1000, clientHeight=400, scrollTop=0
      const button = await screen.findByRole("button", { name: "Ir para o final da conversa" });
      await userEvent.click(button); // phase -> SCROLLING_TO_BOTTOM

      // Content grows mid-animation — a direct scrollTop write, not a second
      // scrollIntoView, so it can't race the click's own in-flight animation.
      Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1600 });
      act(() => flushResizeObservers());

      expect(list.scrollTop).toBe(1200);
      // Still visible until the bottom sentinel actually confirms arrival —
      // unchanged #492 semantics (scrollTop alone is never "arrival").
      expect(
        screen.getByRole("button", { name: "Ir para o final da conversa" }),
      ).toBeInTheDocument();

      fireBottomSentinel(true);
      await waitFor(() =>
        expect(
          screen.queryByRole("button", { name: "Ir para o final da conversa" }),
        ).not.toBeInTheDocument(),
      );
    });

    it("never persists a false reading-history anchor after a reflow while at the bottom", async () => {
      sessionStorage.removeItem(anchorKey);
      mockFetchChannelMessages.mockResolvedValue(
        messagePage([makeMessage({ id: "m1", bodyText: "Última mensagem" })]),
      );
      const { unmount } = renderWithContext("geral", {
        currentUserId: "me-123",
        channels: [{ id: "geral", name: "Geral", type: "public", canWrite: true, unreadCount: 0 }],
      });
      await screen.findByText("Última mensagem");

      act(() => flushResizeObservers());
      unmount();

      const raw = sessionStorage.getItem(anchorKey);
      expect(raw).not.toBeNull();
      expect(JSON.parse(raw!)).toMatchObject({ atBottom: true, anchorMessageId: null });
    });
  });
});

// ── Favoritos (RF-06) ─────────────────────────────────────────────────────────

describe("ChatMessageArea — favoritar mensagem", () => {
  it("shows the favorite action in the hover menu and confirms via REST", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", isFavorited: false })]),
    );
    mockFavoriteMessage.mockResolvedValue(undefined);
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);

    const star = screen.getByRole("button", { name: "Favoritar mensagem" });
    expect(star).toHaveAttribute("aria-pressed", "false");
    await userEvent.click(star);

    expect(mockFavoriteMessage).toHaveBeenCalledWith("m1");
    fireEvent.mouseEnter(bubble);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Remover dos favoritos" })).toHaveAttribute(
        "aria-pressed",
        "true",
      ),
    );
  });

  it("unfavorites an already-favorited message", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", isFavorited: true })]),
    );
    mockUnfavoriteMessage.mockResolvedValue(undefined);
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);

    await userEvent.click(screen.getByRole("button", { name: "Remover dos favoritos" }));

    expect(mockUnfavoriteMessage).toHaveBeenCalledWith("m1");
    fireEvent.mouseEnter(bubble);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Favoritar mensagem" })).toHaveAttribute(
        "aria-pressed",
        "false",
      ),
    );
  });

  it("shows an error banner and keeps state when the favorite call fails", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ id: "m1", isFavorited: false })]),
    );
    mockFavoriteMessage.mockRejectedValue(new Error("boom"));
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);

    await userEvent.click(screen.getByRole("button", { name: "Favoritar mensagem" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/favorito/i);
    fireEvent.mouseEnter(bubble);
    expect(screen.getByRole("button", { name: "Favoritar mensagem" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });
});

describe("ChatMessageArea — fixar mensagem (RF-05)", () => {
  it("pins a channel message via REST and reloads pins", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchPins.mockResolvedValueOnce([]).mockResolvedValue([
      {
        message: makeMessage({ id: "m1", bodyText: "fixe-me" }),
        pinnedByUserId: "mod-1",
        pinnedAt: new Date().toISOString(),
      },
    ]);
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);

    const pinBtn = screen.getByRole("button", { name: "Fixar mensagem" });
    await userEvent.click(pinBtn);

    expect(mockPinMessage).toHaveBeenCalledWith({ kind: "channel", id: "geral" }, "m1");
    // The pins bar appears once the reload resolves, showing the pinned message
    // itself — since issue #435 the bar carries only the most recent pin.
    expect(await screen.findByTestId("chat-pins")).toHaveTextContent("fixe-me");
  });

  it("shows only the most recent pin in the bar and unpins it", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchPins.mockResolvedValue([
      {
        message: makeMessage({ id: "m1", senderDisplayName: "Ana", bodyText: "antigo" }),
        pinnedByUserId: "u1",
        pinnedAt: "2026-07-15T10:00:00.000Z",
      },
      {
        message: makeMessage({ id: "m2", senderDisplayName: "Bruno", bodyText: "recente" }),
        pinnedByUserId: "u2",
        pinnedAt: "2026-07-15T12:00:00.000Z",
      },
    ]);
    renderChannelAreaForUser();

    const bar = await screen.findByTestId("chat-pins");
    expect(bar).toHaveTextContent("Bruno:");
    expect(bar).toHaveTextContent("recente");
    // The older pin is not in the bar at all — there is no expandable list.
    expect(bar).not.toHaveTextContent("antigo");

    await userEvent.click(within(bar).getByRole("button", { name: "Desafixar mensagem" }));

    expect(mockUnpinMessage).toHaveBeenCalledWith({ kind: "channel", id: "geral" }, "m2");
  });

  it("shows a defensive error when pin is rejected", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockPinMessage.mockRejectedValue(new Error("forbidden"));
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);

    await userEvent.click(screen.getByRole("button", { name: "Fixar mensagem" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/fixar/i);
  });

  it("pins a DM message and reloads DM pins", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchPins.mockResolvedValueOnce([]).mockResolvedValue([
      {
        message: makeMessage({ id: "m1" }),
        pinnedByUserId: "u1",
        pinnedAt: new Date().toISOString(),
      },
    ]);
    renderDMArea();
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);

    await userEvent.click(screen.getByRole("button", { name: "Fixar mensagem" }));

    expect(mockPinMessage).toHaveBeenCalledWith({ kind: "dm", id: "dm-juliane" }, "m1");
    expect(await screen.findByTestId("chat-pins")).toHaveTextContent("Olá, mundo!");
  });
});

describe("ChatMessageArea — edição e histórico (RF-13)", () => {
  const ownMessage = () =>
    makeMessage({
      id: "msg-edit",
      senderId: "me-123",
      bodyText: "Texto original",
      bodyFormat: "v2",
    });

  async function openEditor() {
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    await userEvent.click(screen.getByRole("button", { name: "Editar mensagem" }));
    return screen.findByTestId("chat-edit-input-msg-edit");
  }

  it("edits inline, saves the strict body version and shows the edited indicator", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    mockEditMessage.mockResolvedValue(
      makeMessage({
        ...ownMessage(),
        bodyText: "Texto atualizado",
        bodyFormat: "v2",
        isEdited: true,
        editCount: 1,
        editedAt: "2026-07-13T12:00:00Z",
      }),
    );
    renderChannelAreaForUser();

    const editor = await openEditor();
    expect(editor).toHaveTextContent("Texto original");
    await replaceEditorText(editor, "Texto atualizado");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    await waitFor(() =>
      expect(mockEditMessage).toHaveBeenCalledWith("msg-edit", "Texto atualizado", 2),
    );
    expect(await screen.findByText("Texto atualizado")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ver histórico de edições" })).toBeInTheDocument();
  });

  it("applies link safety from the editor response without a reload", async () => {
    const url = "https://example.test/editada";
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ ...ownMessage(), linkSafetyState: "safe" })]),
    );
    mockEditMessage.mockResolvedValue(
      makeMessage({
        ...ownMessage(),
        bodyText: url,
        bodyFormat: "v2",
        isEdited: true,
        editCount: 1,
        editedAt: "2026-08-18T12:00:00Z",
        linkSafetyState: "inconclusive",
      }),
    );
    renderChannelAreaForUser();

    const editor = await openEditor();
    await replaceEditorText(editor, url);
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    expect(await screen.findByTestId("chat-message-link-unverified")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: url })).toHaveAttribute("href", url);
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(1);
  });

  it("cancels an inline edit without calling the API or changing the message", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    renderChannelAreaForUser();

    const editor = await openEditor();
    await replaceEditorText(editor, "Mudança descartada");
    await userEvent.click(screen.getByRole("button", { name: "Cancelar" }));

    expect(screen.getByText("Texto original")).toBeInTheDocument();
    expect(screen.queryByText("Mudança descartada")).not.toBeInTheDocument();
    expect(mockEditMessage).not.toHaveBeenCalled();
  });

  it("preserves v3 as the edit format and cancels the inline editor with Escape", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([makeMessage({ ...ownMessage(), bodyText: "Texto v3", bodyFormat: "v3" })]),
    );
    renderChannelAreaForUser();

    const editor = await openEditor();
    await replaceEditorText(editor, "Mudança v3");
    fireEvent.keyDown(editor, { key: "Escape", code: "Escape" });

    expect(await screen.findByText("Texto v3")).toBeInTheDocument();
    expect(screen.queryByText("Mudança v3")).not.toBeInTheDocument();
    expect(mockEditMessage).not.toHaveBeenCalled();
  });

  it("shows the expired-window error and keeps the persisted content", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    mockEditMessage.mockRejectedValue(
      new chatApi.MessageEditError(409, "window_expired", "expired"),
    );
    renderChannelAreaForUser();

    const editor = await openEditor();
    await replaceEditorText(editor, "Não persistir");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Janela de edição expirada.");
    await userEvent.click(screen.getByRole("button", { name: "Cancelar" }));
    expect(screen.getByText("Texto original")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Ver histórico de edições" }),
    ).not.toBeInTheDocument();
  });

  it("shows rate-limit feedback and keeps the persisted content", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    mockEditMessage.mockRejectedValue(
      new chatApi.MessageEditError(429, "rate_limited", "rate limited"),
    );
    renderChannelAreaForUser();

    const editor = await openEditor();
    await replaceEditorText(editor, "Não persistir");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Aguarde antes de editar novamente.",
    );
    await userEvent.click(screen.getByRole("button", { name: "Cancelar" }));
    expect(screen.getByText("Texto original")).toBeInTheDocument();
  });

  it("shows not-found feedback when the edited message is no longer visible", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    mockEditMessage.mockRejectedValue(new chatApi.MessageEditError(404, "not_found", "missing"));
    renderChannelAreaForUser();

    const editor = await openEditor();
    await replaceEditorText(editor, "Não persistir");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Mensagem não encontrada.");
  });

  it("shows generic feedback when editing fails without a typed API error", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    mockEditMessage.mockRejectedValue(new TypeError("network unavailable"));
    renderChannelAreaForUser();

    const editor = await openEditor();
    await replaceEditorText(editor, "Não persistir");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível editar a mensagem.",
    );
  });

  it("hides editing and reloads after a 403 without retaining the optimistic body", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    mockEditMessage.mockRejectedValue(new chatApi.MessageEditError(403, "forbidden", "forbidden"));
    renderChannelAreaForUser();

    const editor = await openEditor();
    await replaceEditorText(editor, "Não autorizado");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    await waitFor(() => expect(mockFetchChannelMessages).toHaveBeenCalledTimes(2));
    const bubble = await screen.findByTestId("chat-msg-bubble");
    expect(bubble).toHaveTextContent("Texto original");
    fireEvent.mouseEnter(bubble);
    expect(screen.queryByRole("button", { name: "Editar mensagem" })).not.toBeInTheDocument();
  });

  it("reconciles a message.updated event without reloading the page", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    renderChannelAreaForUser();
    await screen.findByText("Texto original");
    await waitFor(() => expect(wsMockState.capturedWSMessageUpdated).not.toBeNull());

    await act(async () =>
      wsMockState.capturedWSMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "geral",
        message_update: {
          message_id: "msg-edit",
          channel_id: "geral",
          body: "Atualizada por WS",
          body_format: "v3",
          edited_at: "2026-07-13T12:00:00Z",
          edit_count: 3,
          is_edited: true,
        },
      }),
    );

    expect(await screen.findByText("Atualizada por WS")).toBeInTheDocument();
    expect(mockFetchChannelMessages).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Ver histórico de edições" })).toBeInTheDocument();
  });

  it("loads history lazily and renders versions through the safe rich-text renderer", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([{ ...ownMessage(), isEdited: true, editCount: 2 }]),
    );
    mockGetMessageHistory
      .mockResolvedValueOnce({
        entries: [
          {
            body: "**Versão segura** <img src=x onerror=alert(1)>",
            bodyFormat: 2,
            versionedAt: "2026-07-13T12:00:00Z",
          },
        ],
        nextCursor: "1",
      })
      .mockResolvedValueOnce({
        entries: [{ body: "Versão inicial", bodyFormat: 1, versionedAt: "2026-07-13T11:00:00Z" }],
      });
    renderChannelAreaForUser();

    expect(mockGetMessageHistory).not.toHaveBeenCalled();
    await userEvent.click(await screen.findByRole("button", { name: "Ver histórico de edições" }));

    const dialog = await screen.findByRole("dialog", { name: "Histórico de edições" });
    expect(dialog).toHaveTextContent("Versão segura");
    expect(dialog.querySelector("strong")).toHaveTextContent("Versão segura");
    expect(dialog.querySelector("img")).toBeNull();
    expect(mockGetMessageHistory).toHaveBeenCalledWith("msg-edit", {
      cursor: undefined,
      limit: 50,
    });
    await userEvent.click(screen.getByRole("button", { name: "Carregar mais" }));
    expect(await screen.findByText("Versão inicial")).toBeInTheDocument();
    expect(mockGetMessageHistory).toHaveBeenLastCalledWith("msg-edit", {
      cursor: "1",
      limit: 50,
    });
  });
});

describe("ChatMessageArea — exclusão com placeholder (RF-14)", () => {
  const ownMessage = () =>
    makeMessage({ id: "msg-delete", senderId: "me-123", bodyText: "Conteúdo privado" });

  async function revealDeleteAction() {
    const bubble = await screen.findByTestId("chat-msg-bubble");
    fireEvent.mouseEnter(bubble);
    return screen.findByRole("button", { name: "Excluir mensagem" });
  }

  it("confirms, prevents duplicate submission and replaces the message in place", async () => {
    const original = ownMessage();
    const deletedAt = "2026-07-14T12:00:00Z";
    let resolveDelete!: (message: Message) => void;
    mockFetchChannelMessages.mockResolvedValue(messagePage([original]));
    mockDeleteMessage.mockImplementation(() => new Promise((resolve) => (resolveDelete = resolve)));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderChannelAreaForUser();

    await userEvent.click(await revealDeleteAction());

    const loadingButton = screen.getByRole("button", { name: "Excluindo mensagem" });
    expect(loadingButton).toBeDisabled();
    expect(loadingButton).toHaveAttribute("aria-busy", "true");
    await userEvent.click(loadingButton);
    expect(mockDeleteMessage).toHaveBeenCalledTimes(1);

    act(() =>
      resolveDelete(
        makeMessage({
          ...original,
          bodyText: "",
          status: "deleted",
          isRemoved: true,
          deletedAt,
          updatedAt: deletedAt,
        }),
      ),
    );

    expect(await screen.findByText("Mensagem removida.")).toBeInTheDocument();
    expect(screen.queryByText("Conteúdo privado")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Editar mensagem" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Excluir mensagem/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Reagir/ })).not.toBeInTheDocument();
  });

  it("cancels confirmation without calling DELETE", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    vi.spyOn(window, "confirm").mockReturnValue(false);
    renderChannelAreaForUser();

    await userEvent.click(await revealDeleteAction());

    expect(mockDeleteMessage).not.toHaveBeenCalled();
    expect(screen.getByText("Conteúdo privado")).toBeInTheDocument();
  });

  it("keeps the original message and shows generic feedback on failure", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([ownMessage()]));
    mockDeleteMessage.mockRejectedValue(new Error("authorization detail"));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderChannelAreaForUser();

    await userEvent.click(await revealDeleteAction());

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível excluir a mensagem. Tente novamente.",
    );
    expect(screen.getByText("Conteúdo privado")).toBeInTheDocument();
  });

  it("does not offer deletion to another author or to a removed message", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({ id: "other", senderId: "other-user", bodyText: "de outro" }),
        makeMessage({
          id: "removed",
          senderId: "me-123",
          bodyText: "conteúdo residual",
          status: "deleted",
          isRemoved: true,
        }),
      ]),
    );
    renderChannelAreaForUser();

    const bubbles = await screen.findAllByTestId("chat-msg-bubble");
    for (const bubble of bubbles) fireEvent.mouseEnter(bubble);

    expect(screen.queryByRole("button", { name: "Excluir mensagem" })).not.toBeInTheDocument();
    expect(screen.getByText("Mensagem removida.")).toBeInTheDocument();
    expect(screen.queryByText("conteúdo residual")).not.toBeInTheDocument();
  });

  it("replaces a reply quote when the referenced message is deleted over WebSocket", async () => {
    const original = ownMessage();
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        original,
        makeMessage({
          id: "reply",
          bodyText: "resposta",
          quoted: {
            id: original.id,
            authorId: original.senderId,
            bodyText: original.bodyText,
            bodyFormat: "v1",
            isRemoved: false,
            deletedAt: null,
            createdAt: original.createdAt,
            linkSafetyState: "",
          },
        }),
      ]),
    );
    renderChannelAreaForUser();
    expect(await screen.findAllByText("Conteúdo privado")).toHaveLength(2);

    act(() =>
      wsMockState.capturedWSMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "geral",
        message_update: {
          message_id: original.id,
          channel_id: "geral",
          body: "",
          body_format: "v1",
          edited_at: "",
          edit_count: 0,
          is_edited: false,
          status: "deleted",
          is_removed: true,
          deleted_at: "2026-07-14T12:00:00Z",
        },
      }),
    );

    expect(await screen.findByText("Mensagem removida.")).toBeInTheDocument();
    expect(screen.getByText("Mensagem original indisponível.")).toBeInTheDocument();
    expect(screen.queryByText("Conteúdo privado")).not.toBeInTheDocument();
    expect(screen.getByText("resposta")).toBeInTheDocument();
  });
});

// ── Painel de detalhes do canal (issue #435) ─────────────────────────────────

/**
 * Renders the message area under a route that can switch channels without
 * remounting the component, which is exactly the condition the panel's
 * behaviour depends on: React Router updates the :id parameter in place, so
 * ChatMessageArea (and the panel state it owns) survives the navigation.
 */
function renderChannelSwitcher(currentUserId = "me-123") {
  function Switcher() {
    const navigate = useNavigate();
    return (
      <>
        <button type="button" onClick={() => navigate("/chat/channel/outro")}>
          ir para outro canal
        </button>
        <ParentWithContext ctx={{ currentUserId, channels: [], dms: [] }} />
      </>
    );
  }
  return render(
    <MemoryRouter initialEntries={["/chat/channel/geral"]}>
      <Routes>
        <Route path="/chat" element={<Switcher />}>
          <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

function detailsToggle() {
  return screen.getByRole("button", { name: "Detalhes do canal" });
}

/**
 * Renders the message area with a *mutable* channel list in the outlet context.
 *
 * The two gestures are real buttons, like renderChannelSwitcher's, so a test can
 * navigate or apply `afterRename` — exactly what the sidebar refetch and the
 * channel.updated event produce — without remounting ChatMessageArea or the
 * panel it owns.
 */
function renderChannelsWithMutableNames(
  initial: ChatOutletContext["channels"],
  afterRename: ChatOutletContext["channels"] = initial,
) {
  function Host() {
    const [channels, setChannels] = useState(initial);
    const navigate = useNavigate();
    return (
      <>
        <button type="button" onClick={() => navigate("/chat/channel/outro")}>
          ir para outro canal
        </button>
        <button type="button" onClick={() => setChannels(afterRename)}>
          aplicar renomeacao
        </button>
        <ParentWithContext ctx={{ currentUserId: "me-123", channels, dms: [] }} />
      </>
    );
  }
  render(
    <MemoryRouter initialEntries={["/chat/channel/geral"]}>
      <Routes>
        <Route path="/chat" element={<Host />}>
          <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

function detailsRequestsFor(channelId: string): number {
  return mockFetchChannelDetails.mock.calls.filter((call) => call[0] === channelId).length;
}

const namedChannel = (id: string, name: string): ChatOutletContext["channels"][number] => ({
  id,
  name,
  type: "public",
  canWrite: true,
});

// ── Detalhes e renomeação (issue #527) ───────────────────────────────────────
//
// The panel holds its own copy of display_name from GET /details, so a rename of
// the conversation it is showing has to make it refetch. What must NOT happen is
// that same refetch firing on navigation: useConversationDetails already loads
// the new target from its own kind/id effect, and a second request would abort
// and repeat it.

describe("ChatMessageArea — detalhes e renomeação do canal (#527)", () => {
  it("requests the new channel's details exactly once when switching channels", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelsWithMutableNames([
      namedChannel("geral", "Geral"),
      namedChannel("outro", "Outro"),
    ]);
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(detailsToggle());
    await screen.findByTestId("chat-conversation-details");
    expect(detailsRequestsFor("geral")).toBe(1);

    await userEvent.click(screen.getByRole("button", { name: "ir para outro canal" }));
    await waitFor(() => expect(detailsRequestsFor("outro")).toBeGreaterThan(0));

    expect(detailsRequestsFor("outro")).toBe(1);
    expect(detailsRequestsFor("geral")).toBe(1);
  });

  it("refetches the open panel exactly once when its own channel is renamed", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    // What the sidebar refetch produces after a rename, locally or from
    // channel.updated: the same id under a new name.
    renderChannelsWithMutableNames(
      [namedChannel("geral", "Geral")],
      [namedChannel("geral", "Plataforma")],
    );
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(detailsToggle());
    await screen.findByTestId("chat-conversation-details");
    expect(detailsRequestsFor("geral")).toBe(1);

    await userEvent.click(screen.getByRole("button", { name: "aplicar renomeacao" }));
    await waitFor(() => expect(detailsRequestsFor("geral")).toBe(2));

    // The header converges from the outlet context, and the route did not move.
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Plataforma");
    expect(detailsRequestsFor("geral")).toBe(2);
  });

  // A closed panel has no target and nothing to refresh: a rename must not be a
  // reason to fetch details nobody is looking at.
  it("does not request details at all when the panel is closed during a rename", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelsWithMutableNames(
      [namedChannel("geral", "Geral")],
      [namedChannel("geral", "Plataforma")],
    );
    await screen.findByTestId("chat-msg-bubble");
    expect(screen.queryByTestId("chat-conversation-details")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "aplicar renomeacao" }));

    // The header still converges — it reads the outlet context, not /details.
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Plataforma");
    expect(detailsRequestsFor("geral")).toBe(0);
    expect(mockFetchChannelDetails).not.toHaveBeenCalled();
  });

  it("does not refetch the open panel when a different channel is renamed", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelsWithMutableNames(
      [namedChannel("geral", "Geral"), namedChannel("outro", "Outro")],
      [namedChannel("geral", "Geral"), namedChannel("outro", "Plataforma")],
    );
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(detailsToggle());
    await screen.findByTestId("chat-conversation-details");
    expect(detailsRequestsFor("geral")).toBe(1);

    await userEvent.click(screen.getByRole("button", { name: "aplicar renomeacao" }));

    expect(detailsRequestsFor("geral")).toBe(1);
    expect(detailsRequestsFor("outro")).toBe(0);
  });
});

describe("ChatMessageArea — painel de detalhes do canal (#435)", () => {
  it("opens and closes from the header control, reflecting the state in aria-expanded", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    const toggle = detailsToggle();
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("chat-conversation-details")).not.toBeInTheDocument();

    await userEvent.click(toggle);
    expect(await screen.findByTestId("chat-conversation-details")).toBeInTheDocument();
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    // The control names the panel it opens.
    expect(toggle.getAttribute("aria-controls")).toBe(
      screen.getByTestId("chat-conversation-details").id,
    );

    await userEvent.click(toggle);
    expect(screen.queryByTestId("chat-conversation-details")).not.toBeInTheDocument();
    expect(toggle).toHaveAttribute("aria-expanded", "false");
  });

  it("returns focus to the header control when the panel closes itself", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(detailsToggle());
    await screen.findByTestId("chat-conversation-details");

    await userEvent.click(screen.getByRole("button", { name: "Fechar detalhes do canal" }));

    expect(detailsToggle()).toHaveFocus();
  });

  // Issue #467: below the wide-desktop threshold the panel covers the
  // conversation, and a keyboard user has to be able to get back out of it.
  // Wired once rather than by width, so the gesture is the same in both
  // compositions.
  it("closes on Escape and returns focus to the header control", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(detailsToggle());
    await screen.findByTestId("chat-conversation-details");

    await userEvent.keyboard("{Escape}");

    expect(screen.queryByTestId("chat-conversation-details")).not.toBeInTheDocument();
    expect(detailsToggle()).toHaveFocus();
  });

  it("is not offered in a DM, which has no channel details to show", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMArea();
    await screen.findByTestId("chat-msg-bubble");

    expect(screen.queryByRole("button", { name: "Detalhes do canal" })).not.toBeInTheDocument();
    expect(mockFetchChannelDetails).not.toHaveBeenCalled();
  });

  it("does not remount the conversation or lose a typed draft when it opens and closes", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    const editor = screen.getByTestId("chat-composer-input");
    await fillEditor(editor, "rascunho preservado");
    const listBefore = screen.getByRole("log", { name: "Mensagens" });
    const messageRequestsBefore = mockFetchChannelMessages.mock.calls.length;

    await userEvent.click(detailsToggle());
    await screen.findByTestId("chat-conversation-details");
    await userEvent.click(detailsToggle());

    // Same DOM node: the scroll container was reconciled in place, never
    // recreated, so the scroll position and the list state are intact.
    expect(screen.getByRole("log", { name: "Mensagens" })).toBe(listBefore);
    expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("rascunho preservado");
    // No refetch: useMessages was never restarted.
    expect(mockFetchChannelMessages.mock.calls.length).toBe(messageRequestsBefore);
  });

  it("stays open across a channel switch and reloads every section for the new channel", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchChannelAttachments.mockImplementation((target: { id: string }) =>
      Promise.resolve([
        {
          id: `a-${target.id}`,
          filename: `arquivo-${target.id}.pdf`,
          contentType: "application/pdf",
          size: 1024,
          status: "clean" as const,
          createdAt: "2026-07-15T12:00:00.000Z",
        },
      ]),
    );
    renderChannelSwitcher();
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(detailsToggle());
    expect(await screen.findByText("Membro de geral")).toBeInTheDocument();
    expect(await screen.findByText("arquivo-geral.pdf")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "ir para outro canal" }));

    // The panel is still open and now describes the channel the user switched to.
    expect(await screen.findByText("Membro de outro")).toBeInTheDocument();
    expect(await screen.findByText("arquivo-outro.pdf")).toBeInTheDocument();
    expect(screen.queryByText("Membro de geral")).not.toBeInTheDocument();
    expect(screen.queryByText("arquivo-geral.pdf")).not.toBeInTheDocument();
  });

  it("ignores a late answer for the channel the user has already left", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    let resolveFirst: (value: ReturnType<typeof channelDetailsFor>) => void = () => {};
    mockFetchChannelDetails
      .mockImplementationOnce(
        () =>
          new Promise<ReturnType<typeof channelDetailsFor>>((resolve) => {
            resolveFirst = resolve;
          }),
      )
      .mockImplementation((channelId: string) => Promise.resolve(channelDetailsFor(channelId)));

    renderChannelSwitcher();
    await screen.findByTestId("chat-msg-bubble");
    await userEvent.click(detailsToggle());

    await userEvent.click(screen.getByRole("button", { name: "ir para outro canal" }));
    expect(await screen.findByText("Membro de outro")).toBeInTheDocument();

    // The abandoned channel finally answers. Its members must not appear.
    await act(async () => {
      resolveFirst(channelDetailsFor("geral"));
    });

    expect(screen.getByText("Membro de outro")).toBeInTheDocument();
    expect(screen.queryByText("Membro de geral")).not.toBeInTheDocument();
  });

  it("keeps the channel section rendered when only the files request fails", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchChannelAttachments.mockRejectedValue(new Error("file-service indisponível"));
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(detailsToggle());

    expect(await screen.findByText("Não foi possível carregar os arquivos.")).toBeInTheDocument();
    expect(screen.getByText(/Canal público/)).toBeInTheDocument();
  });

  it("shows the same pinned message in the bar and in the panel", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchPins.mockResolvedValue([
      {
        message: makeMessage({ id: "m-old", senderDisplayName: "Ana", bodyText: "pin antigo" }),
        pinnedByUserId: "u1",
        pinnedAt: "2026-07-15T10:00:00.000Z",
      },
      {
        message: makeMessage({ id: "m-new", senderDisplayName: "Bruno", bodyText: "pin recente" }),
        pinnedByUserId: "u2",
        pinnedAt: "2026-07-15T12:00:00.000Z",
      },
    ]);
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(detailsToggle());

    const bar = await screen.findByTestId("chat-pins");
    const card = await screen.findByTestId("chat-details-pin");
    expect(bar).toHaveTextContent("pin recente");
    expect(card).toHaveTextContent("pin recente");
    expect(bar).not.toHaveTextContent("pin antigo");
    expect(card).not.toHaveTextContent("pin antigo");
  });

  it("updates the bar and the panel together after an unpin", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchPins.mockResolvedValueOnce([
      {
        message: makeMessage({ id: "m-new", senderDisplayName: "Bruno", bodyText: "pin recente" }),
        pinnedByUserId: "u2",
        pinnedAt: "2026-07-15T12:00:00.000Z",
      },
    ]);
    // The reload after the unpin returns the channel with nothing pinned.
    mockFetchPins.mockResolvedValue([]);
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");
    await userEvent.click(detailsToggle());
    await screen.findByTestId("chat-details-pin");

    await userEvent.click(
      within(screen.getByTestId("chat-pins")).getByRole("button", { name: "Desafixar mensagem" }),
    );

    expect(mockUnpinMessage).toHaveBeenCalledWith({ kind: "channel", id: "geral" }, "m-new");
    await waitFor(() => expect(screen.queryByTestId("chat-pins")).not.toBeInTheDocument());
    expect(screen.getByTestId("chat-details-pin-empty")).toBeInTheDocument();
  });

  it("requests only the selected channel and never a stale one", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(detailsToggle());
    await screen.findByTestId("chat-conversation-details");

    expect(mockFetchChannelDetails).toHaveBeenCalledWith("geral", expect.any(AbortSignal));
    expect(mockFetchChannelAttachments).toHaveBeenCalledWith(
      { kind: "channel", id: "geral" },
      expect.any(Number),
      expect.any(AbortSignal),
    );
  });
});

// ── Painel de detalhes do grupo (issue #441) ─────────────────────────────────

const groupConversationId = "grupo-infra";
const directConversationId = "dm-juliane";

/**
 * Renders a DM route whose outlet context carries the sidebar's conversation
 * records, so the component resolves the conversation type from the domain
 * discriminant (`direct` / `group`) exactly as it does in the app — never from
 * the URL or from the conversation's name.
 */
function renderDMWithContext(
  dmId: string,
  currentUserId = "me-123",
  // A legacy DM whose counterpart the sidebar could not resolve is a real
  // shape, so it is a fixture option rather than a separate render helper.
  { withCounterpart = true }: { withCounterpart?: boolean } = {},
) {
  return render(
    <MemoryRouter initialEntries={[`/chat/dm/${dmId}`]}>
      <Routes>
        <Route
          path="/chat"
          element={
            <ParentWithContext
              ctx={{
                currentUserId,
                channels: [],
                dms: [
                  {
                    id: groupConversationId,
                    type: "group",
                    name: "Time de Infra",
                    participants: [],
                  },
                  {
                    id: directConversationId,
                    type: "1:1",
                    name: "Juliane",
                    participants: [],
                    counterpart: withCounterpart
                      ? { userId: "juliane-1", displayName: "Juliane" }
                      : undefined,
                  },
                ],
              }}
            />
          }
        >
          <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

function groupToggle() {
  return screen.getByRole("button", { name: "Detalhes do grupo" });
}

describe("ChatMessageArea — painel de detalhes do grupo (#441)", () => {
  it("offers the details control in an ad-hoc group", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(groupConversationId);
    await screen.findByTestId("chat-msg-bubble");

    const toggle = groupToggle();
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    await userEvent.click(toggle);

    const panel = await screen.findByTestId("chat-conversation-details");
    expect(panel).toHaveAttribute("data-conversation-kind", "group");
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(toggle.getAttribute("aria-controls")).toBe(panel.id);
    expect(screen.getByRole("heading", { name: "Detalhes do grupo" })).toBeInTheDocument();
    // The group endpoint is used, never the channel one.
    expect(mockFetchGroupDetails).toHaveBeenCalledWith(
      groupConversationId,
      expect.any(AbortSignal),
    );
    expect(mockFetchChannelDetails).not.toHaveBeenCalled();
    expect(mockFetchChannelAttachments).toHaveBeenCalledWith(
      { kind: "dm", id: groupConversationId },
      expect.any(Number),
      expect.any(AbortSignal),
    );
  });

  it("does not lend the group's control or endpoint to a 1:1 DM", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(directConversationId);
    await screen.findByTestId("chat-msg-bubble");

    // A 1:1 has its own control and its own endpoint (issue #443); what it must
    // never do is borrow the group's vocabulary or read the group projection.
    expect(screen.queryByRole("button", { name: "Detalhes do grupo" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Detalhes do canal" })).not.toBeInTheDocument();
    expect(mockFetchGroupDetails).not.toHaveBeenCalled();
  });

  it("closes from the panel and returns focus to the header control", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(groupConversationId);
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(groupToggle());
    await screen.findByTestId("chat-conversation-details");

    await userEvent.click(screen.getByRole("button", { name: "Fechar detalhes do grupo" }));

    expect(screen.queryByTestId("chat-conversation-details")).not.toBeInTheDocument();
    expect(groupToggle()).toHaveFocus();
  });

  it("does not remount the conversation or lose a typed draft", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(groupConversationId);
    await screen.findByTestId("chat-msg-bubble");

    const editor = screen.getByTestId("chat-composer-input");
    await fillEditor(editor, "rascunho do grupo");
    const listBefore = screen.getByRole("log", { name: "Mensagens" });
    const messageRequestsBefore = mockFetchDMMessages.mock.calls.length;

    await userEvent.click(groupToggle());
    await screen.findByTestId("chat-conversation-details");
    await userEvent.click(groupToggle());

    expect(screen.getByRole("log", { name: "Mensagens" })).toBe(listBefore);
    expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("rascunho do grupo");
    expect(mockFetchDMMessages.mock.calls.length).toBe(messageRequestsBefore);
  });

  it("shows the same pinned message in the bar and in the panel", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchPins.mockResolvedValue([
      {
        message: makeMessage({ id: "m-old", senderDisplayName: "Ana", bodyText: "pin antigo" }),
        pinnedByUserId: "u1",
        pinnedAt: "2026-07-15T10:00:00.000Z",
      },
      {
        message: makeMessage({ id: "m-new", senderDisplayName: "Bruno", bodyText: "pin recente" }),
        pinnedByUserId: "u2",
        pinnedAt: "2026-07-15T12:00:00.000Z",
      },
    ]);
    renderDMWithContext(groupConversationId);
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(groupToggle());

    // One usePins instance, one selectLatestPin result, shared by both surfaces.
    const bar = await screen.findByTestId("chat-pins");
    const card = await screen.findByTestId("chat-details-pin");
    expect(bar).toHaveTextContent("pin recente");
    expect(card).toHaveTextContent("pin recente");
    expect(card).not.toHaveTextContent("pin antigo");
  });

  it("keeps the group section rendered when only the files request fails", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchChannelAttachments.mockRejectedValue(new Error("file-service indisponível"));
    renderDMWithContext(groupConversationId);
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(groupToggle());

    expect(await screen.findByText("Não foi possível carregar os arquivos.")).toBeInTheDocument();
    // The group's own metadata survives a file-service failure.
    expect(screen.getByTestId("chat-details-group-name")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Participantes do grupo" })).toBeInTheDocument();
  });
});

// ── Alternância entre canal e grupo (issue #441) ─────────────────────────────

/**
 * Renders a shell that can navigate between a channel and a group without
 * remounting ChatMessageArea, which is what makes "the panel stays open and its
 * content follows the conversation" observable.
 */
function renderCrossTypeSwitcher(currentUserId = "me-123") {
  function Switcher() {
    const navigate = useNavigate();
    return (
      <>
        <button type="button" onClick={() => navigate(`/chat/dm/${groupConversationId}`)}>
          ir para o grupo
        </button>
        <ParentWithContext
          ctx={{
            currentUserId,
            channels: [],
            dms: [
              { id: groupConversationId, type: "group", name: "Time de Infra", participants: [] },
            ],
          }}
        />
      </>
    );
  }
  return render(
    <MemoryRouter initialEntries={["/chat/channel/geral"]}>
      <Routes>
        <Route path="/chat" element={<Switcher />}>
          <Route path="channel/:id" element={<ChatMessageArea kind="channel" />} />
          <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("ChatMessageArea — alternância canal ↔ grupo (#441)", () => {
  it("switches vocabulary and data when moving from a channel to a group", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m2" })]));
    renderCrossTypeSwitcher();
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(screen.getByRole("button", { name: "Detalhes do canal" }));
    expect(await screen.findByText("Membro de geral")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Detalhes do canal" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "ir para o grupo" }));

    // The panel stays open and now describes the group, with no channel-only
    // wording left behind from the previous conversation.
    expect(await screen.findByRole("heading", { name: "Detalhes do grupo" })).toBeInTheDocument();
    expect(await screen.findByText(`Participante de ${groupConversationId}`)).toBeInTheDocument();
    expect(screen.queryByText("Membro de geral")).not.toBeInTheDocument();
    expect(screen.queryByText(/Canal público/)).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Membros online/ })).not.toBeInTheDocument();
  });

  it("ignores a late channel answer that resolves after the switch to a group", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m2" })]));
    let resolveChannel: (value: ReturnType<typeof channelDetailsFor>) => void = () => {};
    mockFetchChannelDetails.mockImplementationOnce(
      () =>
        new Promise<ReturnType<typeof channelDetailsFor>>((resolve) => {
          resolveChannel = resolve;
        }),
    );

    renderCrossTypeSwitcher();
    await screen.findByTestId("chat-msg-bubble");
    await userEvent.click(screen.getByRole("button", { name: "Detalhes do canal" }));

    await userEvent.click(screen.getByRole("button", { name: "ir para o grupo" }));
    expect(await screen.findByRole("heading", { name: "Detalhes do grupo" })).toBeInTheDocument();

    // The abandoned channel finally answers. Its members must not appear under
    // the group's heading.
    await act(async () => {
      resolveChannel(channelDetailsFor("geral"));
    });

    expect(screen.getByRole("heading", { name: "Detalhes do grupo" })).toBeInTheDocument();
    expect(screen.queryByText("Membro de geral")).not.toBeInTheDocument();
    expect(screen.queryByText(/Canal público/)).not.toBeInTheDocument();
  });
});

// ── Painel de perfil da DM 1:1 (issue #443) ──────────────────────────────────

function profileToggle() {
  return screen.getByRole("button", { name: "Abrir perfil de Juliane" });
}

describe("ChatMessageArea — painel de perfil da DM 1:1 (#443)", () => {
  it("offers a profile control named after the other person", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(directConversationId);
    await screen.findByTestId("chat-msg-bubble");

    const toggle = profileToggle();
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    await userEvent.click(toggle);

    const panel = await screen.findByTestId("chat-conversation-details");
    expect(panel).toHaveAttribute("data-conversation-kind", "direct");
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(toggle.getAttribute("aria-controls")).toBe(panel.id);
    expect(screen.getByRole("heading", { name: "Perfil" })).toBeInTheDocument();

    // The profile endpoint, named after the conversation and nothing else.
    expect(mockFetchDirectProfile).toHaveBeenCalledWith(
      directConversationId,
      expect.any(AbortSignal),
    );
    expect(mockFetchGroupDetails).not.toHaveBeenCalled();
    expect(mockFetchChannelDetails).not.toHaveBeenCalled();
    // A profile has no files section, so file-service is never asked.
    expect(mockFetchChannelAttachments).not.toHaveBeenCalled();
  });

  it("names the control after the conversation when no counterpart is known", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(directConversationId, "me-123", { withCounterpart: false });
    await screen.findByTestId("chat-msg-bubble");

    // A legacy DM whose counterpart the sidebar could not resolve still gets a
    // usable accessible name — never a blank one and never an ID.
    expect(screen.getByRole("button", { name: "Abrir perfil da conversa" })).toBeInTheDocument();
  });

  it("shows the profile of the other participant, not the viewer", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(directConversationId);
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(profileToggle());

    const name = await screen.findByTestId("chat-details-profile-name");
    expect(name).toHaveTextContent(`Perfil de ${directConversationId}`);
    // The panel renders what the server resolved; the viewer's own id never
    // appears as the subject of the card.
    expect(name).not.toHaveTextContent("me-123");
  });

  it("shows no group or channel section in a 1:1", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(directConversationId);
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(profileToggle());
    await screen.findByTestId("chat-details-profile-name");

    expect(screen.queryByRole("heading", { name: /Participantes/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Membros online/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Arquivos recentes" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-group-name")).not.toBeInTheDocument();
  });

  it("closes from the panel and returns focus to the header control", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(directConversationId);
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(profileToggle());
    await screen.findByTestId("chat-conversation-details");

    await userEvent.click(screen.getByRole("button", { name: "Fechar perfil" }));

    expect(screen.queryByTestId("chat-conversation-details")).not.toBeInTheDocument();
    expect(profileToggle()).toHaveFocus();
  });

  it("does not remount the conversation or lose a typed draft", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(directConversationId);
    await screen.findByTestId("chat-msg-bubble");

    const editor = screen.getByTestId("chat-composer-input");
    await fillEditor(editor, "rascunho da DM");
    const listBefore = screen.getByRole("log", { name: "Mensagens" });
    const messageRequestsBefore = mockFetchDMMessages.mock.calls.length;

    await userEvent.click(profileToggle());
    await screen.findByTestId("chat-conversation-details");
    await userEvent.click(profileToggle());

    // Opening the panel is a layout change, not a navigation: nothing here is
    // remounted, so the message list, the draft and the subscription survive.
    expect(screen.getByRole("log", { name: "Mensagens" })).toBe(listBefore);
    expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("rascunho da DM");
    expect(mockFetchDMMessages.mock.calls.length).toBe(messageRequestsBefore);
  });

  it("shows an error state when the profile is refused", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchDirectProfile.mockRejectedValue(new Error("404"));
    renderDMWithContext(directConversationId);
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(profileToggle());

    expect(await screen.findByText("Não foi possível carregar o perfil.")).toBeInTheDocument();
    // A refusal is never rendered as a person with no attributes.
    expect(screen.queryByTestId("chat-details-profile-meta")).not.toBeInTheDocument();
  });
});

const secondDirectConversationId = "dm-marcos";

/**
 * A shell that navigates between two 1:1 DMs in place, which is what makes
 * "the panel stayed open and its content followed the conversation" observable.
 */
function renderDirectSwitcher(currentUserId = "me-123") {
  function Switcher() {
    const navigate = useNavigate();
    return (
      <>
        <button type="button" onClick={() => navigate(`/chat/dm/${secondDirectConversationId}`)}>
          ir para a outra DM
        </button>
        <ParentWithContext
          ctx={{
            currentUserId,
            channels: [],
            dms: [
              {
                id: directConversationId,
                type: "1:1",
                name: "Juliane",
                participants: [],
                counterpart: { userId: "juliane-1", displayName: "Juliane" },
              },
              {
                id: secondDirectConversationId,
                type: "1:1",
                name: "Marcos",
                participants: [],
                counterpart: { userId: "marcos-1", displayName: "Marcos" },
              },
            ],
          }}
        />
      </>
    );
  }
  return render(
    <MemoryRouter initialEntries={[`/chat/dm/${directConversationId}`]}>
      <Routes>
        <Route path="/chat" element={<Switcher />}>
          <Route path="dm/:id" element={<ChatMessageArea kind="dm" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("ChatMessageArea — resposta divergente do perfil (#443)", () => {
  it("shows the error state and no identity when the server answers for another conversation", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    // What the real client does when conversation_id does not match the request:
    // it rejects rather than rendering whoever came back.
    mockFetchDirectProfile.mockRejectedValue(
      new ApiRequestError(
        200,
        "invalid_response",
        "Invalid direct profile response: conversation_id does not match the requested conversation",
      ),
    );
    renderDMWithContext(directConversationId);
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(profileToggle());

    expect(await screen.findByText("Não foi possível carregar o perfil.")).toBeInTheDocument();
    // Not a single attribute of the wrong person may reach the panel.
    expect(screen.queryByTestId("chat-details-profile-name")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-profile-meta")).not.toBeInTheDocument();
    expect(screen.queryByText(/@nic\.test/)).not.toBeInTheDocument();
  });

  it("clears the previous DM's profile when the next one violates the contract", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDirectSwitcher();
    await screen.findByTestId("chat-msg-bubble");

    await userEvent.click(profileToggle());
    expect(await screen.findByTestId("chat-details-profile-name")).toHaveTextContent(
      `Perfil de ${directConversationId}`,
    );

    // The panel stays open across the switch, so a stale card is exactly the
    // failure mode: the previous person must be gone even though the new
    // response is unusable.
    mockFetchDirectProfile.mockRejectedValue(
      new ApiRequestError(200, "invalid_response", "Invalid direct profile response: kind"),
    );
    await userEvent.click(screen.getByRole("button", { name: "ir para a outra DM" }));

    expect(await screen.findByText("Não foi possível carregar o perfil.")).toBeInTheDocument();
    expect(screen.queryByText(`Perfil de ${directConversationId}`)).not.toBeInTheDocument();
    expect(screen.queryByText(`${directConversationId}@nic.test`)).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-profile-meta")).not.toBeInTheDocument();
  });
});

describe("ChatMessageArea — ação indisponível do perfil por teclado (#443)", () => {
  it("reaches 'Ver perfil completo' by keyboard and activating it changes nothing", async () => {
    mockFetchDMMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderDMWithContext(directConversationId);
    await screen.findByTestId("chat-msg-bubble");

    const editor = screen.getByTestId("chat-composer-input");
    await fillEditor(editor, "rascunho que não pode sumir");
    const listBefore = screen.getByRole("log", { name: "Mensagens" });
    const messageRequestsBefore = mockFetchDMMessages.mock.calls.length;

    // Opened from the keyboard, as someone who never touches a mouse would.
    const toggle = profileToggle();
    toggle.focus();
    await userEvent.keyboard("{Enter}");

    const panel = await screen.findByTestId("chat-conversation-details");
    const close = screen.getByRole("button", { name: "Fechar perfil" });
    expect(close).toHaveFocus();
    await screen.findByTestId("chat-details-profile-name");

    // The action must be on the real tab path out of the close button.
    const action = screen.getByRole("button", { name: "Ver perfil completo" });
    for (let stop = 0; stop < 20 && document.activeElement !== action; stop += 1) {
      await userEvent.tab();
    }
    expect(action).toHaveFocus();
    expect(action).toHaveAttribute("aria-disabled", "true");
    expect(action).not.toBeDisabled();
    expect(action).toHaveAccessibleDescription(
      "O perfil completo de outros usuários ainda não está disponível nesta versão.",
    );

    const pathBefore = window.location.pathname;
    await userEvent.keyboard("{Enter}");
    await userEvent.keyboard(" ");

    // Nothing happened: no route change, no dialog, and the conversation under
    // the panel was never remounted.
    expect(window.location.pathname).toBe(pathBefore);
    expect(panel).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("log", { name: "Mensagens" })).toBe(listBefore);
    expect(screen.getByTestId("chat-composer-input")).toHaveTextContent(
      "rascunho que não pode sumir",
    );
    expect(mockFetchDMMessages.mock.calls.length).toBe(messageRequestsBefore);

    // And the panel still closes normally, returning focus to its opener.
    await userEvent.click(screen.getByRole("button", { name: "Fechar perfil" }));
    expect(screen.queryByTestId("chat-conversation-details")).not.toBeInTheDocument();
    expect(profileToggle()).toHaveFocus();
  });
});

// ── RF-21 link safety, end to end through the real message area (issue #135) ──
//
// The unit tests around MessageBubble prove the rendering rules; these prove the
// rules survive the whole stack a reader actually goes through — the API mapper,
// useMessages, the list, the bubble. That is what "reload keeps the warning and
// the link" means in practice: nothing here fires a realtime event, it is simply
// a fresh load of the conversation.
describe("ChatMessageArea — RF-21 link safety", () => {
  const linkURL = "https://example.test/artigo";

  beforeEach(() => {
    mockFetchAllowedReactionEmojis.mockResolvedValue([]);
    mockFetchPins.mockResolvedValue([]);
    mockFetchChannelAttachments.mockResolvedValue([]);
  });

  it("keeps the notice and a clickable link across a reload", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "msg-unverified",
          bodyText: `veja ${linkURL} depois`,
          bodyFormat: "v2",
          linkSafetyState: "inconclusive",
        }),
      ]),
    );

    renderChannelArea();

    // The notice, verbatim.
    const notice = await screen.findByTestId("chat-message-link-unverified");
    expect(notice).toHaveTextContent(
      "Não foi possível verificar este link agora. A prévia automática não foi carregada.",
    );

    // And the link is genuinely clickable: a real anchor, with the address the
    // sender wrote, opening in a new tab without leaking this workspace's URL.
    const anchor = await screen.findByRole("link", { name: linkURL });
    expect(anchor).toHaveAttribute("href", linkURL);
    expect(anchor).toHaveAttribute("target", "_blank");
    expect(anchor.getAttribute("rel")).toContain("noopener");
    expect(anchor.getAttribute("rel")).toContain("noreferrer");
  });

  it("withdraws a malicious link after reconnect recovers a missed event", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "msg-unverified",
          bodyText: linkURL,
          bodyFormat: "v2",
          linkSafetyState: "inconclusive",
        }),
      ]),
    );
    mockFetchChannelMessageSecuritySnapshots.mockResolvedValueOnce([
      {
        messageId: "msg-unverified",
        available: true,
        status: "active",
        linkSafetyState: "malicious",
        updatedAt: "2099-08-18T12:00:00Z",
      },
    ]);
    renderChannelArea();
    expect(await screen.findByRole("link", { name: linkURL })).toBeInTheDocument();

    act(() =>
      wsMockState.capturedSubscribed?.({
        type: "subscribed",
        operation: "subscribe",
        target_type: "channel",
        target_id: "geral",
      }),
    );

    await waitFor(() => expect(screen.queryByRole("link", { name: linkURL })).toBeNull());
    expect(screen.queryByText(linkURL)).toBeNull();
    expect(screen.getByTestId("chat-message-link-blocked")).toBeInTheDocument();
    expect(mockFetchChannelMessageSecuritySnapshots).toHaveBeenCalledWith(
      "geral",
      ["msg-unverified"],
      expect.any(AbortSignal),
    );
  });

  it("removes the warning but keeps the anchor after an offline clearance", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "msg-unverified",
          bodyText: linkURL,
          bodyFormat: "v2",
          linkSafetyState: "inconclusive",
        }),
      ]),
    );
    mockFetchChannelMessageSecuritySnapshots.mockResolvedValueOnce([
      {
        messageId: "msg-unverified",
        available: true,
        status: "active",
        linkSafetyState: "safe",
        updatedAt: "2099-08-18T12:00:00Z",
      },
    ]);
    renderChannelArea();
    expect(await screen.findByTestId("chat-message-link-unverified")).toBeInTheDocument();

    act(() =>
      wsMockState.capturedSubscribed?.({
        type: "subscribed",
        operation: "subscribe",
        target_type: "channel",
        target_id: "geral",
      }),
    );

    await waitFor(() =>
      expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("link", { name: linkURL })).toHaveAttribute("href", linkURL);
  });

  it.each([
    ["", "safe"],
    ["", "inconclusive"],
    ["safe", "inconclusive"],
    ["inconclusive", "safe"],
    ["safe", ""],
  ] as const)("renders message.updated link safety %s -> %s", async (before, after) => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "msg-updated",
          bodyText: linkURL,
          bodyFormat: "v2",
          linkSafetyState: before,
        }),
      ]),
    );
    renderChannelArea();
    await screen.findByTestId("chat-msg-bubble");

    act(() =>
      wsMockState.capturedWSMessageUpdated?.({
        type: "message.updated",
        target_type: "channel",
        target_id: "geral",
        message_update: {
          message_id: "msg-updated",
          channel_id: "geral",
          body: after === "" ? "sem URL" : linkURL,
          body_format: "v2",
          edited_at: "2026-08-18T12:00:00Z",
          edit_count: 1,
          is_edited: true,
          link_safety_state: after,
        },
      }),
    );

    if (after === "safe" || after === "inconclusive") {
      expect(await screen.findByRole("link", { name: linkURL })).toHaveAttribute("href", linkURL);
    } else {
      await waitFor(() => expect(screen.queryByRole("link", { name: linkURL })).toBeNull());
    }
    if (after === "inconclusive") {
      expect(screen.getByTestId("chat-message-link-unverified")).toBeInTheDocument();
    } else {
      expect(screen.queryByTestId("chat-message-link-unverified")).toBeNull();
    }
  });

  it("renders a safe message's link as an anchor with no notice", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "msg-safe",
          bodyText: `veja ${linkURL}`,
          bodyFormat: "v2",
          linkSafetyState: "safe",
        }),
      ]),
    );

    renderChannelArea();

    expect(await screen.findByRole("link", { name: linkURL })).toHaveAttribute("href", linkURL);
    expect(screen.queryByTestId("chat-message-link-unverified")).not.toBeInTheDocument();
  });

  it("renders no link at all for a message whose link was condemned", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "msg-blocked",
          bodyText: `veja ${linkURL}`,
          bodyFormat: "v2",
          linkSafetyState: "malicious",
        }),
      ]),
    );

    renderChannelArea();

    expect(await screen.findByTestId("chat-message-link-blocked")).toHaveTextContent(
      "Este link foi bloqueado após a verificação de segurança.",
    );
    expect(screen.queryByRole("link", { name: linkURL })).not.toBeInTheDocument();
    expect(
      screen.queryByText(new RegExp(linkURL.replace(/[/.?]/g, "\\$&"))),
    ).not.toBeInTheDocument();
  });
});
