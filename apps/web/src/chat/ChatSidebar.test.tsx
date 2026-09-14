import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import { clearTokens, setTokens } from "../lib/authSession";
import { _resetSelfProfile, refreshSelfProfile } from "../profile/selfProfile";
import type { SelfProfile } from "../profile/profileApi";
import RequireAuth from "../auth/RequireAuth";
import CallSessionProvider from "../calls/CallSessionProvider";
import AppShell from "./AppShell";
import ChatShell from "./ChatShell";
import ChatSidebar from "./ChatSidebar";
import type {
  Channel,
  DMCandidate,
  DirectDMResult,
  DMConversation,
  ChannelCategory,
} from "./chatTypes";

// ── Mock chatApi ──────────────────────────────────────────────────────────────

const {
  mockFetchSidebarData,
  mockSearchDMCandidates,
  mockGetOrCreateDirectDM,
  mockCreateGroupDM,
  mockCreateChannel,
  mockMarkConversationRead,
} = vi.hoisted(() => ({
  mockFetchSidebarData: vi.fn<
    () => Promise<{
      currentUserId: string;
      channels: Channel[];
      dms: DMConversation[];
      categories?: ChannelCategory[];
    }>
  >(),
  mockSearchDMCandidates: vi.fn<(query: string, signal?: AbortSignal) => Promise<DMCandidate[]>>(),
  mockGetOrCreateDirectDM:
    vi.fn<(userId: string, signal?: AbortSignal) => Promise<DirectDMResult>>(),
  mockCreateGroupDM:
    vi.fn<(userIds: string[], title: string, signal?: AbortSignal) => Promise<string>>(),
  mockCreateChannel: vi.fn<
    (
      input: {
        slug: string;
        displayName: string;
        type: "public" | "private";
        categoryId?: string;
      },
      signal?: AbortSignal,
    ) => Promise<Channel>
  >(),
  mockMarkConversationRead: vi.fn<() => Promise<void>>(),
}));

vi.mock("./chatApi", () => ({
  fetchSidebarData: () => {
    const res = mockFetchSidebarData();
    if (res && typeof res.then === "function") {
      return res.then((data) => ({
        categories: [{ name: "Geral", kind: "uncategorized" }],
        ...data,
      }));
    }
    return Promise.resolve({
      categories: [{ name: "Geral", kind: "uncategorized" }],
      currentUserId: "",
      channels: [],
      dms: [],
    });
  },
  // Keep individual exports so chatApi.test.ts can still import them.
  fetchChannels: vi.fn(),
  fetchDMs: vi.fn(),
  searchDMCandidates: (query: string, signal?: AbortSignal) =>
    mockSearchDMCandidates(query, signal),
  getOrCreateDirectDM: (userId: string, signal?: AbortSignal) =>
    mockGetOrCreateDirectDM(userId, signal),
  createGroupDM: (userIds: string[], title: string, signal?: AbortSignal) =>
    mockCreateGroupDM(userIds, title, signal),
  createChannel: (
    input: { slug: string; displayName: string; type: "public" | "private"; categoryId?: string },
    signal?: AbortSignal,
  ) => mockCreateChannel(input, signal),
  markConversationRead: () => mockMarkConversationRead(),
}));

// ── Mock profileApi (the footer's identity source) ────────────────────────────

const { mockFetchMyProfile } = vi.hoisted(() => ({
  mockFetchMyProfile: vi.fn<(signal?: AbortSignal) => Promise<SelfProfile>>(),
}));

vi.mock("../profile/profileApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../profile/profileApi")>();
  return { ...actual, fetchMyProfile: (signal?: AbortSignal) => mockFetchMyProfile(signal) };
});

// ── Fixtures ──────────────────────────────────────────────────────────────────

const SAMPLE_CHANNELS: Channel[] = [
  { id: "geral", name: "geral", type: "public", canWrite: true },
  { id: "infraestrutura", name: "infraestrutura", type: "public", canWrite: true },
  { id: "projetos", name: "projetos", type: "private", canWrite: true },
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
          element={
            <RequireAuth>
              <CallSessionProvider>
                <AppShell />
              </CallSessionProvider>
            </RequireAuth>
          }
        >
          <Route path="/chat" element={<ChatShell />}>
            <Route index element={<div data-testid="chat-default">Selecione um canal</div>} />
            <Route path="channel/:id" element={<div data-testid="chat-channel">channel</div>} />
            <Route path="dm/:id" element={<div data-testid="chat-dm">dm</div>} />
          </Route>
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

// ── Setup / teardown ──────────────────────────────────────────────────────────

beforeEach(() => {
  clearTokens();
  vi.clearAllMocks();
  _resetSelfProfile();
  mockSearchDMCandidates.mockResolvedValue([]);
  mockGetOrCreateDirectDM.mockResolvedValue({ conversationId: "dm-new", created: true });
  mockFetchMyProfile.mockResolvedValue({ id: "user-a", displayName: "Ana Souza" });
});

afterEach(() => {
  clearTokens();
  _resetSelfProfile();
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

  it("sidebar presents the official Nchat product and workspace names", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat();

    const sidebar = await screen.findByTestId("chat-sidebar");
    const brand = within(sidebar).getByRole("link", { name: "Nchat — Workspace Nic-Labs" });
    expect(brand).toHaveTextContent("Nchat");
    expect(brand).toHaveTextContent("Workspace Nic-Labs");
    expect(brand).not.toHaveTextContent("NIC Chat");
    expect(brand).not.toHaveTextContent("Workspace NIC-Labs");
    expect(brand).toHaveAttribute("href", "/chat");
  });

  it("sidebar header renders the official logo as a decorative image", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat();

    const sidebar = await screen.findByTestId("chat-sidebar");
    const logo = sidebar.querySelector<HTMLImageElement>(".chat-sidebar__brand-img");
    expect(logo).toBeInTheDocument();
    expect(logo).toHaveAttribute("src", "/assets/icononly_transparent.png");
    expect(logo).toHaveAttribute("alt", "");
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

  it("does not dispatch state updates after unmount (cancelled guard)", async () => {
    let resolveData: (v: {
      currentUserId: string;
      channels: Channel[];
      dms: DMConversation[];
    }) => void;
    mockFetchSidebarData.mockReturnValue(new Promise((r) => (resolveData = r)));

    const { unmount } = renderChat();

    await screen.findByTestId("chat-sidebar");

    // Unmount before fetch resolves — sets the cancelled flag in useChatSidebar.
    unmount();

    // Resolving after unmount must not cause a state update or React warning.
    resolveData!({ currentUserId: "", channels: [], dms: [] });
  });
});

// ── Channels ──────────────────────────────────────────────────────────────────

describe("ChatSidebar — channels", () => {
  it("keeps a read-only channel visible in the sidebar", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "",
      channels: [{ ...SAMPLE_CHANNELS[1], canWrite: false }],
      dms: [],
    });
    renderChat();

    expect(
      await screen.findByRole("option", { name: /canal infraestrutura/i }),
    ).toBeInTheDocument();
  });

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

  // The API client always yields `participants: []`, so these use the real
  // production shape rather than the avatar-rich fixtures above.
  it("labels each 1:1 DM with its own participant name", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        { id: "dm-1", type: "1:1", name: "Juliane Lino", participants: [] },
        { id: "dm-2", type: "1:1", name: "Caio Almeida", participants: [] },
      ],
    });
    renderChat();

    await waitFor(() => {
      expect(
        screen.getByRole("option", { name: /mensagem direta com juliane lino/i }),
      ).toBeInTheDocument();
    });
    expect(
      screen.getByRole("option", { name: /mensagem direta com caio almeida/i }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Mensagem Direta")).not.toBeInTheDocument();
  });

  it("keeps a group DM labelled with its group name", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        { id: "dm-1", type: "1:1", name: "Juliane Lino", participants: [] },
        { id: "dm-grp", type: "group", name: "Equipe Infra", participants: [] },
      ],
    });
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /grupo equipe infra/i })).toBeInTheDocument();
    });
  });

  // BUG #395 — the sidebar payload never carries participants, so a group must
  // still show an avatar built from its own visible name.
  it("shows the group initials when the sidebar sends no participants", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [{ id: "dm-grp", type: "group", name: "Equipe Infra", participants: [] }],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: "Grupo Equipe Infra" });
    expect(option.textContent).toContain("EI");
  });

  it("shows a single initial for a one-word group name", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [{ id: "dm-grp", type: "group", name: "Infra", participants: [] }],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: "Grupo Infra" });
    expect(option.textContent).toContain("I");
  });

  it("ignores surrounding whitespace in a group name", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [{ id: "dm-grp", type: "group", name: "  Equipe Infra  ", participants: [] }],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: "Grupo Equipe Infra" });
    expect(option.textContent).toContain("EI");
  });

  it("keeps accented group initials legible", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [{ id: "dm-grp", type: "group", name: "Órgão Ágil", participants: [] }],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: "Grupo Órgão Ágil" });
    expect(option.textContent).toContain("ÓÁ");
  });

  it("falls back to a placeholder initial when a group has no usable name", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        { id: "dm-empty", type: "group", name: "", participants: [] },
        { id: "dm-blank", type: "group", name: "   ", participants: [] },
      ],
    });
    renderChat();

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
    for (const option of screen.getAllByRole("option")) {
      expect(option.textContent).toContain("?");
    }
  });

  it("keeps the group avatar decorative and the row labelled by the full name", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [{ id: "dm-grp", type: "group", name: "Equipe Infra", participants: [] }],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: "Grupo Equipe Infra" });
    const avatar = option.querySelector("[aria-hidden='true']");
    expect(avatar).not.toBeNull();
    expect(avatar?.textContent).toBe("EI");
    // The initials are hidden from the accessible name, so "EI" is not announced
    // on top of "Equipe Infra".
    expect(screen.queryByRole("option", { name: /EI/ })).toBeNull();
  });

  it("still composes participant avatars when a group carries participants", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: SAMPLE_DMS,
    });
    renderChat();

    const option = await screen.findByRole("option", { name: "Grupo Equipe Infra" });
    expect(option.textContent).toContain("JL");
    expect(option.textContent).toContain("CA");
  });

  it("shows the counterpart avatar in a 1:1 DM", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
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
      ],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    const image = option.querySelector("img");
    expect(image).not.toBeNull();
    expect(image).toHaveAttribute("src", "/media/avatars/juliane.png");
    // The name is on the button label, so the picture stays decorative.
    expect(image).toHaveAttribute("alt", "");
    // No image request may be issued through the API client.
    expect(mockFetchSidebarData).toHaveBeenCalledTimes(1);
  });

  it("falls back to initials when the counterpart has no avatar", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        {
          id: "dm-1",
          type: "1:1",
          name: "Juliane Lino",
          participants: [],
          counterpart: { userId: "user-2", displayName: "Juliane Lino" },
        },
      ],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    expect(option.querySelector("img")).toBeNull();
    expect(option.textContent).toContain("JL");
  });

  it("falls back to initials when the avatar image fails to load", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        {
          id: "dm-1",
          type: "1:1",
          name: "Juliane Lino",
          participants: [],
          counterpart: { userId: "user-2", displayName: "Juliane Lino", avatarUrl: "/gone.png" },
        },
      ],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    const image = option.querySelector("img");
    expect(image).not.toBeNull();
    fireEvent.error(image as HTMLImageElement);

    await waitFor(() => expect(option.querySelector("img")).toBeNull());
    expect(option.textContent).toContain("JL");
    // A broken image must never stay visible in the row.
    expect(screen.getByRole("option", { name: /mensagem direta com juliane lino/i })).toBeVisible();
  });

  it("still renders an initials avatar when the server sends no counterpart", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [{ id: "dm-1", type: "1:1", name: "Juliane Lino", participants: [] }],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    expect(option.textContent).toContain("JL");
  });

  it("gives two different DMs their own avatars", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        {
          id: "dm-1",
          type: "1:1",
          name: "Juliane Lino",
          participants: [],
          counterpart: { userId: "user-2", displayName: "Juliane Lino", avatarUrl: "/a.png" },
        },
        {
          id: "dm-2",
          type: "1:1",
          name: "Caio Almeida",
          participants: [],
          counterpart: { userId: "user-3", displayName: "Caio Almeida", avatarUrl: "/b.png" },
        },
      ],
    });
    renderChat();

    const first = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    const second = screen.getByRole("option", { name: /mensagem direta com caio almeida/i });
    expect(first.querySelector("img")).toHaveAttribute("src", "/a.png");
    expect(second.querySelector("img")).toHaveAttribute("src", "/b.png");
  });

  it("keeps each DM's avatar state independent when one image fails", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        {
          id: "dm-1",
          type: "1:1",
          name: "Juliane Lino",
          participants: [],
          counterpart: { userId: "user-2", displayName: "Juliane Lino", avatarUrl: "/a.png" },
        },
        {
          id: "dm-2",
          type: "1:1",
          name: "Caio Almeida",
          participants: [],
          counterpart: { userId: "user-3", displayName: "Caio Almeida", avatarUrl: "/b.png" },
        },
      ],
    });
    renderChat();

    const first = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    const second = screen.getByRole("option", { name: /mensagem direta com caio almeida/i });

    // Fail A's image only; B must keep showing its own picture.
    fireEvent.error(first.querySelector("img") as HTMLImageElement);
    await waitFor(() => expect(first.querySelector("img")).toBeNull());
    expect(first.textContent).toContain("JL");
    expect(second.querySelector("img")).toHaveAttribute("src", "/b.png");
  });

  it("sidebar A → B → A: a failed A is retried after the same slot shows B and returns to A", async () => {
    // Rendering ChatSidebar directly with rerender keeps the SAME Avatar
    // instance (keyed by dm.id) while its src changes /a.png → /b.png → /a.png,
    // which is the src-swap cycle the hook-driven harness cannot express.
    const readyState = (avatarUrl: string) => ({
      status: "ready" as const,
      currentUserId: "user-a",
      workspaceId: "workspace-1",
      channels: [] as Channel[],
      dms: [
        {
          id: "dm-1",
          type: "1:1" as const,
          name: "Juliane Lino",
          participants: [],
          counterpart: { userId: "user-2", displayName: "Juliane Lino", avatarUrl },
        },
      ] as DMConversation[],
      categories: [] as ChannelCategory[],
    });

    const tree = (avatarUrl: string) => (
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={readyState(avatarUrl)} retry={() => {}} />
      </MemoryRouter>
    );

    const { rerender } = render(tree("/a.png"));
    const dmOption = () =>
      screen.getByRole("option", { name: /mensagem direta com juliane lino/i });

    // A renders, then fails → initials.
    expect(dmOption().querySelector("img")).toHaveAttribute("src", "/a.png");
    fireEvent.error(dmOption().querySelector("img") as HTMLImageElement);
    await waitFor(() => expect(dmOption().querySelector("img")).toBeNull());
    expect(dmOption().textContent).toContain("JL");

    // The same slot switches to B → B renders.
    rerender(tree("/b.png"));
    await waitFor(() => expect(dmOption().querySelector("img")).toHaveAttribute("src", "/b.png"));

    // Back to A → A is tried again (not stuck on the earlier failure).
    rerender(tree("/a.png"));
    await waitFor(() => expect(dmOption().querySelector("img")).toHaveAttribute("src", "/a.png"));
  });

  it("stays on initials while the same failed avatar src is retried", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        {
          id: "dm-1",
          type: "1:1",
          name: "Juliane Lino",
          participants: [],
          counterpart: { userId: "user-2", displayName: "Juliane Lino", avatarUrl: "/a.png" },
        },
      ],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    const image = option.querySelector("img") as HTMLImageElement;
    fireEvent.error(image);
    await waitFor(() => expect(option.querySelector("img")).toBeNull());
    // A repeated error event on the same element must not resurrect the image.
    expect(option.textContent).toContain("JL");
  });

  it("keeps the accessible label on the full name, not on the avatar", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        {
          id: "dm-1",
          type: "1:1",
          name: "Juliane Lino",
          participants: [],
          counterpart: { userId: "user-2", displayName: "Juliane Lino", avatarUrl: "/a.png" },
        },
      ],
    });
    renderChat();

    const option = await screen.findByRole("option", { name: "Mensagem direta com Juliane Lino" });
    expect(option.querySelector("img")?.closest("[aria-hidden='true']")).not.toBeNull();
  });

  it("renders names with special characters as text, never as markup", async () => {
    const hostileName = '<img src=x onerror="alert(1)"> Ana & Bruno';
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [{ id: "dm-1", type: "1:1", name: hostileName, participants: [] }],
    });
    renderChat();

    const label = await screen.findByText(hostileName);
    // The name reaches the DOM as a text node; no markup was interpreted.
    expect(label.querySelector("img")).toBeNull();
    expect(label.textContent).toBe(hostileName);
  });

  it("still shows the generic label when the backend could not resolve a name", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [{ id: "dm-1", type: "1:1", name: "Mensagem Direta", participants: [] }],
    });
    renderChat();

    await waitFor(() => {
      expect(
        screen.getByRole("option", { name: /mensagem direta com mensagem direta/i }),
      ).toBeInTheDocument();
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

  // ── New-conversation trigger (ISSUE #387) ──
  // The action must read as "start a conversation" without the user having to
  // infer it from a bare "+", and must be distinct from channel creation.

  it("names the new-conversation trigger by its visible text", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "current-user",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    const trigger = await screen.findByRole("button", { name: "Nova conversa" });
    expect(trigger).toHaveTextContent("Nova conversa");
    expect(trigger).toHaveAttribute("aria-haspopup", "dialog");
    expect(screen.queryByRole("button", { name: "Nova mensagem direta" })).not.toBeInTheDocument();
  });

  it("opens the dialog from the keyboard and keeps both conversation modes", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "current-user",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    const trigger = await screen.findByRole("button", { name: "Nova conversa" });
    trigger.focus();
    expect(trigger).toHaveFocus();
    await user.keyboard("{Enter}");

    expect(screen.getByRole("dialog", { name: "Nova conversa" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Pessoa" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Grupo" })).toBeInTheDocument();
  });

  it("keeps the new-conversation trigger visible but disabled until the sidebar is ready", async () => {
    let resolveSidebar!: (value: {
      currentUserId: string;
      channels: Channel[];
      dms: DMConversation[];
    }) => void;
    mockFetchSidebarData.mockReturnValue(
      new Promise((resolve) => {
        resolveSidebar = resolve;
      }),
    );
    renderChat();

    // The action stays discoverable and keeps its accessible name while
    // loading — it is only unavailable, never hidden.
    const trigger = await screen.findByRole("button", { name: "Nova conversa" });
    expect(trigger).toBeInTheDocument();
    expect(trigger).toBeDisabled();
    fireEvent.click(trigger);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    resolveSidebar({ currentUserId: "current-user", channels: SAMPLE_CHANNELS, dms: [] });
    await waitFor(() => expect(trigger).toBeEnabled());
  });

  it("opens and closes the new-message dialog, restoring focus to its trigger", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "current-user",
      channels: SAMPLE_CHANNELS,
      dms: [],
    });
    renderChat();

    const trigger = await screen.findByRole("button", { name: "Nova conversa" });
    await user.click(trigger);
    expect(screen.getByRole("dialog", { name: "Nova conversa" })).toBeInTheDocument();
    expect(screen.getByRole("searchbox", { name: "Pesquisar pessoa" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Fechar nova conversa" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("opens the canonical DM, revalidates the sidebar and does not duplicate an existing item", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "current-user",
      channels: SAMPLE_CHANNELS,
      dms: SAMPLE_DMS,
    });
    mockSearchDMCandidates.mockResolvedValue([{ userId: "juliane", displayName: "Juliane Lino" }]);
    mockGetOrCreateDirectDM.mockResolvedValue({
      conversationId: "dm-juliane",
      created: false,
    });
    renderChat();

    await user.click(await screen.findByRole("button", { name: "Nova conversa" }));
    await user.type(screen.getByRole("searchbox"), "ju");
    await user.click(await screen.findByRole("button", { name: "Juliane Lino" }));

    await waitFor(() => expect(screen.getByTestId("chat-dm")).toBeInTheDocument());
    expect(mockGetOrCreateDirectDM).toHaveBeenCalledWith("juliane", expect.any(AbortSignal));
    expect(mockFetchSidebarData).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(
      screen.getAllByRole("option", { name: /mensagem direta com juliane lino/i }),
    ).toHaveLength(1);

    const trigger = screen.getByRole("button", { name: "Nova conversa" });
    expect(trigger).toHaveFocus();
    await user.click(trigger);
    expect(screen.getByRole("searchbox", { name: "Pesquisar pessoa" })).toHaveValue("");
  });

  it("shows the resolved participant name after the post-creation refresh", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData
      .mockResolvedValueOnce({ currentUserId: "current-user", channels: [], dms: [] })
      .mockResolvedValue({
        currentUserId: "current-user",
        channels: [],
        dms: [{ id: "dm-new", type: "1:1", name: "Juliane Lino", participants: [] }],
      });
    mockSearchDMCandidates.mockResolvedValue([{ userId: "juliane", displayName: "Juliane Lino" }]);
    mockGetOrCreateDirectDM.mockResolvedValue({ conversationId: "dm-new", created: true });
    renderChat();

    await user.click(await screen.findByRole("button", { name: "Nova conversa" }));
    await user.type(screen.getByRole("searchbox"), "ju");
    await user.click(await screen.findByRole("button", { name: "Juliane Lino" }));

    await waitFor(() => {
      expect(
        screen.getByRole("option", { name: /mensagem direta com juliane lino/i }),
      ).toBeInTheDocument();
    });
    expect(screen.queryByText("Mensagem Direta")).not.toBeInTheDocument();
  });
});

