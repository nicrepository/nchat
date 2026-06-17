/**
 * ChatMessageArea tests.
 *
 * All API calls are mocked at the chatApi module level — no runtime fixtures.
 * The component itself is the unit under test.
 */

import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes, useNavigate } from "react-router-dom";

import type { ChatOutletContext } from "./ChatShell";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import ChatMessageArea from "./ChatMessageArea";
import type { Message, MessagePage } from "./chatTypes";

// ── Mock chatApi ──────────────────────────────────────────────────────────────

const { mockFetchChannelMessages, mockPostChannelMessage, mockFetchDMMessages, mockPostDMMessage } =
  vi.hoisted(() => ({
    mockFetchChannelMessages:
      vi.fn<
        (channelId: string, beforeCursor?: string, signal?: AbortSignal) => Promise<MessagePage>
      >(),
    mockPostChannelMessage:
      vi.fn<(channelId: string, bodyText: string, signal?: AbortSignal) => Promise<Message>>(),
    mockFetchDMMessages:
      vi.fn<
        (
          conversationId: string,
          beforeCursor?: string,
          signal?: AbortSignal,
        ) => Promise<MessagePage>
      >(),
    mockPostDMMessage:
      vi.fn<(conversationId: string, bodyText: string, signal?: AbortSignal) => Promise<Message>>(),
  }));

vi.mock("./chatApi", () => ({
  fetchSidebarData: vi.fn(),
  fetchChannels: vi.fn(),
  fetchDMs: vi.fn(),
  fetchChannelMessages: (channelId: string, beforeCursor?: string, signal?: AbortSignal) =>
    mockFetchChannelMessages(channelId, beforeCursor, signal),
  postChannelMessage: (channelId: string, bodyText: string, signal?: AbortSignal) =>
    mockPostChannelMessage(channelId, bodyText, signal),
  fetchDMMessages: (conversationId: string, beforeCursor?: string, signal?: AbortSignal) =>
    mockFetchDMMessages(conversationId, beforeCursor, signal),
  postDMMessage: (conversationId: string, bodyText: string, signal?: AbortSignal) =>
    mockPostDMMessage(conversationId, bodyText, signal),
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
    expect(input).toBeDisabled();

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
    expect(screen.getByTestId("chat-composer-input")).toBeDisabled();
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
  });

  it("inaccessible DM route shows safe error state and disables composer", async () => {
    mockFetchDMMessages.mockRejectedValue(new Error("not_found"));
    renderDMArea("dm-private-target");

    expect(await screen.findByTestId("chat-msg-error")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-msg-empty")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-composer-input")).toBeDisabled();
    expect(screen.getByTestId("chat-send-btn")).toBeDisabled();
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

// ── Send message ──────────────────────────────────────────────────────────────

describe("ChatMessageArea — send message", () => {
  it("send button calls postChannelMessage with correct args", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockPostChannelMessage.mockResolvedValue(
      makeMessage({ id: "new-1", bodyText: "Nova mensagem" }),
    );
    renderChannelArea("geral");

    const input = await screen.findByTestId("chat-composer-input");
    await user.type(input, "Nova mensagem");

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
    await user.type(input, "Oi!");

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
    await user.type(input, "Teste");
    await user.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => {
      expect(input).toHaveValue("");
    });
  });

  it("send button is disabled when input is empty", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    renderChannelArea();

    const sendBtn = await screen.findByTestId("chat-send-btn");
    expect(sendBtn).toBeDisabled();
  });

  it("Enter key sends message", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockPostChannelMessage.mockResolvedValue(makeMessage({ bodyText: "Enter send" }));
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    await user.type(input, "Enter send{Enter}");

    await waitFor(() => {
      expect(mockPostChannelMessage).toHaveBeenCalledTimes(1);
    });
  });

  it("failed send shows error banner and keeps draft", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    mockPostChannelMessage.mockRejectedValue(new Error("Servidor indisponível"));
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    await user.type(input, "mensagem com erro");
    await user.click(screen.getByTestId("chat-send-btn"));

    await waitFor(() => {
      expect(screen.getByTestId("chat-send-error")).toBeInTheDocument();
    });

    // Draft must be preserved so the user can retry or edit.
    expect(input).toHaveValue("mensagem com erro");
  });

  it("non-Error rejection shows generic error message", async () => {
    const user = userEvent.setup();
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    // Reject with a non-Error value (e.g. a plain string) to cover the fallback error message.
    mockPostChannelMessage.mockRejectedValue("raw string failure");
    renderChannelArea();

    const input = await screen.findByTestId("chat-composer-input");
    await user.type(input, "test");
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
    await user.type(input, "mensagem A");
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
    await user.type(input, "mensagem A sucesso");
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
    await user.type(input, "rascunho canal 1");
    await user.click(screen.getByTestId("chat-send-btn"));

    // Navigate to canal-2 while POST for canal-1 is still in-flight.
    // Composer still carries "rascunho canal 1" (draft not cleared yet) and is disabled.
    await user.click(screen.getByRole("button", { name: "Ir para canal 2" }));

    // Resolve the stale POST for canal-1 — should return { status: "stale" }.
    // With the bug: handleSend would call setDraft("") → draft erased.
    await act(async () => {
      resolveSendA!(makeMessage({ bodyText: "rascunho canal 1" }));
    });

    // Draft must still be present — stale success must not clear it.
    expect(screen.getByTestId("chat-composer-input")).toHaveValue("rascunho canal 1");
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
    await user.type(input, "rascunho canal 1");
    await user.click(screen.getByTestId("chat-send-btn"));

    // Navigate to canal-2 while POST is still in-flight.
    await user.click(screen.getByRole("button", { name: "Ir para canal 2" }));

    // Reject the stale POST for canal-1 — should return { status: "stale" }, no throw.
    await act(async () => {
      rejectSendA!(new Error("Erro do canal A"));
    });

    // Draft must still be present — stale failure must not clear it.
    expect(screen.getByTestId("chat-composer-input")).toHaveValue("rascunho canal 1");
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
        makeMessage({ senderId: "other-456", senderDisplayName: "Fernanda Nicácio", senderEmail: "" }),
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
        makeMessage({ senderId: "other-456", senderDisplayName: "", senderEmail: "fernanda@example.com" }),
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
    expect(input).toHaveAttribute("placeholder", "Mensagem para #geral…");
  });

  it("falls back to raw targetId when channel not found in context", async () => {
    mockFetchChannelMessages.mockResolvedValue(emptyPage);
    renderChannelArea("geral");

    const header = await screen.findByTestId("chat-msg-header");
    expect(header).toHaveTextContent("geral");
  });
});
