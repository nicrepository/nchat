/**
 * ChatMessageArea tests.
 *
 * All API calls are mocked at the chatApi module level — no runtime fixtures.
 * The component itself is the unit under test.
 */

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes, useNavigate } from "react-router-dom";

import type { ChatOutletContext } from "./ChatShell";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import ChatMessageArea from "./ChatMessageArea";
import { avatarColorFor } from "./messageDisplay";
import type { Message, MessagePage } from "./chatTypes";
import type {
  WSMessageCreatedEvent,
  WSMessageUpdatedEvent,
  WSReactionUpdatedEvent,
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
  mockPostChannelMessage,
  mockForwardChannelMessage,
  mockFetchDMMessages,
  mockFetchDMMessage,
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
  wsMockState,
} = vi.hoisted(() => ({
  mockFetchChannelMessages:
    vi.fn<
      (channelId: string, beforeCursor?: string, signal?: AbortSignal) => Promise<MessagePage>
    >(),
  mockFetchChannelMessage:
    vi.fn<(channelId: string, messageId: string, signal?: AbortSignal) => Promise<Message>>(),
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
  wsMockState: {
    capturedWSMessageCreated: null as ((event: WSMessageCreatedEvent) => void) | null,
    capturedWSMessageUpdated: null as ((event: WSMessageUpdatedEvent) => void) | null,
    capturedReactionUpdated: null as ((event: WSReactionUpdatedEvent) => void) | null,
    toggleReaction: vi.fn(() => true),
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
}));

// useChatWebSocket is a no-op in component tests — WS behaviour is tested in
// useChatWebSocket.test.ts.
vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: vi.fn(
    ({
      onMessageCreated,
      onMessageUpdated,
      onReactionUpdated,
    }: {
      onMessageCreated: (event: WSMessageCreatedEvent) => void;
      onMessageUpdated?: (event: WSMessageUpdatedEvent) => void;
      onReactionUpdated?: (event: WSReactionUpdatedEvent) => void;
    }) => {
      wsMockState.capturedWSMessageCreated = onMessageCreated;
      wsMockState.capturedWSMessageUpdated = onMessageUpdated ?? null;
      wsMockState.capturedReactionUpdated = onReactionUpdated ?? null;
      return { toggleReaction: wsMockState.toggleReaction };
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
  vi.clearAllMocks();
  vi.mocked(useChatWebSocket).mockImplementation(
    ({ onMessageCreated, onMessageUpdated, onReactionUpdated }) => {
      wsMockState.capturedWSMessageCreated = onMessageCreated;
      wsMockState.capturedWSMessageUpdated = onMessageUpdated ?? null;
      wsMockState.capturedReactionUpdated = onReactionUpdated ?? null;
      return { toggleReaction: wsMockState.toggleReaction };
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
  mockPinMessage.mockResolvedValue(undefined);
  mockUnpinMessage.mockResolvedValue(undefined);
  mockGetMessageHistory.mockResolvedValue({ entries: [], nextCursor: undefined });
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

  it("filters stored recent emojis against the server allowlist", async () => {
    localStorage.setItem("nchat_recent_reactions:me-123", JSON.stringify(["🛑", "🚀", "👍"]));
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage()]));
    renderChannelAreaForUser();
    const bubble = await screen.findByTestId("chat-msg-bubble");

    fireEvent.mouseEnter(bubble);

    expect(screen.queryByRole("button", { name: "Reagir rapidamente com 🛑" })).toBeNull();
    expect(screen.getByRole("button", { name: "Reagir rapidamente com 🚀" })).toBeVisible();
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

  it("stores a confirmed own reaction as the most recent allowed emoji", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

    act(() =>
      wsMockState.capturedReactionUpdated?.({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "geral",
        message_id: "m1",
        reaction: {
          message_id: "m1",
          actor_user_id: "me-123",
          emoji: "🚀",
          added: true,
          reactions: [{ emoji: "🚀", count: 1 }],
        },
      }),
    );

    await waitFor(() =>
      expect(JSON.parse(localStorage.getItem("nchat_recent_reactions:me-123") ?? "[]")[0]).toBe(
        "🚀",
      ),
    );
  });

  it("renders a confirmed reaction when local preference storage is unavailable", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("quota", "QuotaExceededError");
    });
    renderChannelAreaForUser();
    await screen.findByTestId("chat-msg-bubble");

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

  it("renders reaction counts, uses the server allowlist and closes after selection", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({ id: "m1", reactions: [{ emoji: "🎉", count: 3, reactedByMe: true }] }),
      ]),
    );
    renderChannelArea();

    expect(await screen.findByRole("button", { name: "Remover reação 🎉" })).toHaveTextContent("3");
    await openFullReactionPicker();
    await userEvent.click(screen.getByRole("button", { name: "Reagir com 👍" }));
    expect(wsMockState.toggleReaction).toHaveBeenCalledWith("m1", "👍");
    expect(screen.queryByRole("dialog", { name: "Escolher reação" })).not.toBeInTheDocument();
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
          if (this.classList.contains("chat-msg-area__reaction-grid")) {
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
    renderChannelArea();

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
          actor_user_id: "other-user",
          emoji: "👍",
          added: true,
          reactions: [{ emoji: "👍", count: 1 }],
        },
      }),
    );

    expect(await screen.findByRole("button", { name: "Adicionar reação 👍" })).toHaveTextContent(
      "1",
    );
  });

  it("rejects a reaction whose bytes do not match the server allowlist", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [{ emoji: "❤", count: 1, reactedByMe: false }],
        }),
      ]),
    );
    renderChannelArea();

    await userEvent.click(await screen.findByRole("button", { name: "Adicionar reação ❤" }));

    expect(wsMockState.toggleReaction).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent(/emoji não permitido/i);
  });

  it("renders N reactions and toggles an existing pill without opening the grid", async () => {
    mockFetchChannelMessages.mockResolvedValue(
      messagePage([
        makeMessage({
          id: "m1",
          reactions: [
            { emoji: "👍", count: 2, reactedByMe: false },
            { emoji: "🎉", count: 4, reactedByMe: true },
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
          reactions: [{ emoji: "👍", count: 1, reactedByMe: false }],
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
          reactions: [{ emoji: "👍", count: 1, reactedByMe: false }],
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
    expect(mockPostChannelMessage.mock.calls[0]?.[2]).toBe("m1");
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
    expect(mockPostChannelMessage.mock.calls[0]?.slice(0, 4)).toEqual([
      "destination",
      "seguro",
      undefined,
      undefined,
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
    expect(mockPostChannelMessage.mock.calls[0]?.slice(0, 4)).toEqual([
      "destination",
      "veja",
      undefined,
      rf09SourceMessageID,
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

    // Resolve the stale POST for canal-1 — should return { status: "stale" }.
    await act(async () => {
      resolveSendA!(makeMessage({ bodyText: "rascunho canal 1" }));
    });

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

    // The observer was created once (when hasMore became true) and observe() fired once.
    // After prepend, hasMore=false → effect re-runs with !hasMore → returns early, no new observer.
    expect(observeCallCount).toBe(1);
    // Exactly two fetches: initial load + one loadMore.
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
        return { toggleReaction: wsMockState.toggleReaction };
      },
    );
  });

  afterEach(() => {
    // Restore to the default no-op so other test suites are not affected.
    vi.mocked(useChatWebSocket).mockImplementation(() => ({
      toggleReaction: wsMockState.toggleReaction,
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
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1000 });
    Object.defineProperty(list, "clientHeight", { configurable: true, value: 400 });
    Object.defineProperty(list, "scrollTop", { configurable: true, writable: true, value: 0 });
    fireEvent.scroll(list);

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
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 500 });
    Object.defineProperty(list, "clientHeight", { configurable: true, value: 400 });
    Object.defineProperty(list, "scrollTop", { configurable: true, writable: true, value: 99 });
    fireEvent.scroll(list);

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
    // The pins bar appears once the reload resolves with the new pin.
    expect(await screen.findByTestId("chat-pins")).toHaveTextContent("1 mensagem fixada");
  });

  it("expands the pins bar and unpins a removed message", async () => {
    mockFetchChannelMessages.mockResolvedValue(messagePage([makeMessage({ id: "m1" })]));
    mockFetchPins.mockResolvedValue([
      {
        message: makeMessage({ id: "m1", senderDisplayName: "Ana", bodyText: "ativo" }),
        pinnedByUserId: "u1",
        pinnedAt: new Date().toISOString(),
      },
      {
        message: makeMessage({ id: "m2", isRemoved: true, status: "deleted", bodyText: "" }),
        pinnedByUserId: "u2",
        pinnedAt: new Date().toISOString(),
      },
    ]);
    renderChannelAreaForUser();

    await userEvent.click(await screen.findByRole("button", { name: /2 mensagens fixadas/i }));

    expect(screen.getByText(/Ana:/)).toBeInTheDocument();
    const removedItem = screen.getByText("Mensagem removida.").closest("li");
    expect(removedItem).not.toBeNull();

    await userEvent.click(removedItem!.querySelector("button")!);

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
    expect(await screen.findByTestId("chat-pins")).toHaveTextContent("1 mensagem fixada");
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