// ── Ad-hoc group creation (RF-02) ─────────────────────────────────────────────

const GROUP_CANDIDATES: DMCandidate[] = [
  { userId: "juliane", displayName: "Juliane Lino" },
  { userId: "caio", displayName: "Caio Almeida" },
];

const GROUP_DM: DMConversation = {
  id: "dm-group-new",
  type: "group",
  name: "Equipe Infra",
  participants: [],
};

async function openGroupModeAndSelectBoth(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "Nova conversa" }));
  await user.click(screen.getByRole("radio", { name: "Grupo" }));
  await user.type(screen.getByRole("searchbox"), "eq");
  await user.click(await screen.findByRole("button", { name: "Juliane Lino" }));
  await user.click(screen.getByRole("button", { name: "Caio Almeida" }));
}

describe("ChatSidebar — ad-hoc group creation", () => {
  it("creates a named group, opens it and lists it from the canonical sidebar source", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData
      .mockResolvedValueOnce({ currentUserId: "current-user", channels: [], dms: [] })
      .mockResolvedValue({ currentUserId: "current-user", channels: [], dms: [GROUP_DM] });
    mockSearchDMCandidates.mockResolvedValue(GROUP_CANDIDATES);
    mockCreateGroupDM.mockResolvedValue("dm-group-new");
    renderChat();

    await openGroupModeAndSelectBoth(user);
    await user.type(screen.getByLabelText("Nome do grupo (opcional)"), "  Equipe Infra  ");
    await user.click(screen.getByRole("button", { name: "Criar grupo" }));

    await waitFor(() => expect(screen.getByTestId("chat-dm")).toBeInTheDocument());
    // Exactly the contract: the other participants and the raw title. Workspace
    // and actor are never sent from the browser.
    expect(mockCreateGroupDM).toHaveBeenCalledTimes(1);
    expect(mockCreateGroupDM).toHaveBeenCalledWith(
      ["juliane", "caio"],
      "  Equipe Infra  ",
      expect.any(AbortSignal),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    // The sidebar is refetched, not patched by hand.
    expect(mockFetchSidebarData).toHaveBeenCalledTimes(2);
    expect(screen.getAllByRole("option", { name: "Grupo Equipe Infra" })).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Nova conversa" })).toHaveFocus();
  });

  it("does not duplicate a group the refreshed sidebar already contained", async () => {
    const user = userEvent.setup();
    // Same conversation present before and after creation — the equivalent of an
    // out-of-band update racing the HTTP response.
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "current-user",
      channels: [],
      dms: [GROUP_DM],
    });
    mockSearchDMCandidates.mockResolvedValue(GROUP_CANDIDATES);
    mockCreateGroupDM.mockResolvedValue("dm-group-new");
    renderChat();

    await openGroupModeAndSelectBoth(user);
    await user.click(screen.getByRole("button", { name: "Criar grupo" }));

    await waitFor(() => expect(screen.getByTestId("chat-dm")).toBeInTheDocument());
    expect(screen.getAllByRole("option", { name: "Grupo Equipe Infra" })).toHaveLength(1);
  });

  it("creates a group without a name and shows the server-side fallback label", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData
      .mockResolvedValueOnce({ currentUserId: "current-user", channels: [], dms: [] })
      .mockResolvedValue({
        currentUserId: "current-user",
        channels: [],
        dms: [{ ...GROUP_DM, name: "Grupo DM" }],
      });
    mockSearchDMCandidates.mockResolvedValue(GROUP_CANDIDATES);
    mockCreateGroupDM.mockResolvedValue("dm-group-new");
    renderChat();

    await openGroupModeAndSelectBoth(user);
    await user.click(screen.getByRole("button", { name: "Criar grupo" }));

    await waitFor(() => expect(screen.getByTestId("chat-dm")).toBeInTheDocument());
    expect(mockCreateGroupDM).toHaveBeenCalledWith(
      ["juliane", "caio"],
      "",
      expect.any(AbortSignal),
    );
    expect(await screen.findByRole("option", { name: "Grupo Grupo DM" })).toBeInTheDocument();
  });

  it("keeps the modal, the selection and the retry after a failed creation", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "current-user",
      channels: [],
      dms: [],
    });
    mockSearchDMCandidates.mockResolvedValue(GROUP_CANDIDATES);
    mockCreateGroupDM
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce("dm-group-new");
    renderChat();

    await openGroupModeAndSelectBoth(user);
    await user.click(screen.getByRole("button", { name: "Criar grupo" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Não foi possível criar o grupo");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Pessoas selecionadas" })).toHaveTextContent(
      "Juliane Lino",
    );
    expect(screen.queryByTestId("chat-dm")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Criar grupo" }));
    await waitFor(() => expect(screen.getByTestId("chat-dm")).toBeInTheDocument());
  });
});

// ── Section classification (ISSUE #396) ───────────────────────────────────────
// Canais, Mensagens diretas and Grupos are distinct product categories. Each
// item belongs to exactly one of them, decided by the canonical server-derived
// discriminator — never by the label, the avatar or the participant count.

