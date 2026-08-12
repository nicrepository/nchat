import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import { clearTokens, setTokens } from "../lib/authSession";
import RequireAuth from "../auth/RequireAuth";
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
}));

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
  mockSearchDMCandidates.mockResolvedValue([]);
  mockGetOrCreateDirectDM.mockResolvedValue({ conversationId: "dm-new", created: true });
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

  it("reaches items of all three sections by keyboard, with focus never trapped", async () => {
    const user = userEvent.setup();
    mockFetchSidebarData.mockResolvedValue(mixedSidebar());
    renderChat();

    const trigger = await screen.findByRole("button", { name: "Nova conversa" });
    trigger.focus();

    const expected = [
      "Canal geral",
      "Canal privado projetos",
      "Mensagem direta com Juliane Lino",
      "Grupo Equipe Infra",
    ];
    for (const label of expected) {
      await user.tab();
      expect(screen.getByRole("option", { name: label })).toHaveFocus();
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
    channels,
    dms,
    categories: [] as ChannelCategory[],
  });

  const renderState = (channels: Channel[], dms: DMConversation[], path = "/chat") =>
    render(
      <MemoryRouter initialEntries={[path]}>
        <ChatSidebar state={readyState(channels, dms)} retry={() => {}} />
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

    for (const label of ["Canal canal-novo", "Canal canal-antigo", "Mensagem direta com Juliane"]) {
      await user.tab();
      expect(screen.getByRole("option", { name: label })).toHaveFocus();
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
