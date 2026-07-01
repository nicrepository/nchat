/**
 * ChatMessageArea tests.
 *
 * All API calls are mocked at the chatApi module level — no runtime fixtures.
 * The component itself is the unit under test.
 */

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes, useNavigate } from "react-router-dom";

import type { ChatOutletContext } from "./ChatShell";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import ChatMessageArea from "./ChatMessageArea";
import type { Message, MessagePage } from "./chatTypes";
import type { WSMessageCreatedEvent } from "./useChatWebSocket";
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

// ── Mock chatApi ──────────────────────────────────────────────────────────────

const {
  mockFetchChannelMessages,
  mockFetchChannelMessage,
  mockPostChannelMessage,
  mockFetchDMMessages,
  mockPostDMMessage,
  wsMockState,
} = vi.hoisted(() => ({
  mockFetchChannelMessages:
    vi.fn<
      (channelId: string, beforeCursor?: string, signal?: AbortSignal) => Promise<MessagePage>
    >(),
  mockFetchChannelMessage:
    vi.fn<(channelId: string, messageId: string, signal?: AbortSignal) => Promise<Message>>(),
  mockPostChannelMessage:
    vi.fn<(channelId: string, bodyText: string, signal?: AbortSignal) => Promise<Message>>(),
  mockFetchDMMessages:
    vi.fn<
      (conversationId: string, beforeCursor?: string, signal?: AbortSignal) => Promise<MessagePage>
    >(),
  mockPostDMMessage:
    vi.fn<(conversationId: string, bodyText: string, signal?: AbortSignal) => Promise<Message>>(),
  wsMockState: {
    capturedWSMessageCreated: null as ((event: WSMessageCreatedEvent) => void) | null,
  },
}));

vi.mock("./chatApi", () => ({
  fetchSidebarData: vi.fn(),
  fetchChannels: vi.fn(),
  fetchDMs: vi.fn(),
  fetchChannelMessages: (channelId: string, beforeCursor?: string, signal?: AbortSignal) =>
    mockFetchChannelMessages(channelId, beforeCursor, signal),
  fetchChannelMessage: mockFetchChannelMessage,
  postChannelMessage: (channelId: string, bodyText: string, signal?: AbortSignal) =>
    mockPostChannelMessage(channelId, bodyText, signal),
  fetchDMMessages: (conversationId: string, beforeCursor?: string, signal?: AbortSignal) =>
    mockFetchDMMessages(conversationId, beforeCursor, signal),
  postDMMessage: (conversationId: string, bodyText: string, signal?: AbortSignal) =>
    mockPostDMMessage(conversationId, bodyText, signal),
  fetchDMMessage: vi.fn(),
}));

// useChatWebSocket is a no-op in component tests — WS behaviour is tested in
// useChatWebSocket.test.ts.
vi.mock("./useChatWebSocket", () => ({
  useChatWebSocket: vi.fn(
    ({ onMessageCreated }: { onMessageCreated: (event: WSMessageCreatedEvent) => void }) => {
      wsMockState.capturedWSMessageCreated = onMessageCreated;
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
  isRemoved: false,
  status: "active",
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
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

// ── Setup / teardown ──────────────────────────────────────────────────────────

beforeEach(() => {
  setTokens("test-at");
  wsMockState.capturedWSMessageCreated = null;
  vi.clearAllMocks();
  // jsdom does not implement scrollIntoView; mock it so the branch is reachable.
  window.Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
  clearTokens();
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
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    await fillEditor(input, "line one");

    fireEvent.keyDown(input, { key: "Enter", code: "Enter", shiftKey: true });

    // Nothing sent.
    expect(mockPostChannelMessage).not.toHaveBeenCalled();
    expect(input).toHaveTextContent("line one");
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
      // Canal-2 is ready (empty state or loading done).
      // Check only message bubbles — not the composer textarea which may still hold the draft.
      const bubbles = screen.queryAllByTestId("chat-msg-bubble");
      expect(bubbles.some((b) => b.textContent?.includes("mensagem A sucesso"))).toBe(false);
    });
  });

  it("stale send success does not clear draft still in composer after target change", async () => {
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

    // Navigate to canal-2 while POST for canal-1 is still in-flight.
    // Composer still carries "rascunho canal 1" (draft not cleared yet) and is disabled.
    await user.click(screen.getByRole("button", { name: "Ir para canal 2" }));

    // Resolve the stale POST for canal-1 — should return { status: "stale" }.
    // With the bug: handleSend would call setDraft("") → draft erased.
    await act(async () => {
      resolveSendA!(makeMessage({ bodyText: "rascunho canal 1" }));
    });

    // Content must still be present — stale success must not clear it.
    expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("rascunho canal 1");
    // Canal-1 message must not appear in canal-2's message bubble list.
    const bubbles = screen.queryAllByTestId("chat-msg-bubble");
    expect(bubbles.some((b) => b.textContent?.includes("rascunho canal 1"))).toBe(false);
    // No error banner in canal-2.
    expect(screen.queryByTestId("chat-send-error")).not.toBeInTheDocument();
    // Send button usable (not stuck in sending state).
    expect(screen.getByTestId("chat-send-btn")).not.toBeDisabled();
  });

  it("stale send failure does not show error or clear draft in new target", async () => {
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

    // Content must still be present — stale failure must not clear it.
    expect(screen.getByTestId("chat-composer-input")).toHaveTextContent("rascunho canal 1");
    // No error banner in canal-2.
    expect(screen.queryByTestId("chat-send-error")).not.toBeInTheDocument();
    // Send button usable (not stuck in sending state).
    expect(screen.getByTestId("chat-send-btn")).not.toBeDisabled();
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
                  channels: [{ id: "ch-uuid-001", name: "geral", type: "public" }],
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
                  channels: [{ id: "ch-uuid-001", name: "geral", type: "public" }],
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
      },
    );
  });

  afterEach(() => {
    // Restore to the default no-op so other test suites are not affected.
    vi.mocked(useChatWebSocket).mockImplementation(vi.fn());
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