describe("ChatSidebar — section classification", () => {
  const PUBLIC_CHANNEL: Channel = { id: "geral", name: "geral", type: "public", canWrite: true };
  const PRIVATE_CHANNEL: Channel = {
    id: "projetos",
    name: "projetos",
    type: "private",
    canWrite: true,
  };
  const DIRECT: DMConversation = {
    id: "dm-1",
    type: "1:1",
    name: "Juliane Lino",
    participants: [],
  };
  const GROUP: DMConversation = {
    id: "dm-grp",
    type: "group",
    name: "Equipe Infra",
    participants: [],
  };

  const mixedSidebar = () => ({
    currentUserId: "user-a",
    channels: [PUBLIC_CHANNEL, PRIVATE_CHANNEL],
    dms: [DIRECT, GROUP],
  });

  /** Each section is a landmark named by its own heading. */
  const section = (name: string) => screen.getByRole("region", { name });
  const optionNamesIn = (name: string) =>
    within(section(name))
      .queryAllByRole("option")
      .map((option) => option.getAttribute("aria-label"));

  it("renders the three sections as headings, in product order", async () => {
    mockFetchSidebarData.mockResolvedValue(mixedSidebar());
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    const headings = screen.getAllByRole("heading").map((h) => h.textContent);
    expect(headings).toEqual(["Canais", "Mensagens diretas", "Grupos"]);
    // Each section's list is labelled by its own heading, so a screen reader
    // never has to guess which category a row belongs to.
    expect(screen.getAllByRole("listbox").map((l) => l.getAttribute("aria-labelledby"))).toEqual([
      screen.getByRole("heading", { name: "Canais" }).id,
      screen.getByRole("heading", { name: "Mensagens diretas" }).id,
      screen.getByRole("heading", { name: "Grupos" }).id,
    ]);
  });

  it("keeps public and private channels in Canais only", async () => {
    mockFetchSidebarData.mockResolvedValue(mixedSidebar());
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    expect(optionNamesIn("Canais")).toEqual(["Canal geral", "Canal privado projetos"]);
    expect(optionNamesIn("Mensagens diretas")).not.toContain("Canal geral");
    expect(optionNamesIn("Grupos")).not.toContain("Canal privado projetos");
  });

  it("keeps a 1:1 conversation in Mensagens diretas only", async () => {
    mockFetchSidebarData.mockResolvedValue(mixedSidebar());
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    expect(optionNamesIn("Mensagens diretas")).toEqual(["Mensagem direta com Juliane Lino"]);
    expect(optionNamesIn("Grupos")).not.toContain("Mensagem direta com Juliane Lino");
  });

  it("keeps an ad-hoc group in Grupos only", async () => {
    mockFetchSidebarData.mockResolvedValue(mixedSidebar());
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    expect(optionNamesIn("Grupos")).toEqual(["Grupo Equipe Infra"]);
    expect(optionNamesIn("Mensagens diretas")).not.toContain("Grupo Equipe Infra");
  });

  it("lists every item exactly once across the whole sidebar", async () => {
    mockFetchSidebarData.mockResolvedValue(mixedSidebar());
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    const all = [
      ...optionNamesIn("Canais"),
      ...optionNamesIn("Mensagens diretas"),
      ...optionNamesIn("Grupos"),
    ];
    expect(all).toHaveLength(4);
    expect(new Set(all).size).toBe(4);
    // No row is duplicated in the accessibility tree either.
    expect(screen.getAllByRole("option")).toHaveLength(4);
  });

  it("classifies on the discriminator, not on the displayed name", async () => {
    // A group named like a person and a 1:1 named like a group: swapping them
    // would be the classic "read the title and guess" bug.
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [
        { id: "dm-1", type: "1:1", name: "Equipe Infra, Caio e Ana", participants: [] },
        { id: "dm-grp", type: "group", name: "Juliane Lino", participants: [] },
      ],
    });
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    expect(optionNamesIn("Mensagens diretas")).toEqual([
      "Mensagem direta com Equipe Infra, Caio e Ana",
    ]);
    expect(optionNamesIn("Grupos")).toEqual(["Grupo Juliane Lino"]);
  });

  it("gives each empty section its own message and never presents it as a failure", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "user-a", channels: [], dms: [] });
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    expect(within(section("Canais")).getByText("Nenhum canal disponível.")).toBeInTheDocument();
    expect(
      within(section("Mensagens diretas")).getByText("Nenhuma mensagem direta."),
    ).toBeInTheDocument();
    expect(within(section("Grupos")).getByText("Nenhum grupo.")).toBeInTheDocument();
    // An empty section is not an error and offers no retry.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /tentar novamente/i })).not.toBeInTheDocument();
  });

  it("shows an empty Grupos while Mensagens diretas has items, and vice versa", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [DIRECT],
    });
    const { unmount } = renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    expect(optionNamesIn("Mensagens diretas")).toHaveLength(1);
    expect(within(section("Grupos")).getByText("Nenhum grupo.")).toBeInTheDocument();
    unmount();

    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [GROUP],
    });
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    expect(optionNamesIn("Grupos")).toHaveLength(1);
    expect(
      within(section("Mensagens diretas")).getByText("Nenhuma mensagem direta."),
    ).toBeInTheDocument();
  });

  it("shows no section at all while loading — nothing lands in the wrong category", async () => {
    mockFetchSidebarData.mockReturnValue(new Promise(() => {}));
    renderChat();

    await screen.findByTestId("chat-sidebar");
    expect(screen.getByRole("status", { name: /carregando/i })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Grupos" })).not.toBeInTheDocument();
    expect(screen.queryAllByRole("option")).toHaveLength(0);
  });

  it("replaces the sections with a single retryable error, exposing no internals", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData
      .mockRejectedValueOnce(new Error("pq: relation chat.dm_conversations does not exist"))
      .mockResolvedValue(mixedSidebar());
    renderChat();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Não foi possível carregar os canais.");
    expect(alert).not.toHaveTextContent(/pq:|relation|dm_conversations/);
    expect(screen.queryByRole("heading", { name: "Grupos" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /tentar novamente/i }));
    await screen.findByRole("heading", { name: "Grupos" });
    expect(optionNamesIn("Grupos")).toEqual(["Grupo Equipe Infra"]);
  });

  it("keeps unread badges on their own item in each section", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [{ ...PUBLIC_CHANNEL, unreadCount: 3 }, PRIVATE_CHANNEL],
      dms: [
        { ...DIRECT, unreadCount: 7 },
        { ...GROUP, unreadCount: 2 },
      ],
    });
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    const badgeOf = (label: string) =>
      within(screen.getByRole("option", { name: label })).queryByLabelText(/não lidas/i)
        ?.textContent;

    expect(badgeOf("Canal geral")).toBe("3");
    expect(badgeOf("Mensagem direta com Juliane Lino")).toBe("7");
    expect(badgeOf("Grupo Equipe Infra")).toBe("2");
    // A channel with no unread count shows no badge at all.
    expect(badgeOf("Canal privado projetos")).toBeUndefined();
  });

  it("marks a mentioned conversation's badge distinctly, not by color alone", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [{ ...PUBLIC_CHANNEL, unreadCount: 3, hasMentionUnread: true }, PRIVATE_CHANNEL],
      dms: [{ ...DIRECT, unreadCount: 2 }],
    });
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    const badge = () =>
      within(screen.getByRole("option", { name: "Canal geral" })).getByLabelText(/não lidas/i);

    // A visible "@" mark (not aria-hidden text alone) plus the accessible
    // name spelling it out — two independent signals, neither is color.
    expect(badge().textContent).toBe("@3");
    expect(badge()).toHaveAccessibleName("3 não lidas, incluindo menção");
    expect(badge()).toHaveClass("chat-sidebar__unread-badge--mention");
  });

  it("does not mark a plain unread badge as a mention", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [{ ...PUBLIC_CHANNEL, unreadCount: 3 }, PRIVATE_CHANNEL],
      dms: [],
    });
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    const badge = within(screen.getByRole("option", { name: "Canal geral" })).getByLabelText(
      /não lidas/i,
    );

    expect(badge.textContent).toBe("3");
    expect(badge).toHaveAccessibleName("3 não lidas");
    expect(badge).not.toHaveClass("chat-sidebar__unread-badge--mention");
  });

  it("marks a mentioned DM's badge the same way as a mentioned channel", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-a",
      channels: [],
      dms: [{ ...DIRECT, unreadCount: 5, hasMentionUnread: true }],
    });
    renderChat();

    await screen.findByRole("heading", { name: "Canais" });
    const badge = within(
      screen.getByRole("option", { name: "Mensagem direta com Juliane Lino" }),
    ).getByLabelText(/não lidas/i);
    expect(badge.textContent).toBe("@5");
    expect(badge).toHaveAccessibleName("5 não lidas, incluindo menção");
  });

  it("reaches items of all three sections by keyboard, with focus never trapped", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue(mixedSidebar());
    renderChat();

    const trigger = await screen.findByRole("button", { name: "Nova conversa" });
    trigger.focus();

    // Since issue #527 the second stop on every row is the actions menu's
    // trigger, not a pin: the pin is state and no longer takes a tab stop.
    // Since issue #779 each section adds two stops of its own before its
    // rows: the collapse button, then the "show unread when collapsed" switch.
    const expected = [
      ["button", "Canais"],
      ["switch", "Mostrar mensagens não lidas quando Canais estiver recolhida"],
      ["option", "Canal geral"],
      ["button", "Mais opções para canal geral"],
      ["option", "Canal privado projetos"],
      ["button", "Mais opções para canal projetos"],
      ["button", "Mensagens diretas"],
      ["switch", "Mostrar mensagens não lidas quando Mensagens diretas estiver recolhida"],
      ["option", "Mensagem direta com Juliane Lino"],
      ["button", "Mais opções para conversa com Juliane Lino"],
      ["button", "Grupos"],
      ["switch", "Mostrar mensagens não lidas quando Grupos estiver recolhida"],
      ["option", "Grupo Equipe Infra"],
      ["button", "Mais opções para grupo Equipe Infra"],
    ] as const;
    for (const [role, label] of expected) {
      await user.tab();
      expect(screen.getByRole(role, { name: label })).toHaveFocus();
    }

    // Tab order continues past the last section into the footer.
    await user.tab();
    expect(screen.getByRole("option", { name: "Grupo Equipe Infra" })).not.toHaveFocus();
  });

  it("keeps the selected group selected across a refetch that rebuilds the list", async () => {
    // Fresh objects with the same ids — what a refetch actually produces.
    const stateWith = (dms: DMConversation[]) => ({
      status: "ready" as const,
      currentUserId: "user-a",
      workspaceId: "workspace-1",
      channels: [{ ...PUBLIC_CHANNEL }],
      dms,
      categories: [] as ChannelCategory[],
    });
    const tree = (dms: DMConversation[]) => (
      <MemoryRouter initialEntries={["/chat/dm/dm-grp"]}>
        <ChatSidebar state={stateWith(dms)} retry={() => {}} />
      </MemoryRouter>
    );

    const { rerender } = render(tree([{ ...DIRECT }, { ...GROUP }]));
    expect(screen.getByRole("option", { name: "Grupo Equipe Infra" })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    // Reordered and rebuilt: selection follows the id, not the position.
    rerender(tree([{ ...GROUP }, { ...DIRECT }, { ...DIRECT, id: "dm-2", name: "Caio Almeida" }]));
    expect(screen.getByRole("option", { name: "Grupo Equipe Infra" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(
      screen.getByRole("option", { name: "Mensagem direta com Juliane Lino" }),
    ).toHaveAttribute("aria-selected", "false");
    expect(optionNamesIn("Grupos")).toEqual(["Grupo Equipe Infra"]);
  });

  it("routes a selected group to the DM route, like any other conversation", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue(mixedSidebar());
    renderChat();

    await user.click(await screen.findByRole("option", { name: "Grupo Equipe Infra" }));

    await waitFor(() => expect(screen.getByTestId("chat-dm")).toBeInTheDocument());
    expect(screen.getByRole("option", { name: "Grupo Equipe Infra" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });
});

// ── Activity ordering (ISSUE #414) ───────────────────────────────────────────
// Three sections, three independent orders. These assert what is rendered, so
// the fixtures are deliberately given in the *wrong* order: an assertion that
// passed on input order would prove nothing.

describe("ChatSidebar — activity ordering", () => {
  const channel = (id: string, overrides: Partial<Channel> = {}): Channel => ({
    id,
    name: id,
    type: "public",
    canWrite: true,
    ...overrides,
  });
  const dm = (id: string, type: "1:1" | "group", overrides: Partial<DMConversation> = {}) => ({
    id,
    type,
    name: id,
    participants: [],
    ...overrides,
  });

  const section = (name: string) => screen.getByRole("region", { name });
  // The accessible name, not the text content: a DM row also renders avatar
  // initials, and this is about which rows are where, not how they look.
  const optionNamesIn = (name: string) =>
    within(section(name))
      .queryAllByRole("option")
      .map((option) => option.getAttribute("aria-label"));

  const readyState = (channels: Channel[], dms: DMConversation[]) => ({
    status: "ready" as const,
    currentUserId: "user-a",
    workspaceId: "workspace-1",
    channels,
    dms,
    categories: [] as ChannelCategory[],
  });

  const renderState = (
    channels: Channel[],
    dms: DMConversation[],
    path = "/chat",
    setPinned?: (
      target: { kind: "channel" | "dm"; targetId: string },
      pinned: boolean,
    ) => Promise<void>,
  ) =>
    render(
      <MemoryRouter initialEntries={[path]}>
        <ChatSidebar state={readyState(channels, dms)} retry={() => {}} setPinned={setPinned} />
      </MemoryRouter>,
    );

  it("orders channels by their own last message, newest first", () => {
    renderState(
      [
        channel("quieto", { lastMessageAt: "2026-07-28T08:00:00Z" }),
        channel("agitado", { lastMessageAt: "2026-07-30T18:00:00Z" }),
        channel("medio", { lastMessageAt: "2026-07-29T09:00:00Z" }),
      ],
      [],
    );

    expect(optionNamesIn("Canais")).toEqual(["Canal agitado", "Canal medio", "Canal quieto"]);
  });

  // Since issue #527 unpinning is a menu action, not a click on the pin. The
  // conversation must stay where it is: unpinning a channel is not a way to move
  // a group into "Canais".
  it("unpins from the menu and preserves the item's original category", async () => {
    const user = userEvent.setup();
    const setPinned = vi.fn().mockResolvedValue(undefined);
    renderState(
      [channel("geral", { pinnedAt: "2026-08-12T10:00:00Z", unreadCount: 3 })],
      [dm("projeto", "group")],
      "/chat/channel/geral",
      setPinned,
    );

    expect(
      within(section("Canais")).getByRole("option", { name: "Canal geral, fixado" }),
    ).toBeInTheDocument();
    expect(
      within(section("Grupos")).getByRole("option", { name: "Grupo projeto" }),
    ).toBeInTheDocument();

    await user.click(
      within(section("Canais")).getByRole("button", { name: "Mais opções para canal geral" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "Desafixar" }));
    expect(setPinned).toHaveBeenCalledWith({ kind: "channel", targetId: "geral" }, false);
    expect(
      within(section("Grupos")).getByRole("option", { name: "Grupo projeto" }),
    ).toBeInTheDocument();
  });

  it("orders direct messages independently of every other section", () => {
    renderState(
      [
        channel("canal-antigo", { lastMessageAt: "2020-01-01T00:00:00Z" }),
        channel("canal-novo", { lastMessageAt: "2026-08-01T00:00:00Z" }),
      ],
      [
        dm("dm-antiga", "1:1", { lastMessageAt: "2026-07-01T00:00:00Z" }),
        dm("dm-nova", "1:1", { lastMessageAt: "2026-07-31T00:00:00Z" }),
        dm("grupo-antigo", "group", { lastMessageAt: "2026-07-02T00:00:00Z" }),
        dm("grupo-novo", "group", { lastMessageAt: "2026-07-30T00:00:00Z" }),
      ],
    );

    expect(optionNamesIn("Canais")).toEqual(["Canal canal-novo", "Canal canal-antigo"]);
    expect(optionNamesIn("Mensagens diretas")).toEqual([
      "Mensagem direta com dm-nova",
      "Mensagem direta com dm-antiga",
    ]);
    expect(optionNamesIn("Grupos")).toEqual(["Grupo grupo-novo", "Grupo grupo-antigo"]);
  });

  it("keeps conversations without messages after every active one", () => {
    // The empty conversations were created *after* both active ones were last
    // written in, and must still sit behind them.
    renderState(
      [
        channel("vazio-recente", { createdAt: "2026-08-02T00:00:00Z" }),
        channel("ativo-antigo", {
          createdAt: "2020-01-01T00:00:00Z",
          lastMessageAt: "2024-05-05T00:00:00Z",
        }),
        channel("vazio-antigo", { createdAt: "2026-08-01T00:00:00Z" }),
        channel("ativo-recente", {
          createdAt: "2020-01-01T00:00:00Z",
          lastMessageAt: "2026-07-30T00:00:00Z",
        }),
      ],
      [],
    );

    expect(optionNamesIn("Canais")).toEqual([
      "Canal ativo-recente",
      "Canal ativo-antigo",
      "Canal vazio-recente",
      "Canal vazio-antigo",
    ]);
  });

  // Two messages written inside the same millisecond are two different
  // instants; the rendered order has to follow the microseconds and not fall
  // through to the name/id tie-breakers.
  it("orders by the microseconds when two messages share a millisecond", () => {
    const { rerender } = renderState(
      [
        // Named so that an alphabetical tie-break would put "anterior" first.
        channel("anterior", { lastMessageAt: "2026-08-04T12:00:00.900045Z" }),
        channel("posterior", { lastMessageAt: "2026-08-04T12:00:00.900123Z" }),
      ],
      [
        dm("dm-1", "1:1", { name: "Juliane", lastMessageAt: "2026-08-04T12:00:00.900045Z" }),
        dm("dm-2", "1:1", { name: "Caio", lastMessageAt: "2026-08-04T12:00:00.900123Z" }),
      ],
      "/chat/dm/dm-1",
    );

    expect(optionNamesIn("Canais")).toEqual(["Canal posterior", "Canal anterior"]);
    expect(optionNamesIn("Mensagens diretas")).toEqual([
      "Mensagem direta com Caio",
      "Mensagem direta com Juliane",
    ]);
    // Sections stay independent, and the open conversation stays selected.
    expect(optionNamesIn("Grupos")).toEqual([]);
    expect(screen.getByRole("option", { name: "Mensagem direta com Juliane" })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    // A message 78µs newer than the leader promotes the selected conversation.
    rerender(
      <MemoryRouter initialEntries={["/chat/dm/dm-1"]}>
        <ChatSidebar
          state={readyState(
            [
              channel("anterior", { lastMessageAt: "2026-08-04T12:00:00.900045Z" }),
              channel("posterior", { lastMessageAt: "2026-08-04T12:00:00.900123Z" }),
            ],
            [
              dm("dm-1", "1:1", { name: "Juliane", lastMessageAt: "2026-08-04T12:00:00.900201Z" }),
              dm("dm-2", "1:1", { name: "Caio", lastMessageAt: "2026-08-04T12:00:00.900123Z" }),
            ],
          )}
          retry={() => {}}
        />
      </MemoryRouter>,
    );

    expect(optionNamesIn("Mensagens diretas")).toEqual([
      "Mensagem direta com Juliane",
      "Mensagem direta com Caio",
    ]);
    // The channels did not move: a DM event is a DM event.
    expect(optionNamesIn("Canais")).toEqual(["Canal posterior", "Canal anterior"]);
    expect(screen.getByRole("option", { name: "Mensagem direta com Juliane" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("breaks ties by name and then by id", () => {
    const sameInstant = "2026-07-30T10:00:00Z";
    renderState(
      [
        channel("id-z", { name: "Zeta", lastMessageAt: sameInstant }),
        channel("id-b", { name: "alfa", lastMessageAt: sameInstant }),
        channel("id-a", { name: "Alfa", lastMessageAt: sameInstant }),
      ],
      [],
    );

    expect(optionNamesIn("Canais")).toEqual(["Canal Alfa", "Canal alfa", "Canal Zeta"]);
  });

  it("keeps the open conversation selected when its section is reordered", () => {
    const before = [
      dm("dm-1", "1:1", { name: "Juliane", lastMessageAt: "2026-07-30T10:00:00Z" }),
      dm("dm-2", "1:1", { name: "Caio", lastMessageAt: "2026-07-29T10:00:00Z" }),
    ];
    const { rerender } = renderState([], before, "/chat/dm/dm-2");

    expect(optionNamesIn("Mensagens diretas")).toEqual([
      "Mensagem direta com Juliane",
      "Mensagem direta com Caio",
    ]);
    const selected = screen.getByRole("option", { name: "Mensagem direta com Caio" });
    expect(selected).toHaveAttribute("aria-selected", "true");
    // Focus survives the move because rows are keyed by conversation id.
    selected.focus();

    rerender(
      <MemoryRouter initialEntries={["/chat/dm/dm-2"]}>
        <ChatSidebar
          state={readyState(
            [],
            [
              dm("dm-1", "1:1", { name: "Juliane", lastMessageAt: "2026-07-30T10:00:00Z" }),
              dm("dm-2", "1:1", { name: "Caio", lastMessageAt: "2026-07-31T10:00:00Z" }),
            ],
          )}
          retry={() => {}}
        />
      </MemoryRouter>,
    );

    expect(optionNamesIn("Mensagens diretas")).toEqual([
      "Mensagem direta com Caio",
      "Mensagem direta com Juliane",
    ]);
    const promoted = screen.getByRole("option", { name: "Mensagem direta com Caio" });
    expect(promoted).toHaveAttribute("aria-selected", "true");
    expect(promoted).toHaveFocus();
  });

  it("does not reorder the arrays it was given", () => {
    const channels = [
      channel("b", { lastMessageAt: "2026-07-01T00:00:00Z" }),
      channel("a", { lastMessageAt: "2026-07-02T00:00:00Z" }),
    ];
    const dms = [
      dm("dm-b", "1:1", { lastMessageAt: "2026-07-01T00:00:00Z" }),
      dm("dm-a", "1:1", { lastMessageAt: "2026-07-02T00:00:00Z" }),
    ];
    renderState(channels, dms);

    expect(channels.map((item) => item.id)).toEqual(["b", "a"]);
    expect(dms.map((item) => item.id)).toEqual(["dm-b", "dm-a"]);
  });

  it("keeps keyboard navigation walking each section in rendered order", async () => {
    const user = userEvent.setup();
    renderState(
      [
        channel("canal-antigo", { lastMessageAt: "2026-07-01T00:00:00Z" }),
        channel("canal-novo", { lastMessageAt: "2026-07-31T00:00:00Z" }),
      ],
      [dm("dm-1", "1:1", { name: "Juliane", lastMessageAt: "2026-07-02T00:00:00Z" })],
    );

    const trigger = screen.getByRole("button", { name: "Nova conversa" });
    trigger.focus();

    for (const [role, label] of [
      ["button", "Canais"],
      ["switch", "Mostrar mensagens não lidas quando Canais estiver recolhida"],
      ["option", "Canal canal-novo"],
      ["button", "Mais opções para canal canal-novo"],
      ["option", "Canal canal-antigo"],
      ["button", "Mais opções para canal canal-antigo"],
      ["button", "Mensagens diretas"],
      ["switch", "Mostrar mensagens não lidas quando Mensagens diretas estiver recolhida"],
      ["option", "Mensagem direta com Juliane"],
    ] as const) {
      await user.tab();
      expect(screen.getByRole(role, { name: label })).toHaveFocus();
    }
  });
});

// ── Post-creation placement (ISSUE #396) ──────────────────────────────────────
// Each creation mode lands in its own section, and it lands there because the
// canonical refetch put it there — never because the UI kept a parallel list.

describe("ChatSidebar — post-creation placement", () => {
  const sectionOptions = (name: string) =>
    within(screen.getByRole("region", { name }))
      .queryAllByRole("option")
      .map((option) => option.getAttribute("aria-label"));

  it("puts a newly created person in Mensagens diretas", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData
      .mockResolvedValueOnce({ currentUserId: "current-user", channels: [], dms: [] })
      .mockResolvedValue({
        currentUserId: "current-user",
        channels: [],
        dms: [{ id: "dm-new", type: "1:1", name: "Juliane Lino", participants: [] }],
      });
    mockSearchDMCandidates.mockResolvedValue([{ userId: "juliane", displayName: "Juliane Lino" }]);
    mockGetOrCreateDirectDM.mockResolvedValue({ conversationId: "dm-new", created: true });
    renderChat();

    await user.click(await screen.findByRole("button", { name: "Nova conversa" }));
    await user.type(screen.getByRole("searchbox"), "ju");
    await user.click(await screen.findByRole("button", { name: "Juliane Lino" }));

    await waitFor(() =>
      expect(sectionOptions("Mensagens diretas")).toEqual(["Mensagem direta com Juliane Lino"]),
    );
    expect(sectionOptions("Grupos")).toEqual([]);
    expect(sectionOptions("Canais")).toEqual([]);
  });

  it("puts a newly created group in Grupos", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData
      .mockResolvedValueOnce({ currentUserId: "current-user", channels: [], dms: [] })
      .mockResolvedValue({
        currentUserId: "current-user",
        channels: [],
        dms: [{ id: "dm-group-new", type: "group", name: "Equipe Infra", participants: [] }],
      });
    mockSearchDMCandidates.mockResolvedValue(GROUP_CANDIDATES);
    mockCreateGroupDM.mockResolvedValue("dm-group-new");
    renderChat();

    await openGroupModeAndSelectBoth(user);
    await user.click(screen.getByRole("button", { name: "Criar grupo" }));

    await waitFor(() => expect(sectionOptions("Grupos")).toEqual(["Grupo Equipe Infra"]));
    expect(sectionOptions("Mensagens diretas")).toEqual([]);
    expect(sectionOptions("Canais")).toEqual([]);
  });

  it("puts a newly created channel in Canais", async () => {
    const user = userEvent.setup();
    const created: Channel = { id: "novo", name: "Novo", type: "public", canWrite: true };
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "current-user",
      channels: [],
      dms: [],
    });
    mockCreateChannel.mockResolvedValue(created);
    renderChat();

    await user.click(await screen.findByRole("button", { name: "Nova conversa" }));
    await user.click(screen.getByRole("radio", { name: "Canal" }));
    fireEvent.change(screen.getByLabelText(/nome do canal/i), { target: { value: "Novo" } });
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "current-user",
      channels: [created],
      dms: [],
    });
    await user.click(screen.getByRole("button", { name: "Criar canal" }));

    await waitFor(() => expect(sectionOptions("Canais")).toEqual(["Canal Novo"]));
    expect(sectionOptions("Mensagens diretas")).toEqual([]);
    expect(sectionOptions("Grupos")).toEqual([]);
  });

  // BUG #393 regression: the legacy can_create_channel contract is server-side
  // and the UI never gates on it. Splitting the sections must not reintroduce a
  // client-side permission decision.
  it("keeps all three creation modes available regardless of the legacy flag", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-1",
      channels: SAMPLE_CHANNELS,
      dms: SAMPLE_DMS,
    });
    renderChat();

    await user.click(await screen.findByRole("button", { name: "Nova conversa" }));
    const dialog = screen.getByRole("dialog", { name: "Nova conversa" });
    for (const mode of ["Pessoa", "Grupo", "Canal"]) {
      expect(within(dialog).getByRole("radio", { name: mode })).toBeEnabled();
    }
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

  it("shows generic error message when rejection is not an Error instance", async () => {
    // Covers the `err instanceof Error ? ... : "Não foi possível carregar os dados."` branch
    // in useChatSidebar when the caught value is not an Error object.
    mockFetchSidebarData.mockRejectedValue("string rejection");
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
    });

    expect(screen.getByText(/não foi possível carregar os canais/i)).toBeInTheDocument();
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
            element={
              <RequireAuth>
                <CallSessionProvider>
                  <AppShell />
                </CallSessionProvider>
              </RequireAuth>
            }
          >
            <Route path="/chat" element={<ChatShell />}>
              <Route index element={<div>default</div>} />
              <Route path="channel/:id" element={<div>channel</div>} />
              <Route path="dm/:id" element={<div>dm</div>} />
            </Route>
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
  // The footer's identity is the session's, so it does not depend on the
  // conversation lists: rendering the sidebar directly keeps each assertion
  // about the footer alone.
  function renderFooter() {
    return render(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar
          state={{
            status: "ready",
            currentUserId: "user-a",
            workspaceId: "workspace-1",
            channels: [],
            dms: [],
            categories: [],
          }}
          retry={() => {}}
        />
      </MemoryRouter>,
    );
  }

  const userLink = () => screen.getByRole("link", { name: /meu perfil/i });
  const avatarText = () => userLink().querySelector(".chat-sidebar__avatar")?.textContent;

  it("shows no invented identity while the profile is loading", async () => {
    // A request that never settles: the loading state stays observable.
    mockFetchMyProfile.mockReturnValue(new Promise<SelfProfile>(() => {}));
    renderFooter();

    const placeholder = await screen.findByTestId("chat-sidebar-user-placeholder");
    expect(placeholder).toHaveAttribute("data-state", "loading");
    const sidebar = screen.getByTestId("chat-sidebar");
    expect(sidebar).not.toHaveTextContent("Usuário");
    expect(sidebar).not.toHaveTextContent("?");
  });

  it("shows the authenticated user's real display name once loaded", async () => {
    mockFetchMyProfile.mockResolvedValue({ id: "user-a", displayName: "Ana Souza" });
    renderFooter();

    expect(await screen.findByText("Ana Souza")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-sidebar-user-placeholder")).not.toBeInTheDocument();
  });

  it("renders the avatar image when the profile carries one", async () => {
    mockFetchMyProfile.mockResolvedValue({
      id: "user-a",
      displayName: "Ana Souza",
      avatarUrl: "/api/auth/avatars/a.png",
    });
    renderFooter();

    await screen.findByText("Ana Souza");
    const img = userLink().querySelector("img");
    expect(img).toHaveAttribute("src", "/api/auth/avatars/a.png");
    expect(img).toHaveAttribute("referrerpolicy", "no-referrer");
  });

  it("falls back to initials when the profile has no avatar", async () => {
    mockFetchMyProfile.mockResolvedValue({ id: "user-a", displayName: "Ana Souza" });
    renderFooter();

    await screen.findByText("Ana Souza");
    expect(userLink().querySelector("img")).toBeNull();
    expect(avatarText()).toBe("AS");
  });

  it("falls back to initials when the avatar image fails to load", async () => {
    mockFetchMyProfile.mockResolvedValue({
      id: "user-a",
      displayName: "Ana Souza",
      avatarUrl: "/api/auth/avatars/broken.png",
    });
    renderFooter();

    await screen.findByText("Ana Souza");
    const img = userLink().querySelector("img") as HTMLImageElement;
    fireEvent.error(img);

    expect(userLink().querySelector("img")).toBeNull();
    expect(avatarText()).toBe("AS");
  });

  it("tries a new avatar URL after a previous one failed", async () => {
    mockFetchMyProfile.mockResolvedValue({
      id: "user-a",
      displayName: "Ana Souza",
      avatarUrl: "/api/auth/avatars/broken.png",
    });
    renderFooter();

    await screen.findByText("Ana Souza");
    fireEvent.error(userLink().querySelector("img") as HTMLImageElement);
    expect(userLink().querySelector("img")).toBeNull();

    // A confirmed profile change publishes the new URL; the earlier failure is
    // scoped to the URL it happened on and must not suppress this one.
    mockFetchMyProfile.mockResolvedValue({
      id: "user-a",
      displayName: "Ana Souza",
      avatarUrl: "/api/auth/avatars/new.png",
    });
    act(() => refreshSelfProfile());

    await waitFor(() =>
      expect(userLink().querySelector("img")).toHaveAttribute("src", "/api/auth/avatars/new.png"),
    );
  });

  it("drops to initials when a confirmed removal leaves no avatar", async () => {
    mockFetchMyProfile.mockResolvedValue({
      id: "user-a",
      displayName: "Ana Souza",
      avatarUrl: "/api/auth/avatars/a.png",
    });
    renderFooter();

    await waitFor(() => expect(userLink().querySelector("img")).toBeInTheDocument());

    mockFetchMyProfile.mockResolvedValue({ id: "user-a", displayName: "Ana Souza" });
    act(() => refreshSelfProfile());

    await waitFor(() => expect(userLink().querySelector("img")).toBeNull());
    expect(avatarText()).toBe("AS");
  });

  it.each([
    ["Ana", "A"],
    ["Ana Souza", "AS"],
    ["Ana   Maria   Souza", "AM"],
    ["Édson Ávila", "ÉÁ"],
    ["ana souza", "AS"],
  ])("derives at most two initials from %s", async (displayName, expected) => {
    mockFetchMyProfile.mockResolvedValue({ id: "user-a", displayName });
    renderFooter();

    await waitFor(() =>
      expect(screen.queryByTestId("chat-sidebar-user-placeholder")).not.toBeInTheDocument(),
    );
    expect(avatarText()).toBe(expected);
  });

  it("keeps the footer structure with a very long name", async () => {
    const longName = "Maria Aparecida de Souza Fernandes do Nascimento Albuquerque Filha";
    mockFetchMyProfile.mockResolvedValue({ id: "user-a", displayName: longName });
    renderFooter();

    // The name is one clipped element (CSS ellipsis), and the account menu
    // trigger remains a sibling of the profile link rather than being pushed
    // out of it.
    const name = await screen.findByText(longName);
    expect(name).toHaveClass("chat-sidebar__user-name");
    expect(avatarText()).toBe("MA");
    expect(screen.getByRole("button", { name: /menu da conta/i })).toBeInTheDocument();
  });

  it("keeps the account menu trigger as a sibling of the profile link", async () => {
    renderFooter();

    const trigger = await screen.findByRole("button", { name: /menu da conta/i });
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
    // Interactive elements must not nest: the trigger is a sibling of the
    // profile link, never inside it.
    expect(userLink().contains(trigger)).toBe(false);
  });

  it("links to the global search page (RF-15)", () => {
    renderFooter();

    const search = screen.getByRole("link", { name: "Buscar" });
    expect(search).toHaveAttribute("href", "/chat/search");
  });

  it("keeps the profile link reachable", async () => {
    renderFooter();

    await screen.findByText("Ana Souza");
    expect(userLink()).toHaveAttribute("href", "/profile");
  });

  it("reaches the profile link and account menu trigger by keyboard", async () => {
    const user = userEvent.setup();
    renderFooter();

    await screen.findByText("Ana Souza");
    const trigger = screen.getByRole("button", { name: /menu da conta/i });
    userLink().focus();
    expect(userLink()).toHaveFocus();
    await user.tab();
    expect(trigger).toHaveFocus();
  });

  it("does not invent an identity when the profile fails to load", async () => {
    mockFetchMyProfile.mockRejectedValue(new ApiRequestError(500, "internal_error", "boom"));
    renderFooter();

    const placeholder = await screen.findByTestId("chat-sidebar-user-placeholder");
    // An error is its own state — not "Usuário", not "?", not "no avatar".
    await waitFor(() => expect(placeholder).toHaveAttribute("data-state", "error"));
    const sidebar = screen.getByTestId("chat-sidebar");
    expect(sidebar).not.toHaveTextContent("Usuário");
    expect(sidebar).not.toHaveTextContent("?");
  });

  it("never renders the previous session's profile after a session switch", async () => {
    setTokens("token-a");
    mockFetchMyProfile.mockResolvedValue({ id: "user-a", displayName: "Ana Souza" });
    renderFooter();
    await screen.findByText("Ana Souza");

    // Session B: A's name must be gone in the same commit as the switch, before
    // B's profile has even been requested.
    mockFetchMyProfile.mockReturnValue(new Promise<SelfProfile>(() => {}));
    act(() => setTokens("token-b"));
    expect(screen.queryByText("Ana Souza")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-sidebar-user-placeholder")).toHaveAttribute(
      "data-state",
      "loading",
    );

    mockFetchMyProfile.mockResolvedValue({ id: "user-b", displayName: "Bruno Lima" });
    act(() => refreshSelfProfile());
    expect(await screen.findByText("Bruno Lima")).toBeInTheDocument();
    expect(screen.queryByText("Ana Souza")).not.toBeInTheDocument();
  });

  it("shows a neutral label instead of '?' when the server has no usable name", async () => {
    mockFetchMyProfile.mockResolvedValue({ id: "user-a", displayName: "" });
    renderFooter();

    await waitFor(() =>
      expect(screen.queryByTestId("chat-sidebar-user-placeholder")).not.toBeInTheDocument(),
    );
    expect(avatarText()).toBe("");
    expect(screen.getByTestId("chat-sidebar")).not.toHaveTextContent("Usuário");
  });

  it("footer does not render fixture user identity", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "", channels: [], dms: [] });
    renderChat();

    await screen.findByTestId("chat-sidebar");
    // The sidebar must never show hardcoded personal data.
    expect(screen.getByTestId("chat-sidebar")).not.toHaveTextContent("Álvaro Neto");
  });

  it("leaves the new-conversation trigger untouched", async () => {
    mockFetchSidebarData.mockResolvedValue({ currentUserId: "user-a", channels: [], dms: [] });
    renderChat();

    const trigger = await screen.findByRole("button", { name: "Nova conversa" });
    await waitFor(() => expect(trigger).toBeEnabled());
    expect(trigger).toHaveAttribute("aria-haspopup", "dialog");
  });
});

// ── Route encoding ────────────────────────────────────────────────────────────

describe("ChatSidebar — route encoding", () => {
  it("navigates with encoded channel ID containing special chars", async () => {
    const user = userEvent.setup();
    const channelWithSpace: Channel = {
      id: "equipe infra",
      name: "equipe infra",
      type: "public",
      canWrite: true,
    };
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
    const channelWithSpace: Channel = {
      id: "equipe infra",
      name: "equipe infra",
      type: "public",
      canWrite: true,
    };
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

// ── Single creation entry point (BUG #393) ───────────────────────────────────
// The sidebar offers one action, and it is available to every member the
// sidebar loaded for — there is no role in this decision, on this side or in
// what is sent to the server.

describe("ChatSidebar — single creation entry point", () => {
  const readySidebar = () => ({
    currentUserId: "user-1",
    channels: SAMPLE_CHANNELS,
    dms: SAMPLE_DMS,
  });

  it("offers only 'Nova conversa' — no separate channel controls, no admin-only notice", async () => {
    mockFetchSidebarData.mockResolvedValue(readySidebar());
    renderChat();

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Nova conversa" })).toBeEnabled(),
    );
    expect(screen.queryByRole("button", { name: "Novo canal" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Adicionar canal" })).not.toBeInTheDocument();
    expect(screen.queryByText(/somente administradores/i)).not.toBeInTheDocument();
    // Exactly one control opens a creation dialog.
    expect(screen.getAllByRole("button", { name: /nova conversa/i })).toHaveLength(1);
  });

  it("lets any loaded member reach the channel option from the keyboard", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue(readySidebar());
    renderChat();

    const trigger = await screen.findByRole("button", { name: "Nova conversa" });
    trigger.focus();
    await user.keyboard("{Enter}");

    const dialog = screen.getByRole("dialog", { name: "Nova conversa" });
    expect(within(dialog).getByRole("radio", { name: "Pessoa" })).toBeEnabled();
    expect(within(dialog).getByRole("radio", { name: "Grupo" })).toBeEnabled();
    const channelOption = within(dialog).getByRole("radio", { name: "Canal" });
    expect(channelOption).toBeEnabled();

    await user.click(channelOption);
    expect(within(dialog).getByLabelText(/nome do canal/i)).toBeInTheDocument();
  });

  it("creates a channel, opens it, refetches and lists it under Canais only", async () => {
    const user = userEvent.setup();
    const created: Channel = { id: "novo", name: "Novo", type: "public", canWrite: true };
    mockFetchSidebarData.mockResolvedValue(readySidebar());
    mockCreateChannel.mockResolvedValue(created);
    renderChat();

    await user.click(await screen.findByRole("button", { name: "Nova conversa" }));
    await user.click(screen.getByRole("radio", { name: "Canal" }));
    const loadsBefore = mockFetchSidebarData.mock.calls.length;

    fireEvent.change(screen.getByLabelText(/nome do canal/i), { target: { value: "Novo" } });
    // What the user ends up seeing comes from the refetch, never from the
    // creation response.
    mockFetchSidebarData.mockResolvedValue({
      ...readySidebar(),
      channels: [...SAMPLE_CHANNELS, created],
    });
    await user.click(screen.getByRole("button", { name: "Criar canal" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
    expect(mockFetchSidebarData.mock.calls.length).toBeGreaterThan(loadsBefore);
    expect(await screen.findByTestId("chat-channel")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "Canal Novo" })).toBeInTheDocument(),
    );
    // The new channel never lands among the direct messages.
    expect(screen.queryByRole("option", { name: /mensagem direta com novo/i })).toBeNull();
    expect(screen.queryByRole("option", { name: "Grupo Novo" })).toBeNull();
    expect(screen.getByRole("button", { name: "Nova conversa" })).toHaveFocus();
  });

  it("keeps the dialog open and shows the denial when the server refuses", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue(readySidebar());
    mockCreateChannel.mockRejectedValue(new ApiRequestError(403, "forbidden", "forbidden"));
    renderChat();

    await user.click(await screen.findByRole("button", { name: "Nova conversa" }));
    await user.click(screen.getByRole("radio", { name: "Canal" }));
    fireEvent.change(screen.getByLabelText(/nome do canal/i), { target: { value: "Novo" } });
    await user.click(screen.getByRole("button", { name: "Criar canal" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/permissão/i);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-channel")).not.toBeInTheDocument();
  });

  it("closes the dialog on Escape and restores focus to the trigger", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue(readySidebar());
    renderChat();

    const trigger = await screen.findByRole("button", { name: "Nova conversa" });
    await user.click(trigger);
    await user.click(screen.getByRole("radio", { name: "Canal" }));
    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });
});

describe("ChatSidebar — collapsible categories", () => {
  it("renders grouped channels under their category headers", async () => {
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-1",
      channels: [
        {
          id: "ch-1",
          name: "canal-1",
          type: "public",
          canWrite: true,
          categoryId: "cat-proj",
          categoryName: "Projetos",
        },
        {
          id: "ch-2",
          name: "canal-2",
          type: "public",
          canWrite: true,
          categoryId: "cat-infra",
          categoryName: "Infra",
        },
      ],
      dms: [],
      categories: [
        { id: "cat-proj", name: "Projetos", kind: "category" },
        { id: "cat-infra", name: "Infra", kind: "category" },
      ],
    });
    renderChat();

    // Verify category headers are rendered
    expect(await screen.findByRole("button", { name: /Projetos/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Infra/i })).toBeInTheDocument();

    // Verify channels are rendered under their headers
    expect(screen.getByRole("option", { name: /canal-1/i })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /canal-2/i })).toBeInTheDocument();
  });

  it("collapses and expands categories when clicked", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue({
      currentUserId: "user-1",
      channels: [
        {
          id: "ch-1",
          name: "canal-1",
          type: "public",
          canWrite: true,
          categoryId: "cat-proj",
          categoryName: "Projetos",
        },
      ],
      dms: [],
      categories: [{ id: "cat-proj", name: "Projetos", kind: "category" }],
    });
    renderChat();

    // Category button should be expanded by default
    const headerBtn = await screen.findByRole("button", { name: /Projetos/i });
    expect(headerBtn).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("option", { name: /canal-1/i })).toBeInTheDocument();

    // Click to collapse
    await user.click(headerBtn);
    expect(headerBtn).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("option", { name: /canal-1/i })).not.toBeInTheDocument();

    // Click to expand again
    await user.click(headerBtn);
    expect(headerBtn).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("option", { name: /canal-1/i })).toBeInTheDocument();
  });
});

