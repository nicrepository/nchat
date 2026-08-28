import { render, screen } from "@testing-library/react";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";

import type { ChatOutletContext } from "./ChatShell";
import type { Channel, DMConversation } from "./chatTypes";
import ChatPlaceholder from "./ChatPlaceholder";

// ── Helper ────────────────────────────────────────────────────────────────────

function renderAtPath(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/chat" element={<ChatPlaceholder />} />
        <Route path="/chat/channel/:id" element={<ChatPlaceholder type="channel" />} />
        <Route path="/chat/dm/:id" element={<ChatPlaceholder type="dm" />} />
      </Routes>
    </MemoryRouter>,
  );
}

/**
 * Renders the index route the same way ChatShell wires it in production: a
 * parent route element providing `<Outlet context={...}>`, with the index
 * route's `ChatPlaceholder` as its child. The landed-on route renders a
 * marker div so a test can assert which one won without depending on
 * ChatMessageArea (out of scope here — only the redirect target matters).
 */
function renderIndexWithOutletContext(
  ctx: Partial<ChatOutletContext> & { channels: Channel[]; dms: DMConversation[] },
  locationState?: unknown,
) {
  const fullCtx: ChatOutletContext = { currentUserId: "current-user", ...ctx };
  return render(
    <MemoryRouter initialEntries={[{ pathname: "/chat", state: locationState }]}>
      <Routes>
        <Route path="/chat" element={<Outlet context={fullCtx} />}>
          <Route index element={<ChatPlaceholder />} />
          <Route path="channel/:id" element={<div data-testid="landed">channel</div>} />
          <Route path="dm/:id" element={<div data-testid="landed">dm</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

// ── Tests ─────────────────────────────────────────────────────────────────────

function channel(id: string, overrides: Partial<Channel> = {}): Channel {
  return { id, name: id, type: "public", canWrite: true, ...overrides };
}

function dm(
  id: string,
  type: "1:1" | "group",
  overrides: Partial<DMConversation> = {},
): DMConversation {
  return { id, type, name: id, participants: [], ...overrides };
}

describe("ChatPlaceholder — /chat (index)", () => {
  it("renders the empty/select state", () => {
    renderAtPath("/chat");
    expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
  });

  it("shows invite-to-select message", () => {
    renderAtPath("/chat");
    expect(screen.getByText(/selecione um canal ou mensagem direta/i)).toBeInTheDocument();
  });

  it("shows guidance subtitle", () => {
    renderAtPath("/chat");
    expect(screen.getByText(/escolha um canal ou uma conversa/i)).toBeInTheDocument();
  });
});

describe("ChatPlaceholder — /chat/channel/:id", () => {
  it("renders channel selected state for /chat/channel/geral", () => {
    renderAtPath("/chat/channel/geral");
    expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
  });

  it("shows channel name with # prefix", () => {
    renderAtPath("/chat/channel/geral");
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent("#geral");
  });

  it("shows coming-soon subtitle for channel", () => {
    renderAtPath("/chat/channel/geral");
    expect(screen.getByText(/as mensagens aparecerão aqui em breve/i)).toBeInTheDocument();
  });

  it("shows correct channel name for infraestrutura", () => {
    renderAtPath("/chat/channel/infraestrutura");
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent("#infraestrutura");
  });
});

describe("ChatPlaceholder — /chat/dm/:id", () => {
  it("renders DM selected state for /chat/dm/alvaro", () => {
    renderAtPath("/chat/dm/alvaro");
    expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
  });

  it("shows coming-soon subtitle for DM", () => {
    renderAtPath("/chat/dm/alvaro");
    expect(screen.getByText(/as mensagens aparecerão aqui em breve/i)).toBeInTheDocument();
  });

  it("does not show channel # prefix for DM route", () => {
    renderAtPath("/chat/dm/alvaro");
    const heading = screen.getByRole("heading", { level: 2 });
    expect(heading.textContent).not.toMatch(/^#/);
  });
});

describe("ChatPlaceholder — default-conversation redirect", () => {
  it("does nothing when there are no conversations at all", () => {
    renderIndexWithOutletContext({ channels: [], dms: [] });
    expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
    expect(screen.queryByTestId("landed")).not.toBeInTheDocument();
  });

  it("redirects to the most recently active unread conversation", () => {
    renderIndexWithOutletContext({
      channels: [channel("geral", { unreadCount: 2, lastMessageAt: "2026-08-04T10:00:00Z" })],
      dms: [dm("alvaro", "1:1", { unreadCount: 1, lastMessageAt: "2026-08-04T12:00:00Z" })],
    });
    expect(screen.getByTestId("landed")).toHaveTextContent("dm");
  });

  it("falls back to the most recently active conversation when nothing is unread", () => {
    renderIndexWithOutletContext({
      channels: [channel("geral", { lastMessageAt: "2026-08-04T09:00:00Z" })],
      dms: [dm("alvaro", "1:1", { lastMessageAt: "2026-08-04T12:00:00Z" })],
    });
    expect(screen.getByTestId("landed")).toHaveTextContent("dm");
  });

  // leaveConversation (useChatSidebar.ts) sets this flag on its own
  // navigate("/chat", ...) so a reader who just left a conversation lands on
  // the neutral route it documents, not wherever this redirect would send
  // them next.
  it("does not redirect when the navigation to /chat is marked to skip it", () => {
    renderIndexWithOutletContext(
      {
        channels: [channel("geral", { unreadCount: 2, lastMessageAt: "2026-08-04T10:00:00Z" })],
        dms: [dm("alvaro", "1:1", { unreadCount: 1, lastMessageAt: "2026-08-04T12:00:00Z" })],
      },
      { skipDefaultConversationRedirect: true },
    );
    expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
    expect(screen.queryByTestId("landed")).not.toBeInTheDocument();
  });

  it("never redirects a route that already names a conversation", () => {
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <Routes>
          <Route
            path="/chat"
            element={
              <Outlet
                context={{
                  currentUserId: "current-user",
                  channels: [channel("geral")],
                  dms: [
                    dm("alvaro", "1:1", { unreadCount: 1, lastMessageAt: "2026-08-04T12:00:00Z" }),
                  ],
                }}
              />
            }
          >
            <Route path="channel/:id" element={<ChatPlaceholder type="channel" />} />
            <Route path="dm/:id" element={<div data-testid="landed">dm</div>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByTestId("chat-placeholder")).toBeInTheDocument();
    expect(screen.queryByTestId("landed")).not.toBeInTheDocument();
  });
});
