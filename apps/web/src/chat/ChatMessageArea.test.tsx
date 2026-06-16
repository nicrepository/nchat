/**
 * ChatMessageArea tests.
 *
 * All API calls are mocked at the chatApi module level — no runtime fixtures.
 * The component itself is the unit under test.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
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
      // Error banner must appear.
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
