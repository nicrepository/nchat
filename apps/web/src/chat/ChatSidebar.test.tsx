import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import RequireAuth from "../auth/RequireAuth";
import ChatShell from "./ChatShell";
import type { Channel, DMConversation } from "./chatTypes";

// ── Mock chatApi ──────────────────────────────────────────────────────────────

const { mockFetchSidebarData } = vi.hoisted(() => ({
  mockFetchSidebarData:
    vi.fn<() => Promise<{ currentUserId: string; channels: Channel[]; dms: DMConversation[] }>>(),
}));

vi.mock("./chatApi", () => ({
  fetchSidebarData: () => mockFetchSidebarData(),
  // Keep individual exports so chatApi.test.ts can still import them.
  fetchChannels: vi.fn(),
  fetchDMs: vi.fn(),
}));

// ── Fixtures ──────────────────────────────────────────────────────────────────

const SAMPLE_CHANNELS: Channel[] = [
  { id: "geral", name: "geral", type: "public" },
  { id: "infraestrutura", name: "infraestrutura", type: "public" },
  { id: "projetos", name: "projetos", type: "private" },
];

const SAMPLE_DMS: DMConversation[] = [
  {
    id: "dm-juliane",
    type: "1:1",
    name: "Juliane Lino",
    participants: [
      {
        id: "juliane",
        displayName: "Juliane Lino",
        initials: "JL",
        color: "rose",
        status: "online",
      },
    ],
  },
  {
    id: "dm-grupo-infra",
    type: "group",
    name: "Equipe Infra",
    participants: [
      {
        id: "juliane",
        displayName: "Juliane Lino",
        initials: "JL",
        color: "rose",
        status: "online",
      },
      { id: "caio", displayName: "Caio Almeida", initials: "CA", color: "blue", status: "away" },
      {
        id: "fernanda",
        displayName: "Fernanda Nicácio",
        initials: "FN",
        color: "teal",
        status: "online",
      },
    ],
  },
];

// ── Render helper ─────────────────────────────────────────────────────────────

function renderChat(initialPath = "/chat", authenticated = true) {
  if (authenticated) {
    setTokens("test-at");
  } else {
    clearTokens();
  }

  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <Routes>
        <Route path="/login" element={<div>Login page</div>} />
        <Route
          path="/chat"
          element={
            <RequireAuth>
              <ChatShell />
            </RequireAuth>
          }
        >
          <Route index element={<div data-testid="chat-default">Selecione um canal</div>} />
          <Route path="channel/:id" element={<div data-testid="chat-channel">channel</div>} />
          <Route path="dm/:id" element={<div data-testid="chat-dm">dm</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

// ── Setup / teardown ──────────────────────────────────────────────────────────

beforeEach(() => {
  clearTokens();
  vi.clearAllMocks();
});

afterEach(() => {
  clearTokens();
});

// ── Auth ──────────────────────────────────────────────────────────────────────

describe("ChatShell — route protection", () => {
  it("redirects unauthenticated user to /login", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat("/chat", false);

    expect(await screen.findByText("Login page")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-shell")).not.toBeInTheDocument();
  });

  it("renders chat shell for authenticated user", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat("/chat", true);

    expect(await screen.findByTestId("chat-shell")).toBeInTheDocument();
  });
});

// ── Shell structure ───────────────────────────────────────────────────────────

describe("ChatShell — shell structure", () => {
  it("renders the dark sidebar", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat();

    expect(await screen.findByTestId("chat-sidebar")).toBeInTheDocument();
  });

  it("sidebar has NIC Chat branding", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat();

    const sidebar = await screen.findByTestId("chat-sidebar");
    expect(sidebar).toHaveTextContent("NIC Chat");
    expect(sidebar).toHaveTextContent("Workspace NIC-Labs");
  });

  it("sidebar header renders NIC-Labs logo with accessible alt text", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat();

    await screen.findByTestId("chat-sidebar");
    const logo = screen.getByRole("img", { name: /nic-labs/i });
    expect(logo).toBeInTheDocument();
    expect(logo).toHaveAttribute("src", "/assets/nic-labs-icon.png");
  });
});

// ── Loading state ─────────────────────────────────────────────────────────────

