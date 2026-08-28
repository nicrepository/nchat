/**
 * ISSUE #467 (code quality review) — focus returns when the details panel
 * opened from a sidebar row is closed.
 *
 * A file of its own because ChatShell.test.tsx stubs SidebarDetailsPanel: that
 * stub renders no close button, answers no Escape and moves no focus, so a
 * regression in exactly this behaviour would pass there unnoticed. Everything
 * below runs the *real* SidebarDetailsPanel, the real useConversationDetails and
 * the real ConversationDetailsPanel; only the two HTTP calls behind them are
 * mocked, because a fetch is not what is under test.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import CallSessionProvider from "../calls/CallSessionProvider";
import { clearTokens, setTokens } from "../lib/authSession";
import ChatShell from "./ChatShell";
import { fetchChannelDetails, fetchSidebarData } from "./chatApi";
import { fetchConversationAttachments } from "./filesApi";
import { _resetChatSocket } from "./chatSocket";
import { NAV_DRAWER_QUERY } from "./useNavDrawer";

vi.mock("./chatApi", async () => {
  const actual = await vi.importActual<typeof import("./chatApi")>("./chatApi");
  return { ...actual, fetchSidebarData: vi.fn(), fetchChannelDetails: vi.fn() };
});
vi.mock("./filesApi", async () => {
  const actual = await vi.importActual<typeof import("./filesApi")>("./filesApi");
  return { ...actual, fetchConversationAttachments: vi.fn() };
});
vi.mock("./useChatWebSocket", () => ({ useChatWebSocket: vi.fn() }));

class FakeWebSocket {
  static readonly OPEN = 1;
  readyState = FakeWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  send() {}
  close() {}
}

const currentUserId = "00000000-0000-4000-8000-0000000006a0";
const readingId = "00000000-0000-4000-8000-0000000006a1";
const otherId = "00000000-0000-4000-8000-0000000006a2";
const OriginalWebSocket = global.WebSocket;

/**
 * A MediaQueryList stand-in: jsdom does not implement matchMedia, so leaving it
 * out is exactly the wide-viewport answer — the composition where the sidebar is
 * a permanent column.
 */
function stubViewport(startsAsDrawer: boolean) {
  let drawer = startsAsDrawer;
  window.matchMedia = ((query: string) => ({
    get matches() {
      return query === NAV_DRAWER_QUERY && drawer;
    },
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
  return {
    set(next: boolean) {
      drawer = next;
    },
  };
}

function renderShell() {
  return render(
    <MemoryRouter initialEntries={[`/chat/channel/${readingId}`]}>
      <Routes>
        <Route
          path="/chat"
          element={
            <CallSessionProvider>
              <ChatShell />
            </CallSessionProvider>
          }
        >
          <Route path="channel/:channelId" element={<div>mensagens</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const rowTrigger = () => screen.getByRole("button", { name: "Mais opções para canal Infra" });
const navToggle = () => screen.getByTestId("chat-nav-toggle");
const panel = () => screen.queryByTestId("chat-conversation-details");

/** Opens the details panel for the "Infra" row through its own menu. */
async function openDetailsFromSidebar(user: ReturnType<typeof userEvent.setup>) {
  await user.click(rowTrigger());
  await user.click(screen.getByRole("menuitem", { name: "Detalhes do canal" }));
  await screen.findByTestId("chat-conversation-details");
}

beforeEach(() => {
  global.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  _resetChatSocket(() => 0);
  setTokens("test-token");
  vi.mocked(fetchSidebarData).mockResolvedValue({
    currentUserId,
    workspaceId: "workspace-1",
    channels: [
      { id: readingId, name: "Plataforma", type: "public", canWrite: true },
      { id: otherId, name: "Infra", type: "public", canWrite: true },
    ],
    dms: [],
    categories: [],
  });
  vi.mocked(fetchChannelDetails).mockResolvedValue({
    id: otherId,
    slug: "infra",
    name: "Infra",
    type: "public",
    createdAt: "2026-01-12T09:30:00.000Z",
    memberCount: 4,
    onlineCount: 0,
    onlineMembers: [],
    canManageMembers: false,
  });
  vi.mocked(fetchConversationAttachments).mockResolvedValue([]);
});

afterEach(() => {
  _resetChatSocket();
  global.WebSocket = OriginalWebSocket;
  clearTokens();
  // @ts-expect-error -- jsdom does not define this by default; restore that.
  delete window.matchMedia;
  vi.clearAllMocks();
});

describe("ChatShell — foco ao fechar os detalhes abertos pela sidebar", () => {
  it("devolve o foco à linha que abriu o painel ao fechar pelo botão", async () => {
    const user = userEvent.setup();
    renderShell();
    await screen.findByRole("option", { name: /Infra/ });

    await openDetailsFromSidebar(user);
    // The panel takes focus on open — the behaviour this fix must not disturb.
    expect(screen.getByRole("button", { name: "Fechar detalhes do canal" })).toHaveFocus();

    await user.click(screen.getByRole("button", { name: "Fechar detalhes do canal" }));

    await waitFor(() => expect(panel()).not.toBeInTheDocument());
    expect(rowTrigger()).toHaveFocus();
    expect(document.activeElement).not.toBe(document.body);
  });

  it("devolve o foco à linha que abriu o painel ao fechar com Escape", async () => {
    const user = userEvent.setup();
    renderShell();
    await screen.findByRole("option", { name: /Infra/ });

    await openDetailsFromSidebar(user);

    await user.keyboard("{Escape}");

    await waitFor(() => expect(panel()).not.toBeInTheDocument());
    expect(rowTrigger()).toHaveFocus();
  });

  // The mobile fallback's real trigger is CSS — a row inside a closed drawer is
  // mounted but cannot hold focus — and jsdom applies no stylesheets, so the
  // equivalent unusable opener here is one that has genuinely left the document.
  // The browser-side half of this case is covered end to end in
  // e2e/messaging/responsive-layout.spec.ts, where the drawer really is hidden.
  it("recorre ao toggle de navegação quando o acionador não pode mais receber foco", async () => {
    stubViewport(true);
    const user = userEvent.setup();
    renderShell();
    await screen.findByRole("option", { name: /Infra/ });

    await user.click(navToggle());
    await openDetailsFromSidebar(user);
    // Opening the panel closed the drawer, and the sidebar refetch that follows
    // no longer carries the row: its trigger leaves the document while the panel
    // is open, which is the state a phone reaches by hiding the drawer.
    const opener = rowTrigger();
    opener.remove();
    expect(opener.isConnected).toBe(false);

    await user.click(screen.getByRole("button", { name: "Fechar detalhes do canal" }));

    await waitFor(() => expect(panel()).not.toBeInTheDocument());
    expect(navToggle()).toHaveFocus();
    expect(document.activeElement).not.toBe(document.body);
  });
});
