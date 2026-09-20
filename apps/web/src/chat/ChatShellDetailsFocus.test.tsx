/**
 * The details panel opened from a sidebar row, running for real.
 *
 * A file of its own because ChatShell.test.tsx stubs SidebarDetailsPanel: that
 * stub renders no close button, answers no Escape, moves no focus and fetches
 * nothing, so a regression in any of that would pass there unnoticed.
 * Everything below runs the *real* SidebarDetailsPanel, the real
 * useConversationDetails, the real useChatSidebar and the real
 * ConversationDetailsPanel; only the HTTP calls behind them are mocked,
 * because a fetch is not what is under test.
 *
 * Two subjects share the harness:
 *  - issue #467 (code quality review): focus returns when the panel closes;
 *  - issue #893 (CQ-893-02): the panel converges when the conversation it
 *    describes is renamed while it is open.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { act } from "react";

import CallSessionProvider from "../calls/CallSessionProvider";
import { clearTokens, setTokens } from "../lib/authSession";
import AppShell from "./AppShell";
import ChatShell from "./ChatShell";
import { fetchChannelDetails, fetchSidebarData } from "./chatApi";
import { fetchConversationAttachments } from "./filesApi";
import { _resetChatSocket } from "./chatSocket";
import { useChatWebSocket } from "./useChatWebSocket";
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
        <Route element={<AppShell />}>
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
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const rowTrigger = () => screen.getByRole("button", { name: "Mais opções para canal Infra" });
const navToggle = () => screen.getByTestId("chat-nav-toggle");
const panel = () => screen.queryByTestId("chat-conversation-details");

/**
 * The socket options useChatSidebar registered this render.
 *
 * The transport is stubbed in this file, but the *handlers* are production's:
 * `onConversationUpdated` here is literally the `refreshSidebar` useChatSidebar
 * passes in. Invoking it is what a conversation.updated frame does, so a test
 * that fires it exercises the real reconciliation rather than shortcutting it —
 * nothing here touches the panel, its props or the DOM.
 */
function sidebarSocketOptions() {
  const withHandler = vi
    .mocked(useChatWebSocket)
    .mock.calls.map((call) => call[0])
    .filter((options) => typeof options?.onConversationUpdated === "function");
  const latest = withHandler[withHandler.length - 1];
  if (!latest) throw new Error("useChatSidebar never subscribed to conversation.updated");
  return latest;
}

/** What the server now holds for the "Infra" channel, as both endpoints see it. */
function setServerChannelName(name: string) {
  vi.mocked(fetchSidebarData).mockResolvedValue({
    currentUserId,
    workspaceId: "workspace-1",
    channels: [
      { id: readingId, name: "Plataforma", type: "public", canWrite: true },
      { id: otherId, name, type: "public", canWrite: true },
    ],
    dms: [],
    categories: [],
  });
  vi.mocked(fetchChannelDetails).mockResolvedValue({
    id: otherId,
    slug: "infra",
    name,
    type: "public",
    createdAt: "2026-01-12T09:30:00.000Z",
    memberCount: 4,
    onlineCount: 0,
    onlineMembers: [],
    canManageMembers: false,
  });
}

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

// ── ISSUE #893 (CQ-893-02) — the row-menu panel converges on a rename ───────
//
// This panel holds its own display_name from GET /details. The header's panel
// already refetched when the canonical name moved under it; this one did not,
// so a rename by anybody left it showing the old name until it was closed and
// reopened — while the row right next to it showed the new one.
//
// Nothing below pushes a name into the panel. The server's state changes, the
// application's own conversation.updated handler runs, useChatSidebar refetches
// the canonical list, and the panel is expected to notice and re-read its own
// projection. The name in the panel therefore only ever comes from an HTTP
// response the component asked for.
describe("ChatShell — detalhes abertos pela sidebar convergem após rename (#893)", () => {
  /** How many times the panel has read its own projection. */
  const detailsReads = () => vi.mocked(fetchChannelDetails).mock.calls.length;

  it("recarrega o painel aberto quando o canal é renomeado por outro cliente", async () => {
    const user = userEvent.setup();
    renderShell();
    await screen.findByRole("option", { name: /Infra/ });

    await openDetailsFromSidebar(user);
    expect(screen.getByTestId("chat-details-channel-name")).toHaveTextContent("Infra");
    await waitFor(() => expect(detailsReads()).toBe(1));

    // Somebody else renames the channel: the server now answers differently.
    setServerChannelName("Infraestrutura");
    // …and the frame arrives. This is the handler useChatSidebar registered.
    await act(async () => {
      sidebarSocketOptions().onConversationUpdated?.({
        type: "conversation.updated",
        target_type: "channel",
        target_id: otherId,
      });
    });

    // The row converges, as it already did…
    await screen.findByRole("option", { name: /Infraestrutura/ });
    // …and now so does the panel, without being closed and reopened.
    await waitFor(() =>
      expect(screen.getByTestId("chat-details-channel-name")).toHaveTextContent("Infraestrutura"),
    );
    expect(panel()).toBeInTheDocument();
    // Exactly one reconciliation: the opening read plus one (CQ-893-03).
    expect(detailsReads()).toBe(2);
  });

  // The trigger is "the canonical name of this target moved", not "a frame
  // arrived" — so any refetch of the canonical list converges the panel, which
  // is what makes a reconnect work with no code of its own. And a refetch that
  // finds the same name is not a reason to re-read anything.
  it("converge por refetch autoritativo e não recarrega quando nada mudou", async () => {
    const user = userEvent.setup();
    renderShell();
    await screen.findByRole("option", { name: /Infra/ });

    await openDetailsFromSidebar(user);
    await waitFor(() => expect(detailsReads()).toBe(1));

    // A refresh of the canonical list that finds the conversation unchanged —
    // a reconnect with nothing to report.
    await act(async () => {
      sidebarSocketOptions().onConversationUpdated?.({
        type: "conversation.updated",
        target_type: "channel",
        target_id: otherId,
      });
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(detailsReads()).toBe(1);

    // The same refresh, once the server's own state has moved, does converge.
    setServerChannelName("Plataforma de Infra");
    await act(async () => {
      sidebarSocketOptions().onConversationUpdated?.({
        type: "conversation.updated",
        target_type: "channel",
        target_id: otherId,
      });
    });

    await waitFor(() =>
      expect(screen.getByTestId("chat-details-channel-name")).toHaveTextContent(
        "Plataforma de Infra",
      ),
    );
    expect(detailsReads()).toBe(2);
  });

  // The panel is closed: there is nothing on screen to keep in step, and a
  // refetch for a panel nobody is looking at is wasted.
  it("não busca detalhes quando o rename acontece com o painel fechado", async () => {
    renderShell();
    await screen.findByRole("option", { name: /Infra/ });

    setServerChannelName("Infraestrutura");
    await act(async () => {
      sidebarSocketOptions().onConversationUpdated?.({
        type: "conversation.updated",
        target_type: "channel",
        target_id: otherId,
      });
    });

    await screen.findByRole("option", { name: /Infraestrutura/ });
    expect(detailsReads()).toBe(0);
  });
});