describe("ChatSidebar — loading state", () => {
  it("shows loading skeleton while request is pending", async () => {
    let resolveData: (v: {
      currentUserId: string;
      channels: Channel[];
      dms: DMConversation[];
    }) => void;
    mockFetchSidebarData.mockReturnValue(new Promise((r) => (resolveData = r)));

    renderChat();

    await screen.findByTestId("chat-sidebar");
    expect(screen.getByRole("status", { name: /carregando/i })).toBeInTheDocument();

    resolveData!({ currentUserId: "", channels: [], dms: [] });
  });
});

// ── Channels ──────────────────────────────────────────────────────────────────

describe("ChatSidebar — channels", () => {
  it("renders the channels section", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    await waitFor(() => {
      expect(screen.getByText("Canais")).toBeInTheDocument();
    });
  });

  it("renders #geral channel", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toBeInTheDocument();
    });
  });

  it("renders all channels", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toBeInTheDocument();
    });

    expect(screen.getByRole("option", { name: /canal infraestrutura/i })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /canal privado projetos/i })).toBeInTheDocument();
  });

  it("renders private channel indicator in accessible label", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    await waitFor(() => {
      const privateBtn = screen.getByRole("option", { name: /privado projetos/i });
      expect(privateBtn).toBeInTheDocument();
    });
  });

  it("shows empty channels state when list is empty", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: SAMPLE_DMS });
    renderChat();

    await waitFor(() => {
      expect(screen.getByText(/nenhum canal disponível/i)).toBeInTheDocument();
    });
  });
});

// ── DMs ───────────────────────────────────────────────────────────────────────

describe("ChatSidebar — DMs", () => {
  it("renders the DMs section", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: SAMPLE_DMS });
    renderChat();

    await waitFor(() => {
      expect(screen.getByText("Mensagens diretas")).toBeInTheDocument();
    });
  });

  it("renders 1:1 DM entry", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: SAMPLE_DMS });
    renderChat();

    await waitFor(() => {
      expect(
        screen.getByRole("option", { name: /mensagem direta com juliane lino/i }),
      ).toBeInTheDocument();
    });
  });

  it("renders group DM indicator in accessible label", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: SAMPLE_DMS });
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /grupo equipe infra/i })).toBeInTheDocument();
    });
  });

  it("shows empty DMs state when list is empty", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    await waitFor(() => {
      expect(screen.getByText(/nenhuma mensagem direta/i)).toBeInTheDocument();
    });
  });
});

// ── Active state ──────────────────────────────────────────────────────────────

describe("ChatSidebar — active selection", () => {
  it("clicking a channel marks it as selected", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    const btn = await screen.findByRole("option", { name: /canal geral/i });
    await user.click(btn);

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toHaveAttribute(
        "aria-selected",
        "true",
      );
    });
  });

  it("clicking a DM marks it as selected", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: SAMPLE_DMS });
    renderChat();

    const btn = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    await user.click(btn);

    await waitFor(() => {
      expect(
        screen.getByRole("option", { name: /mensagem direta com juliane lino/i }),
      ).toHaveAttribute("aria-selected", "true");
    });
  });

  it("clicking a channel renders the channel route", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    const btn = await screen.findByRole("option", { name: /canal geral/i });
    await user.click(btn);

    await waitFor(() => {
      expect(screen.getByTestId("chat-channel")).toBeInTheDocument();
    });
  });

  it("clicking a DM renders the DM route", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: SAMPLE_DMS });
    renderChat();

    const btn = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    await user.click(btn);

    await waitFor(() => {
      expect(screen.getByTestId("chat-dm")).toBeInTheDocument();
    });
  });

  it("channel active on matching route param", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat("/chat/channel/geral");

    const btn = await screen.findByRole("option", { name: /canal geral/i });
    expect(btn).toHaveAttribute("aria-selected", "true");
  });

  it("DM active on matching route param", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: SAMPLE_DMS });
    renderChat("/chat/dm/dm-juliane");

    const btn = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    expect(btn).toHaveAttribute("aria-selected", "true");
  });
});

// ── Error state ───────────────────────────────────────────────────────────────

describe("ChatSidebar — error state", () => {
  it("shows error state when API fails", async () => {
    mockFetchSidebarData.mockRejectedValue(new Error("network failure"));
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
    });

    expect(screen.getByText(/não foi possível carregar os canais/i)).toBeInTheDocument();
  });

  it("error state shows retry button", async () => {
    mockFetchSidebarData.mockRejectedValue(new Error("fail"));
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /tentar novamente/i })).toBeInTheDocument();
    });
  });

  it("retry button reloads data", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData
      .mockRejectedValueOnce(new Error("fail"))
      .mockResolvedValue({ currentUserId: "", channels: SAMPLE_CHANNELS, dms: [] });

    renderChat();

    const retryBtn = await screen.findByRole("button", { name: /tentar novamente/i });
    await user.click(retryBtn);

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toBeInTheDocument();
    });
  });
});