// ── Collapsible sections + "show unread when collapsed" (ISSUE #779) ────────
//
// Each of the three product sections owns two independent controls: a
// collapse/expand toggle and a "show unread when collapsed" preference. The
// preference is never a filter while expanded — only while collapsed does it
// decide between showing nothing and showing only conversations with unread.

describe("ChatSidebar — collapsible sections with unread filter", () => {
  const channel = (id: string, overrides: Partial<Channel> = {}): Channel => ({
    id,
    name: id,
    type: "public",
    canWrite: true,
    ...overrides,
  });
  const dm = (
    id: string,
    type: "1:1" | "group",
    overrides: Partial<DMConversation> = {},
  ): DMConversation => ({
    id,
    type,
    name: id,
    participants: [],
    ...overrides,
  });

  const readyState = (
    channels: Channel[],
    dms: DMConversation[],
    categories: ChannelCategory[] = [],
  ) => ({
    status: "ready" as const,
    currentUserId: "user-a",
    workspaceId: "workspace-1",
    channels,
    dms,
    categories,
  });

  function renderState(
    channels: Channel[],
    dms: DMConversation[],
    categories: ChannelCategory[] = [],
    path = "/chat",
  ) {
    return render(
      <MemoryRouter initialEntries={[path]}>
        <ChatSidebar state={readyState(channels, dms, categories)} retry={() => {}} />
      </MemoryRouter>,
    );
  }

  const section = (name: string) => screen.getByRole("region", { name });
  const optionNamesIn = (name: string) =>
    within(section(name))
      .queryAllByRole("option")
      .map((option) => option.getAttribute("aria-label"));
  const collapseButton = (title: string) => screen.getByRole("button", { name: title });
  const unreadSwitch = (title: string) =>
    screen.getByRole("switch", {
      name: `Mostrar mensagens não lidas quando ${title} estiver recolhida`,
    });

  afterEach(() => {
    localStorage.clear();
  });

  it.each([
    {
      title: "Canais",
      build: () => ({ channels: [channel("a"), channel("b", { unreadCount: 2 })], dms: [] }),
    },
    {
      title: "Mensagens diretas",
      build: () => ({
        channels: [],
        dms: [dm("a", "1:1"), dm("b", "1:1", { unreadCount: 2 })],
      }),
    },
    {
      title: "Grupos",
      build: () => ({
        channels: [],
        dms: [dm("a", "group"), dm("b", "group", { unreadCount: 2 })],
      }),
    },
  ])("$title — walks the full expand/collapse × unread-only matrix", async ({ title, build }) => {
    const user = userEvent.setup();
    const { channels, dms } = build();
    renderState(channels, dms);
    const allNames =
      title === "Canais"
        ? ["Canal a", "Canal b"]
        : title === "Mensagens diretas"
          ? ["Mensagem direta com a", "Mensagem direta com b"]
          : ["Grupo a", "Grupo b"];
    const unreadOnlyName = allNames[1];

    // Expandida + unread off (default) = todos.
    expect(collapseButton(title)).toHaveAttribute("aria-expanded", "true");
    expect(unreadSwitch(title)).toHaveAttribute("aria-checked", "false");
    expect(optionNamesIn(title)).toEqual(allNames);

    // Expandida + unread on = todos (não é filtro quando expandida).
    await user.click(unreadSwitch(title));
    expect(unreadSwitch(title)).toHaveAttribute("aria-checked", "true");
    expect(optionNamesIn(title)).toEqual(allNames);

    // Recolhida + unread on = somente as com unread.
    await user.click(collapseButton(title));
    expect(collapseButton(title)).toHaveAttribute("aria-expanded", "false");
    expect(optionNamesIn(title)).toEqual([unreadOnlyName]);

    // Recolhida + unread off = nenhum item, e nenhuma mensagem de "vazio".
    await user.click(unreadSwitch(title));
    expect(unreadSwitch(title)).toHaveAttribute("aria-checked", "false");
    expect(optionNamesIn(title)).toEqual([]);
    expect(within(section(title)).queryByText(/nenhum|nenhuma/i)).not.toBeInTheDocument();
  });

  it("counts conversations with unread in the header, never a sum of message counts", () => {
    renderState(
      [channel("a", { unreadCount: 5 }), channel("b", { unreadCount: 3 }), channel("c")],
      [],
    );

    // Two conversations carry unread (5 and 3 messages) — the count is 2, not 8.
    expect(screen.getByLabelText("2 conversas não lidas")).toHaveTextContent("2");
  });

  // ── Issue #787 — header refinements ────────────────────────────────────────
  // A visual pass over the #779 controls. The source of the count, the realtime
  // updates and the preference behind the switch are all unchanged; what
  // changes is that a zero is not drawn and the switch is a switch.

  it("renders no count at all for a section with nothing unread", () => {
    renderState([channel("a"), channel("b")], []);

    // Not a "0" anywhere in the header, and not an element holding one either:
    // the count must not reserve width for a section that has nothing to say.
    const header = within(section("Canais"));
    expect(header.queryByLabelText(/conversas? não lidas?$/)).toBeNull();
    expect(document.querySelectorAll(".chat-sidebar__section-unread-count")).toHaveLength(0);
  });

  it("renders the count from one unread conversation upwards, in the singular and the plural", () => {
    const { unmount } = renderState([channel("a", { unreadCount: 4 }), channel("b")], []);
    expect(within(section("Canais")).getByLabelText("1 conversa não lida")).toHaveTextContent("1");
    unmount();

    renderState([channel("a", { unreadCount: 4 }), channel("b", { unreadCount: 1 })], []);
    expect(within(section("Canais")).getByLabelText("2 conversas não lidas")).toHaveTextContent(
      "2",
    );
  });

  it("keeps each section's count independent, showing none for the sections that are quiet", () => {
    renderState([channel("a", { unreadCount: 2 })], [dm("d", "1:1"), dm("g", "group")]);

    expect(within(section("Canais")).getByLabelText("1 conversa não lida")).toBeInTheDocument();
    expect(
      within(section("Mensagens diretas")).queryByLabelText(/conversas? não lidas?$/),
    ).toBeNull();
    expect(within(section("Grupos")).queryByLabelText(/conversas? não lidas?$/)).toBeNull();
  });

  it("appears and disappears from the header as unread arrives and is cleared", () => {
    const read = channel("a");
    const unread = channel("a", { unreadCount: 3 });

    const { rerender } = render(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={readyState([read], [])} retry={() => {}} />
      </MemoryRouter>,
    );
    expect(within(section("Canais")).queryByLabelText(/conversas? não lidas?$/)).toBeNull();

    // The same update path the realtime events already use: new canonical state.
    rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={readyState([unread], [])} retry={() => {}} />
      </MemoryRouter>,
    );
    expect(within(section("Canais")).getByLabelText("1 conversa não lida")).toHaveTextContent("1");

    rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={readyState([read], [])} retry={() => {}} />
      </MemoryRouter>,
    );
    expect(within(section("Canais")).queryByLabelText(/conversas? não lidas?$/)).toBeNull();
  });

  it("draws the unread-only control as a switch whose thumb moves, not by colour alone", async () => {
    const user = userEvent.setup();
    renderState([channel("a", { unreadCount: 1 })], []);

    const toggle = unreadSwitch("Canais");
    // The semantics of issue #779 are untouched.
    expect(toggle).toHaveAttribute("role", "switch");
    expect(toggle).toHaveAttribute("aria-checked", "false");
    expect(toggle).toHaveAccessibleName(
      "Mostrar mensagens não lidas quando Canais estiver recolhida",
    );

    // The visual is a rail with a thumb, and the "on" class is what moves it.
    expect(toggle.querySelector(".chat-sidebar__section-unread-track")).not.toBeNull();
    expect(toggle.querySelector(".chat-sidebar__section-unread-thumb")).not.toBeNull();
    expect(toggle.className).not.toContain("chat-sidebar__section-unread-toggle--on");

    await user.click(toggle);
    expect(unreadSwitch("Canais")).toHaveAttribute("aria-checked", "true");
    expect(unreadSwitch("Canais").className).toContain("chat-sidebar__section-unread-toggle--on");
  });

  it("hints at the switch on hover without letting the hint become its name", () => {
    renderState([channel("a")], []);

    for (const title of ["Canais", "Mensagens diretas", "Grupos"]) {
      const toggle = unreadSwitch(title);
      // The hover affordance is the browser's own tooltip — one attribute, no
      // element of ours inside the sidebar's scrollport.
      expect(toggle).toHaveAttribute("title", "Exibir não lidas quando a seção estiver recolhida");
      // The generic hint must never displace the name that says *which*
      // section this switch belongs to.
      expect(toggle).toHaveAttribute(
        "aria-label",
        `Mostrar mensagens não lidas quando ${title} estiver recolhida`,
      );
      expect(toggle).toHaveAccessibleName(
        `Mostrar mensagens não lidas quando ${title} estiver recolhida`,
      );
      expect(toggle).toHaveAttribute("role", "switch");
      expect(toggle).toHaveAttribute("aria-checked", "false");
    }
  });

  it("keeps the three sections' collapse and unread-only state fully independent", async () => {
    const user = userEvent.setup();
    renderState(
      [channel("ch", { unreadCount: 1 })],
      [dm("d", "1:1", { unreadCount: 1 }), dm("g", "group", { unreadCount: 1 })],
    );

    await user.click(collapseButton("Canais"));
    await user.click(unreadSwitch("Canais"));

    // Mensagens diretas and Grupos remain expanded and unaffected.
    expect(collapseButton("Mensagens diretas")).toHaveAttribute("aria-expanded", "true");
    expect(collapseButton("Grupos")).toHaveAttribute("aria-expanded", "true");
    expect(optionNamesIn("Mensagens diretas")).toEqual(["Mensagem direta com d"]);
    expect(optionNamesIn("Grupos")).toEqual(["Grupo g"]);
    // Canais alone shows its (only, unread) channel while collapsed.
    expect(optionNamesIn("Canais")).toEqual(["Canal ch"]);
  });

  it("removes a conversation from the collapsed+unread view once it is marked read", () => {
    const { rerender } = render(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={readyState([channel("a", { unreadCount: 3 })], [])} retry={() => {}} />
      </MemoryRouter>,
    );

    fireEvent.click(collapseButton("Canais"));
    fireEvent.click(unreadSwitch("Canais"));
    expect(optionNamesIn("Canais")).toEqual(["Canal a"]);

    // The server-authoritative unreadCount drops to 0 (mark read / realtime
    // reconciliation) — the same canonical array the badges already render.
    rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={readyState([channel("a", { unreadCount: 0 })], [])} retry={() => {}} />
      </MemoryRouter>,
    );

    expect(optionNamesIn("Canais")).toEqual([]);
  });

  it("shows a newly-unread conversation the instant its canonical unreadCount arrives", () => {
    const { rerender } = render(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={readyState([channel("a")], [])} retry={() => {}} />
      </MemoryRouter>,
    );

    fireEvent.click(collapseButton("Canais"));
    fireEvent.click(unreadSwitch("Canais"));
    expect(optionNamesIn("Canais")).toEqual([]);

    // A realtime message bumped the canonical unread count.
    rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={readyState([channel("a", { unreadCount: 1 })], [])} retry={() => {}} />
      </MemoryRouter>,
    );

    expect(optionNamesIn("Canais")).toEqual(["Canal a"]);
  });

  it("keeps the open conversation's route unchanged when its section is collapsed", async () => {
    const user = userEvent.setup();
    function LocationProbe({ onPath }: { onPath: (path: string) => void }) {
      onPath(useLocation().pathname);
      return null;
    }
    let path = "";
    render(
      <MemoryRouter initialEntries={["/chat/channel/geral"]}>
        <LocationProbe onPath={(p) => (path = p)} />
        <ChatSidebar state={readyState([channel("geral")], [])} retry={() => {}} />
      </MemoryRouter>,
    );

    await user.click(collapseButton("Canais"));

    // The active channel disappeared from the list (no unread, section
    // collapsed with the toggle off) but the route never moved.
    expect(screen.queryByRole("option", { name: "Canal geral" })).not.toBeInTheDocument();
    expect(path).toBe("/chat/channel/geral");
  });

  it("does not force visibility from a pin alone — only unreadCount decides", async () => {
    const user = userEvent.setup();
    renderState(
      [
        channel("pinned-but-read", { pinnedAt: "2026-08-12T10:00:00Z" }),
        channel("has-unread", { unreadCount: 1 }),
      ],
      [],
    );

    await user.click(collapseButton("Canais"));
    await user.click(unreadSwitch("Canais"));

    expect(optionNamesIn("Canais")).toEqual(["Canal has-unread"]);
  });

  it("keeps a muted conversation's unread following the canonical count", async () => {
    const user = userEvent.setup();
    renderState([channel("muted-unread", { unreadCount: 2, muted: true })], []);

    await user.click(collapseButton("Canais"));
    await user.click(unreadSwitch("Canais"));

    expect(optionNamesIn("Canais")).toEqual(["Canal muted-unread"]);
  });

  it("keeps a mentioned conversation eligible for the unread filter", async () => {
    const user = userEvent.setup();
    renderState([channel("mentioned", { unreadCount: 1, hasMentionUnread: true })], []);

    await user.click(collapseButton("Canais"));
    await user.click(unreadSwitch("Canais"));

    expect(optionNamesIn("Canais")).toEqual(["Canal mentioned"]);
  });

  it("shows the genuinely-empty message only while expanded, never while collapsed", async () => {
    const user = userEvent.setup();
    renderState([], []);

    expect(within(section("Canais")).getByText("Nenhum canal disponível.")).toBeInTheDocument();

    await user.click(collapseButton("Canais"));
    expect(
      within(section("Canais")).queryByText("Nenhum canal disponível."),
    ).not.toBeInTheDocument();

    await user.click(unreadSwitch("Canais"));
    expect(
      within(section("Canais")).queryByText("Nenhum canal disponível."),
    ).not.toBeInTheDocument();
    // The header is still there — its switch proves it — but since issue #787
    // an empty section shows no count at all rather than a literal "0".
    expect(unreadSwitch("Canais")).toBeInTheDocument();
    expect(within(section("Canais")).queryByLabelText(/conversas? não lidas?$/)).toBeNull();
  });

  it("persists per (user, workspace) and restores across a remount", () => {
    const { unmount } = renderState([channel("a")], []);
    fireEvent.click(collapseButton("Canais"));
    fireEvent.click(unreadSwitch("Canais"));
    unmount();

    renderState([channel("a")], []);
    expect(collapseButton("Canais")).toHaveAttribute("aria-expanded", "false");
    expect(unreadSwitch("Canais")).toHaveAttribute("aria-checked", "true");
  });

  it("falls back to defaults when the persisted section preference is corrupted", () => {
    localStorage.setItem("nchat.sidebar.sections.v1:workspace-1:user-a", "{not json");

    renderState([channel("a")], []);

    expect(collapseButton("Canais")).toHaveAttribute("aria-expanded", "true");
    expect(unreadSwitch("Canais")).toHaveAttribute("aria-checked", "false");
  });

  // Code review (issue #779): the section preference hook must never reuse an
  // in-session edit made under one (user, workspace) scope once the sidebar
  // moves to another — and the very first render of the new scope must already
  // reflect whatever that scope's own storage holds, with no intermediate
  // frame showing the old scope's state.
  it("never reuses a scope's preference after switching user or workspace, and shows the new scope's own state on the very first render", () => {
    const stateFor = (workspaceId: string, currentUserId: string, channels: Channel[]) => ({
      status: "ready" as const,
      currentUserId,
      workspaceId,
      channels,
      dms: [] as DMConversation[],
      categories: [] as ChannelCategory[],
    });

    // The second scope already has a collapsed Canais persisted from a prior
    // session — proving the first render for that scope picks it up directly,
    // not the first scope's (expanded) state.
    localStorage.setItem(
      "nchat.sidebar.sections.v1:workspace-2:user-b",
      JSON.stringify({
        channels: { collapsed: true, showUnreadOnly: false },
        directs: { collapsed: false, showUnreadOnly: false },
        groups: { collapsed: false, showUnreadOnly: false },
      }),
    );

    const { rerender } = render(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={stateFor("workspace-1", "user-a", [channel("a")])} retry={() => {}} />
      </MemoryRouter>,
    );

    // Collapse Canais under the first (user, workspace) scope, in-session.
    fireEvent.click(collapseButton("Canais"));
    expect(collapseButton("Canais")).toHaveAttribute("aria-expanded", "false");

    // Switch scope — same component instance, new user and workspace.
    rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={stateFor("workspace-2", "user-b", [channel("a")])} retry={() => {}} />
      </MemoryRouter>,
    );

    // The new scope's own persisted state — collapsed — not a stale carry-over
    // of the first scope's in-session edit (which was also collapsed, so this
    // alone would not distinguish the bug; the switch back below does).
    expect(collapseButton("Canais")).toHaveAttribute("aria-expanded", "false");

    // A third, never-before-seen scope must not inherit either earlier
    // scope's collapsed state — it has nothing persisted, so it falls back to
    // the plain defaults (expanded).
    rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar state={stateFor("workspace-3", "user-c", [channel("a")])} retry={() => {}} />
      </MemoryRouter>,
    );
    expect(collapseButton("Canais")).toHaveAttribute("aria-expanded", "true");
  });

  it("exposes the collapse control and the unread toggle as distinct, keyboard-operable elements", async () => {
    const user = userEvent.setup();
    renderState([channel("a", { unreadCount: 1 })], []);

    const collapse = collapseButton("Canais");
    const toggle = unreadSwitch("Canais");
    // Two independent controls — not a button nested in a button.
    expect(collapse).not.toContainElement(toggle);
    expect(toggle).not.toContainElement(collapse);
    expect(toggle.tagName).toBe("BUTTON");
    expect(toggle).toHaveAttribute("role", "switch");

    collapse.focus();
    await user.keyboard("{Enter}");
    expect(collapse).toHaveAttribute("aria-expanded", "false");
    await user.keyboard(" ");
    expect(collapse).toHaveAttribute("aria-expanded", "true");

    toggle.focus();
    await user.keyboard("{Enter}");
    expect(toggle).toHaveAttribute("aria-checked", "true");
    await user.keyboard(" ");
    expect(toggle).toHaveAttribute("aria-checked", "false");
  });

  it("hides only the categories that became empty after the unread filter, without disturbing category collapse state", async () => {
    const user = userEvent.setup();
    renderState(
      [
        channel("ch-unread", { categoryId: "cat-a", unreadCount: 1 }),
        channel("ch-read", { categoryId: "cat-b" }),
      ],
      [],
      [
        { id: "cat-a", name: "Categoria A", kind: "category" },
        { id: "cat-b", name: "Categoria B", kind: "category" },
      ],
    );

    expect(screen.getByRole("button", { name: /Categoria A/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Categoria B/i })).toBeInTheDocument();

    await user.click(collapseButton("Canais"));
    await user.click(unreadSwitch("Canais"));

    // Only the category that still has an unread channel is shown.
    expect(screen.getByRole("button", { name: /Categoria A/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Categoria B/i })).not.toBeInTheDocument();
    expect(optionNamesIn("Canais")).toEqual(["Canal ch-unread"]);

    // Expanding the section again restores both categories untouched.
    await user.click(collapseButton("Canais"));
    expect(screen.getByRole("button", { name: /Categoria A/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Categoria B/i })).toBeInTheDocument();
  });
});