// ── Security ──────────────────────────────────────────────────────────────────

describe("ChatSidebar — storage safety", () => {
  it("sidebar mount and data-load add no storage writes", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: SAMPLE_DMS,
    });

    // Auth token written BEFORE the spy is installed so the nchat_at write
    // from setTokens() is not captured. The spy then covers the full sidebar
    // mount + async data-load window with no blind spots.
    setTokens("test-at");
    const setItemSpy = vi.spyOn(Storage.prototype, "setItem");

    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <Routes>
          <Route path="/login" element={<div>Login page</div>} />
          <Route
            path="/chat"
            element={
              <RequireAuth>
                <ChatShell />
              </RequireAuth>
            }
          >
            <Route index element={<div>default</div>} />
            <Route path="channel/:id" element={<div>channel</div>} />
            <Route path="dm/:id" element={<div>dm</div>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toBeInTheDocument();
    });

    // Sidebar mount + data load must write nothing to storage.
    expect(setItemSpy).not.toHaveBeenCalled();
    setItemSpy.mockRestore();
  });
});

// ── Footer ────────────────────────────────────────────────────────────────────

describe("ChatSidebar — footer", () => {
  it("renders placeholder user name in footer", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat();

    await screen.findByTestId("chat-sidebar");
    // Footer renders the placeholder user; real profile comes from a future /api/auth/me endpoint.
    expect(screen.getByTestId("chat-sidebar")).toHaveTextContent("Usuário");
  });

  it("footer does not render fixture user identity", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat();

    await screen.findByTestId("chat-sidebar");
    // The sidebar must never show hardcoded personal data.
    expect(screen.getByTestId("chat-sidebar")).not.toHaveTextContent("Álvaro Neto");
  });
});

// ── Route encoding ────────────────────────────────────────────────────────────

describe("ChatSidebar — route encoding", () => {
  it("navigates with encoded channel ID containing special chars", async () => {
    const user = userEvent.setup();
    const channelWithSpace: Channel = { id: "equipe infra", name: "equipe infra", type: "public" };
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: [channelWithSpace],
      dms: [],
    });
    renderChat();

    const btn = await screen.findByRole("option", { name: /canal equipe infra/i });
    await user.click(btn);

    // After clicking, the channel should become active (route changed, location decoded)
    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal equipe infra/i })).toHaveAttribute(
        "aria-selected",
        "true",
      );
    });
  });

  it("marks channel active when route has encoded ID", async () => {
    const channelWithSpace: Channel = { id: "equipe infra", name: "equipe infra", type: "public" };
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: [channelWithSpace],
      dms: [],
    });
    // Simulate pre-encoded URL: /chat/channel/equipe%20infra
    renderChat("/chat/channel/equipe%20infra");

    const btn = await screen.findByRole("option", { name: /canal equipe infra/i });
    expect(btn).toHaveAttribute("aria-selected", "true");
  });

  it("does not crash with malformed percent-encoded route segment", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    // `%` alone is invalid percent-encoding; decodeURIComponent would throw without the guard.
    renderChat("/chat/channel/%");

    // Sidebar must still render and show channels without throwing.
    await screen.findByTestId("chat-sidebar");
    expect(screen.getByTestId("chat-sidebar")).toBeInTheDocument();
  });
});

// ── No fixture import in production modules ───────────────────────────────────

describe("chatApi — no runtime fixture import", () => {
  it("chatApi.ts does not import from chatFixtures", async () => {
    const { readFileSync } = await import("node:fs");
    const { resolve } = await import("node:path");
    const src = readFileSync(resolve(__dirname, "chatApi.ts"), "utf-8");
    expect(src).not.toMatch(/from\s+["'].*chatFixtures/);
    expect(src).not.toMatch(/import\s*\(["'].*chatFixtures/);
  });

  it("ChatSidebar.tsx does not import from chatFixtures", async () => {
    const { readFileSync } = await import("node:fs");
    const { resolve } = await import("node:path");
    const src = readFileSync(resolve(__dirname, "ChatSidebar.tsx"), "utf-8");
    expect(src).not.toMatch(/from\s+["'].*chatFixtures/);
    expect(src).not.toMatch(/import\s*\(["'].*chatFixtures/);
  });
});