// ── Row action menu (ISSUE #527) ─────────────────────────────────────────────
//
// The visual contract this issue establishes: "…" means actions, a white pin
// means pinned, and an unpinned row carries no pin at all. Pinning stopped being
// a click on the pin and became a menu item; nothing in the menu may select a
// conversation, navigate, or change what is read.

describe("ChatSidebar — row action menu", () => {
  const channel = (id: string, overrides: Partial<Channel> = {}): Channel => ({
    id,
    name: id,
    type: "public",
    canWrite: true,
    ...overrides,
  });
  const dm = (
    id: string,
    type: "1:1" | "group",
    overrides: Partial<DMConversation> = {},
  ): DMConversation => ({
    id,
    type,
    name: id,
    participants: [],
    ...overrides,
  });

  const readyState = (channels: Channel[], dms: DMConversation[]) => ({
    status: "ready" as const,
    currentUserId: "user-a",
    workspaceId: "workspace-1",
    channels,
    dms,
    categories: [] as ChannelCategory[],
  });

  interface RenderOptions {
    channels?: Channel[];
    dms?: DMConversation[];
    path?: string;
    setPinned?: (
      target: { kind: "channel" | "dm"; targetId: string },
      pinned: boolean,
    ) => Promise<void>;
    markRead?: (target: { kind: "channel" | "dm"; targetId: string }) => void;
    renameChannel?: (channelId: string, displayName: string) => Promise<void>;
    renameGroup?: (conversationId: string, title: string) => Promise<void>;
    setMuted?: (
      target: { kind: "channel" | "dm"; targetId: string },
      muted: boolean,
    ) => Promise<void>;
    leaveConversation?: (target: { kind: "channel" | "dm"; targetId: string }) => Promise<void>;
    onOpenDetails?: (kind: "channel" | "dm", targetId: string) => void;
  }

  const renderSidebar = ({
    channels = [],
    dms = [],
    path = "/chat",
    setPinned = vi.fn().mockResolvedValue(undefined),
    markRead = vi.fn(),
    renameChannel = vi.fn().mockResolvedValue(undefined),
    renameGroup = vi.fn().mockResolvedValue(undefined),
    setMuted = vi.fn().mockResolvedValue(undefined),
    leaveConversation = vi.fn().mockResolvedValue(undefined),
    onOpenDetails = vi.fn(),
  }: RenderOptions = {}) =>
    render(
      <MemoryRouter initialEntries={[path]}>
        <ChatSidebar
          state={readyState(channels, dms)}
          retry={() => {}}
          setPinned={setPinned}
          markRead={markRead}
          renameChannel={renameChannel}
          renameGroup={renameGroup}
          setMuted={setMuted}
          leaveConversation={leaveConversation}
          onOpenDetails={onOpenDetails}
        />
      </MemoryRouter>,
    );

  const trigger = (name: string) =>
    screen.getByRole("button", { name: `Mais opções para ${name}` });

  // ── The pin is state, not an action ────────────────────────────────────────

  it("draws no pin at all on an unpinned row", () => {
    renderSidebar({ channels: [channel("geral")], dms: [dm("Juliane", "1:1")] });

    expect(screen.queryAllByTestId("chat-sidebar-pinned")).toHaveLength(0);
    expect(screen.getByRole("option", { name: "Canal geral" })).toBeInTheDocument();
  });

  it("draws a pin on a pinned row and says so in the row's accessible name", () => {
    renderSidebar({ channels: [channel("geral", { pinnedAt: "2026-08-12T10:00:00Z" })] });

    expect(screen.getAllByTestId("chat-sidebar-pinned")).toHaveLength(1);
    expect(screen.getByRole("option", { name: "Canal geral, fixado" })).toBeInTheDocument();
  });

  // The pin used to be the way to unpin. It must not be reachable as a control
  // any more — not by role, and not by tabbing.
  it("leaves the pin out of the accessibility tree as a control", () => {
    renderSidebar({ channels: [channel("geral", { pinnedAt: "2026-08-12T10:00:00Z" })] });

    expect(screen.queryByRole("button", { name: /desafixar/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /fixar/i })).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-sidebar-pinned")).toHaveAttribute("aria-hidden", "true");
  });

  // ── The trigger ────────────────────────────────────────────────────────────

  it("names the trigger after the conversation and declares its popup", async () => {
    const user = userEvent.setup();
    renderSidebar({ channels: [channel("geral")] });

    const button = trigger("canal geral");
    expect(button).toHaveAttribute("aria-haspopup", "menu");
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).not.toHaveAccessibleName("...");

    await user.click(button);
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(button).toHaveAttribute("aria-controls", screen.getByRole("menu").id);
  });

  it("keeps at most one menu open across rows", async () => {
    const user = userEvent.setup();
    renderSidebar({ channels: [channel("geral"), channel("infra")] });

    await user.click(trigger("canal geral"));
    await user.click(trigger("canal infra"));

    expect(screen.getAllByRole("menu")).toHaveLength(1);
    expect(trigger("canal geral")).toHaveAttribute("aria-expanded", "false");
    expect(trigger("canal infra")).toHaveAttribute("aria-expanded", "true");
  });

  // ── The menu's contents ────────────────────────────────────────────────────

  it("offers Fixar no topo on an unpinned row and Desafixar on a pinned one", async () => {
    const user = userEvent.setup();
    const setPinned = vi.fn().mockResolvedValue(undefined);
    const { unmount } = renderSidebar({ channels: [channel("geral")], setPinned });

    await user.click(trigger("canal geral"));
    expect(screen.getByRole("menuitem", { name: "Fixar no topo" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Desafixar" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: "Fixar no topo" }));
    expect(setPinned).toHaveBeenCalledWith({ kind: "channel", targetId: "geral" }, true);
    unmount();

    const unpin = vi.fn().mockResolvedValue(undefined);
    renderSidebar({
      channels: [channel("geral", { pinnedAt: "2026-08-12T10:00:00Z" })],
      setPinned: unpin,
    });
    await user.click(trigger("canal geral"));
    expect(screen.getByRole("menuitem", { name: "Desafixar" })).toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: "Desafixar" }));
    expect(unpin).toHaveBeenCalledWith({ kind: "channel", targetId: "geral" }, false);
  });

  // A group is a chat.dm_conversations row: its pin goes to the DM endpoint.
  it("pins a group through the DM endpoint", async () => {
    const user = userEvent.setup();
    const setPinned = vi.fn().mockResolvedValue(undefined);
    renderSidebar({ dms: [dm("Equipe Infra", "group")], setPinned });

    await user.click(trigger("grupo Equipe Infra"));
    await user.click(screen.getByRole("menuitem", { name: "Fixar no topo" }));

    expect(setPinned).toHaveBeenCalledWith({ kind: "dm", targetId: "Equipe Infra" }, true);
  });

  it("never offers Sair on a 1:1 conversation", async () => {
    const user = userEvent.setup();
    renderSidebar({
      dms: [dm("Juliane", "1:1", { unreadCount: 4, pinnedAt: "2026-08-12T10:00:00Z" })],
    });

    await user.click(trigger("conversa com Juliane"));

    expect(screen.queryByRole("menuitem", { name: /sair/i })).not.toBeInTheDocument();
  });

  // Archiving and hiding have no backend at all, so they must never appear on
  // any row (issue #527 keeps them out of scope deliberately).
  it("offers no action the backend does not implement", async () => {
    const user = userEvent.setup();
    renderSidebar({
      channels: [channel("geral", { canRename: true, unreadCount: 2 })],
      dms: [dm("Equipe", "group", { unreadCount: 1 }), dm("Juliane", "1:1", { unreadCount: 1 })],
    });

    for (const name of ["canal geral", "grupo Equipe", "conversa com Juliane"]) {
      await user.click(trigger(name));
      for (const absent of [/arquivar/i, /ocultar/i, /excluir/i]) {
        expect(screen.queryByRole("menuitem", { name: absent })).not.toBeInTheDocument();
      }
      await user.keyboard("{Escape}");
    }
  });

  // The structural rules, asserted through the rendered menu rather than only
  // through the action builder.
  it("never offers Sair or Renomear on a 1:1 conversation", async () => {
    const user = userEvent.setup();
    renderSidebar({ dms: [dm("Juliane", "1:1", { unreadCount: 2 })] });

    await user.click(trigger("conversa com Juliane"));

    expect(screen.queryByRole("menuitem", { name: /sair/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /renomear/i })).not.toBeInTheDocument();
    // What it does get.
    expect(screen.getByRole("menuitem", { name: "Silenciar notificações" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Detalhes da conversa" })).toBeInTheDocument();
  });

  // The general channel is structural: no rename, no mute, no leave, for anyone.
  it("omits rename, mute and leave on the general channel", async () => {
    const user = userEvent.setup();
    renderSidebar({
      channels: [channel("geral", { isGeneral: true, canRename: true, unreadCount: 3 })],
    });

    await user.click(trigger("canal geral"));

    for (const absent of [/renomear/i, /silenciar/i, /notificações/i, /sair/i]) {
      expect(screen.queryByRole("menuitem", { name: absent })).not.toBeInTheDocument();
    }
    expect(screen.getByRole("menuitem", { name: "Fixar no topo" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Marcar como lido" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Detalhes do canal" })).toBeInTheDocument();
  });

  it("offers the full menu on an ordinary channel", async () => {
    const user = userEvent.setup();
    renderSidebar({ channels: [channel("infra", { canRename: true, unreadCount: 1 })] });

    await user.click(trigger("canal infra"));

    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "Fixar no topo",
      "Marcar como lido",
      "Silenciar notificações",
      "Renomear canal",
      "Detalhes do canal",
      "Sair do canal",
    ]);
  });

  it("offers the full menu on a group, including rename", async () => {
    const user = userEvent.setup();
    renderSidebar({ dms: [dm("Equipe", "group", { unreadCount: 1 })] });

    await user.click(trigger("grupo Equipe"));

    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "Fixar no topo",
      "Marcar como lido",
      "Silenciar notificações",
      "Renomear grupo",
      "Detalhes do grupo",
      "Sair do grupo",
    ]);
  });

  it("toggles the notification item against the persisted preference", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn().mockResolvedValue(undefined);
    renderSidebar({ channels: [channel("infra", { muted: true })], setMuted });

    await user.click(trigger("canal infra"));
    expect(
      screen.queryByRole("menuitem", { name: "Silenciar notificações" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: "Ativar notificações" }));

    expect(setMuted).toHaveBeenCalledWith({ kind: "channel", targetId: "infra" }, false);
  });

  // A group mutes through the DM endpoint, because it is a dm_conversations row.
  it("mutes a group through the DM endpoint", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn().mockResolvedValue(undefined);
    renderSidebar({ dms: [dm("Equipe", "group")], setMuted });

    await user.click(trigger("grupo Equipe"));
    await user.click(screen.getByRole("menuitem", { name: "Silenciar notificações" }));

    expect(setMuted).toHaveBeenCalledWith({ kind: "dm", targetId: "Equipe" }, true);
  });

  // The action acts on the row whose menu was opened, never on the selected
  // conversation. This is the mistake the whole target-threading exists to stop.
  it("acts on the menu's target and not on the selected conversation", async () => {
    const user = userEvent.setup();
    const setMuted = vi.fn().mockResolvedValue(undefined);
    const markRead = vi.fn();
    renderSidebar({
      channels: [channel("geral", { unreadCount: 4 }), channel("infra", { unreadCount: 2 })],
      path: "/chat/channel/geral",
      setMuted,
      markRead,
    });

    await user.click(trigger("canal infra"));
    await user.click(screen.getByRole("menuitem", { name: "Marcar como lido" }));
    expect(markRead).toHaveBeenCalledWith({ kind: "channel", targetId: "infra" });

    await user.click(trigger("canal infra"));
    await user.click(screen.getByRole("menuitem", { name: "Silenciar notificações" }));
    expect(setMuted).toHaveBeenCalledWith({ kind: "channel", targetId: "infra" }, true);

    // The selected conversation is untouched throughout.
    expect(screen.getByRole("option", { name: /Canal geral/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("opens details for the menu's target, without navigating", async () => {
    const user = userEvent.setup();
    const onOpenDetails = vi.fn();
    renderSidebar({
      channels: [channel("geral"), channel("infra")],
      path: "/chat/channel/geral",
      onOpenDetails,
    });

    await user.click(trigger("canal infra"));
    await user.click(screen.getByRole("menuitem", { name: "Detalhes do canal" }));

    // The row's own trigger travels with the request (issue #467, code quality
    // review): the panel opens elsewhere, and closing it has to know where to
    // hand focus back to.
    expect(onOpenDetails).toHaveBeenCalledWith("channel", "infra", trigger("canal infra"));
    expect(screen.getByRole("option", { name: /Canal geral/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("shows Renomear canal only when the server said this caller may rename", async () => {
    const user = userEvent.setup();
    renderSidebar({ channels: [channel("geral", { canRename: true }), channel("infra")] });

    await user.click(trigger("canal geral"));
    expect(screen.getByRole("menuitem", { name: "Renomear canal" })).toBeInTheDocument();
    await user.keyboard("{Escape}");

    await user.click(trigger("canal infra"));
    expect(screen.queryByRole("menuitem", { name: "Renomear canal" })).not.toBeInTheDocument();
  });

  // ISSUE #527 — a categorized channel comes from the categories endpoint, whose
  // payload once omitted can_rename entirely. The row must offer the same menu
  // wherever the channel is filed.
  it("shows Renomear canal on a categorized channel too", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar
          state={{
            status: "ready",
            currentUserId: "user-a",
            workspaceId: "workspace-1",
            channels: [channel("infra", { canRename: true, categoryId: "cat-1" })],
            dms: [],
            categories: [{ id: "cat-1", name: "Projetos", kind: "category" }],
          }}
          retry={() => {}}
          setPinned={vi.fn().mockResolvedValue(undefined)}
          markRead={vi.fn()}
          renameChannel={vi.fn().mockResolvedValue(undefined)}
        />
      </MemoryRouter>,
    );

    // The channel really is rendered under its category, not in a flat list.
    expect(screen.getByRole("button", { name: /Projetos/ })).toBeInTheDocument();

    await user.click(trigger("canal infra"));
    expect(screen.getByRole("menuitem", { name: "Renomear canal" })).toBeInTheDocument();
  });

  // The same categorized row, for a caller the server did not authorize. The
  // capability is the only thing that decides what the menu shows — the category
  // never grants or withholds it.
  it("omits Renomear canal on a categorized channel the server did not authorize", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar
          state={{
            status: "ready",
            currentUserId: "user-a",
            workspaceId: "workspace-1",
            channels: [channel("infra", { canRename: false, categoryId: "cat-1" })],
            dms: [],
            categories: [{ id: "cat-1", name: "Projetos", kind: "category" }],
          }}
          retry={() => {}}
          setPinned={vi.fn().mockResolvedValue(undefined)}
          markRead={vi.fn()}
          renameChannel={vi.fn().mockResolvedValue(undefined)}
        />
      </MemoryRouter>,
    );

    expect(screen.getByRole("button", { name: /Projetos/ })).toBeInTheDocument();

    await user.click(trigger("canal infra"));
    expect(screen.getByRole("menu")).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Renomear canal" })).not.toBeInTheDocument();
  });

  it("offers Marcar como lido only when the row has unread messages", async () => {
    const user = userEvent.setup();
    const markRead = vi.fn();
    renderSidebar({ channels: [channel("geral", { unreadCount: 3 }), channel("infra")], markRead });

    await user.click(trigger("canal infra"));
    expect(screen.queryByRole("menuitem", { name: "Marcar como lido" })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");

    await user.click(trigger("canal geral"));
    await user.click(screen.getByRole("menuitem", { name: "Marcar como lido" }));
    expect(markRead).toHaveBeenCalledWith({ kind: "channel", targetId: "geral" });
  });

  // ── Nothing here selects, navigates or reads ───────────────────────────────

  it("does not select the conversation when the trigger is used", async () => {
    const user = userEvent.setup();
    renderSidebar({
      channels: [channel("geral"), channel("infra")],
      path: "/chat/channel/infra",
    });

    await user.click(trigger("canal geral"));

    expect(screen.getByRole("option", { name: "Canal infra" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByRole("option", { name: "Canal geral" })).toHaveAttribute(
      "aria-selected",
      "false",
    );
  });

  it("does not select the conversation when a menu action runs", async () => {
    const user = userEvent.setup();
    renderSidebar({
      channels: [channel("geral"), channel("infra")],
      path: "/chat/channel/infra",
    });

    await user.click(trigger("canal geral"));
    await user.click(screen.getByRole("menuitem", { name: "Fixar no topo" }));

    expect(screen.getByRole("option", { name: "Canal infra" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  // Opening a menu is not reading a conversation. The badge is what would betray
  // it, and markRead is the only thing allowed to clear it.
  it("leaves the unread badge alone while the menu is merely open", async () => {
    const user = userEvent.setup();
    const markRead = vi.fn();
    renderSidebar({ channels: [channel("geral", { unreadCount: 7 })], markRead });

    await user.click(trigger("canal geral"));

    expect(screen.getByLabelText("7 não lidas")).toBeInTheDocument();
    expect(markRead).not.toHaveBeenCalled();
  });

  // ── Keyboard ───────────────────────────────────────────────────────────────

  it("opens with Enter and with Space, and closes on Escape returning focus", async () => {
    const user = userEvent.setup();
    renderSidebar({ channels: [channel("geral")] });
    const button = trigger("canal geral");

    button.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(button).toHaveFocus();

    await user.keyboard(" ");
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(button).toHaveFocus();
  });

  it("moves between items with the arrow keys and runs one with Enter", async () => {
    const user = userEvent.setup();
    const setPinned = vi.fn().mockResolvedValue(undefined);
    const markRead = vi.fn();
    renderSidebar({ channels: [channel("geral", { unreadCount: 2 })], setPinned, markRead });

    trigger("canal geral").focus();
    await user.keyboard("{Enter}");
    // Focus opens on the first item.
    expect(screen.getByRole("menuitem", { name: "Fixar no topo" })).toHaveFocus();

    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("menuitem", { name: "Marcar como lido" })).toHaveFocus();
    await user.keyboard("{ArrowUp}");
    expect(screen.getByRole("menuitem", { name: "Fixar no topo" })).toHaveFocus();

    await user.keyboard("{Enter}");
    expect(setPinned).toHaveBeenCalledWith({ kind: "channel", targetId: "geral" }, true);
    expect(markRead).not.toHaveBeenCalled();
  });

  // The sidebar's nav is an `overflow-y: auto` scrollport, which clips anything
  // absolutely positioned inside it — the menu on the last visible row was cut
  // off. It is portalled to <body> instead, so the popup has no clipping
  // ancestor at all and is positioned in viewport coordinates.
  it("renders the menu outside the sidebar's scrollport", async () => {
    const user = userEvent.setup();
    const { container } = renderSidebar({ channels: [channel("geral")] });

    await user.click(trigger("canal geral"));

    const menu = screen.getByRole("menu");
    expect(container.querySelector(".chat-sidebar__nav")?.contains(menu)).toBe(false);
    expect(menu.parentElement).toBe(document.body);
    expect(menu.style.top).not.toBe("");
    expect(menu.style.left).not.toBe("");
  });

  it("returns focus to the trigger after an action runs", async () => {
    const user = userEvent.setup();
    renderSidebar({ channels: [channel("geral")] });
    const button = trigger("canal geral");

    button.focus();
    await user.keyboard("{Enter}");
    await user.keyboard("{Enter}");

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(button).toHaveFocus();
  });

  // The menu is not a focus trap: Tab out of it closes it and leaves focus on the
  // trigger, from which the next Tab continues through the sidebar normally. The
  // popup is portalled to <body>, so handing the move to the browser from inside
  // it would strand focus at the end of the document instead.
  it("does not trap Tab inside the menu", async () => {
    const user = userEvent.setup();
    renderSidebar({ channels: [channel("geral"), channel("infra")] });

    const button = trigger("canal geral");
    button.focus();
    await user.keyboard("{Enter}");
    await user.tab();

    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(document.activeElement).not.toBe(document.body);
    expect(button).toHaveFocus();

    // And the sidebar's tab order continues from there.
    await user.tab();
    expect(button).not.toHaveFocus();
  });

  // ── Reordering (#474 is preserved) ─────────────────────────────────────────

  it("reorders on pin and on unpin, without duplicating the row", async () => {
    const user = userEvent.setup();
    const older = { lastMessageAt: "2026-07-01T00:00:00Z" };
    const newer = { lastMessageAt: "2026-07-31T00:00:00Z" };
    const { rerender } = renderSidebar({
      channels: [channel("antigo", older), channel("novo", newer)],
    });

    expect(
      within(screen.getByRole("region", { name: "Canais" }))
        .getAllByRole("option")
        .map((option) => option.getAttribute("aria-label")),
    ).toEqual(["Canal novo", "Canal antigo"]);

    // What the hook's optimistic pin_changed produces.
    rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar
          state={readyState(
            [
              channel("antigo", { ...older, pinnedAt: "0001-01-01T00:00:00Z" }),
              channel("novo", newer),
            ],
            [],
          )}
          retry={() => {}}
        />
      </MemoryRouter>,
    );
    const pinnedOrder = within(screen.getByRole("region", { name: "Canais" }))
      .getAllByRole("option")
      .map((option) => option.getAttribute("aria-label"));
    expect(pinnedOrder).toEqual(["Canal antigo, fixado", "Canal novo"]);
    expect(screen.getAllByTestId("chat-sidebar-pinned")).toHaveLength(1);

    // And back, on unpin.
    rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar
          state={readyState([channel("antigo", older), channel("novo", newer)], [])}
          retry={() => {}}
        />
      </MemoryRouter>,
    );
    expect(
      within(screen.getByRole("region", { name: "Canais" }))
        .getAllByRole("option")
        .map((option) => option.getAttribute("aria-label")),
    ).toEqual(["Canal novo", "Canal antigo"]);
    expect(screen.queryAllByTestId("chat-sidebar-pinned")).toHaveLength(0);
    await user.click(trigger("canal novo"));
    expect(screen.getByRole("menuitem", { name: "Fixar no topo" })).toBeInTheDocument();
  });

  // A rejected pin must not leave the row claiming a state the server refused.
  // The hook owns the rollback; the sidebar's job is to surface the failure.
  it("reports a failed pin instead of leaving the row pinned", async () => {
    const user = userEvent.setup();
    const setPinned = vi.fn().mockRejectedValue(new Error("nope"));
    renderSidebar({ channels: [channel("geral")], setPinned });

    await user.click(trigger("canal geral"));
    await user.click(screen.getByRole("menuitem", { name: "Fixar no topo" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/fixação/i);
    expect(screen.queryAllByTestId("chat-sidebar-pinned")).toHaveLength(0);
  });
});

// ── Rename dialog (ISSUE #527) ───────────────────────────────────────────────

describe("ChatSidebar — renaming a channel", () => {
  const renameable = (overrides: Partial<Channel> = {}): Channel => ({
    id: "ch-1",
    name: "infra",
    type: "public",
    canWrite: true,
    canRename: true,
    ...overrides,
  });

  const renderWithRename = (
    renameChannel: (channelId: string, displayName: string) => Promise<void>,
    channels: Channel[] = [renameable()],
    path = "/chat",
  ) =>
    render(
      <MemoryRouter initialEntries={[path]}>
        <ChatSidebar
          state={{
            status: "ready",
            currentUserId: "user-a",
            workspaceId: "workspace-1",
            channels,
            dms: [],
            categories: [],
          }}
          retry={() => {}}
          setPinned={vi.fn().mockResolvedValue(undefined)}
          markRead={vi.fn()}
          renameChannel={renameChannel}
        />
      </MemoryRouter>,
    );

  const openDialog = async (user: ReturnType<typeof userEvent.setup>, name = "canal infra") => {
    await user.click(screen.getByRole("button", { name: `Mais opções para ${name}` }));
    await user.click(screen.getByRole("menuitem", { name: "Renomear canal" }));
  };

  it("opens seeded with the current name, without selecting or navigating", async () => {
    const user = userEvent.setup();
    renderWithRename(
      vi.fn().mockResolvedValue(undefined),
      [renameable(), renameable({ id: "ch-2", name: "geral" })],
      "/chat/channel/ch-2",
    );

    await openDialog(user);

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByLabelText("Nome do canal")).toHaveValue("infra");
    expect(within(dialog).getByLabelText("Nome do canal")).toHaveFocus();
    // The menu closed, and the open conversation did not change.
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Canal geral" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("submits the typed name and closes", async () => {
    const user = userEvent.setup();
    const renameChannel = vi.fn().mockResolvedValue(undefined);
    renderWithRename(renameChannel);

    await openDialog(user);
    const field = screen.getByLabelText("Nome do canal");
    await user.clear(field);
    await user.type(field, "  Plataforma  ");
    await user.click(screen.getByRole("button", { name: "Salvar" }));

    expect(renameChannel).toHaveBeenCalledWith("ch-1", "Plataforma");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("cancels without calling the server", async () => {
    const user = userEvent.setup();
    const renameChannel = vi.fn().mockResolvedValue(undefined);
    renderWithRename(renameChannel);

    await openDialog(user);
    await user.click(screen.getByRole("button", { name: "Cancelar" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(renameChannel).not.toHaveBeenCalled();
  });

  it("closes on Escape without calling the server", async () => {
    const user = userEvent.setup();
    const renameChannel = vi.fn().mockResolvedValue(undefined);
    renderWithRename(renameChannel);

    await openDialog(user);
    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(renameChannel).not.toHaveBeenCalled();
  });

  it("refuses an empty name locally and associates the message with the field", async () => {
    const user = userEvent.setup();
    const renameChannel = vi.fn().mockResolvedValue(undefined);
    renderWithRename(renameChannel);

    await openDialog(user);
    const field = screen.getByLabelText("Nome do canal");
    await user.clear(field);
    await user.type(field, "   ");
    await user.click(screen.getByRole("button", { name: "Salvar" }));

    expect(renameChannel).not.toHaveBeenCalled();
    const error = screen.getByRole("alert");
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveAttribute("aria-describedby", error.id);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  // A refusal keeps the dialog open, recoverable, and showing the typed name —
  // never the UI claiming a name the server did not persist.
  it("keeps the dialog usable when the server refuses", async () => {
    const user = userEvent.setup();
    const renameChannel = vi
      .fn()
      .mockRejectedValue(new ApiRequestError(403, "forbidden", "forbidden"));
    renderWithRename(renameChannel);

    await openDialog(user);
    const field = screen.getByLabelText("Nome do canal");
    await user.clear(field);
    await user.type(field, "Plataforma");
    await user.click(screen.getByRole("button", { name: "Salvar" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/permissão/i);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByLabelText("Nome do canal")).toHaveValue("Plataforma");
    // The sidebar still shows the persisted name.
    expect(screen.getByRole("option", { name: "Canal infra" })).toBeInTheDocument();
  });

  it("submits once however many times Salvar is pressed", async () => {
    const user = userEvent.setup();
    let release: (() => void) | undefined;
    const renameChannel = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = () => resolve();
        }),
    );
    renderWithRename(renameChannel);

    await openDialog(user);
    const field = screen.getByLabelText("Nome do canal");
    await user.clear(field);
    await user.type(field, "Plataforma");
    const save = screen.getByRole("button", { name: "Salvar" });
    await user.click(save);

    // While in flight the controls are disabled and announced as busy.
    const busy = screen.getByRole("button", { name: "Salvando…" });
    expect(busy).toBeDisabled();
    expect(busy).toHaveAttribute("aria-busy", "true");
    expect(screen.getByRole("button", { name: "Cancelar" })).toBeDisabled();
    await user.click(busy);
    expect(renameChannel).toHaveBeenCalledTimes(1);

    release?.();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  // A refetch that drops the channel — access revoked, archived — must not leave
  // a dialog open over a conversation that no longer exists.
  it("closes when the channel disappears from the canonical list", async () => {
    const user = userEvent.setup();
    const renameChannel = vi.fn().mockResolvedValue(undefined);
    const { rerender } = renderWithRename(renameChannel);

    await openDialog(user);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <ChatSidebar
          state={{
            status: "ready",
            currentUserId: "user-a",
            workspaceId: "workspace-1",
            channels: [],
            dms: [],
            categories: [],
          }}
          retry={() => {}}
          renameChannel={renameChannel}
        />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
