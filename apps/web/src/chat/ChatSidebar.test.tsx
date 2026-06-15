import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import RequireAuth from "../auth/RequireAuth";
import ChatShell from "./ChatShell";
import type { Channel, DMConversation } from "./chatTypes";

// ── Mock chatApi ──────────────────────────────────────────────────────────────

const { mockFetchChannels, mockFetchDMs } = vi.hoisted(() => ({
  mockFetchChannels: vi.fn<() => Promise<Channel[]>>(),
  mockFetchDMs:      vi.fn<() => Promise<DMConversation[]>>(),
}));

vi.mock("./chatApi", () => ({
  fetchChannels: () => mockFetchChannels(),
  fetchDMs:      () => mockFetchDMs(),
}));

// ── Fixtures ──────────────────────────────────────────────────────────────────

const SAMPLE_CHANNELS: Channel[] = [
  { id: "geral",          name: "geral",         type: "public"  },
  { id: "infraestrutura", name: "infraestrutura", type: "public"  },
  { id: "projetos",       name: "projetos",       type: "private" },
];

const SAMPLE_DMS: DMConversation[] = [
  {
    id: "dm-juliane",
    type: "1:1",
    name: "Juliane Lino",
    participants: [
      { id: "juliane", displayName: "Juliane Lino", initials: "JL", color: "rose", status: "online" },
    ],
  },
  {
    id: "dm-grupo-infra",
    type: "group",
    name: "Equipe Infra",
    participants: [
      { id: "juliane", displayName: "Juliane Lino",     initials: "JL", color: "rose",  status: "online"  },
      { id: "caio",    displayName: "Caio Almeida",     initials: "CA", color: "blue",  status: "away"    },
      { id: "fernanda",displayName: "Fernanda Nicácio", initials: "FN", color: "teal",  status: "online"  },
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
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue([]);
    renderChat("/chat", false);

    expect(await screen.findByText("Login page")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-shell")).not.toBeInTheDocument();
  });

  it("renders chat shell for authenticated user", async () => {
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue([]);
    renderChat("/chat", true);

    expect(await screen.findByTestId("chat-shell")).toBeInTheDocument();
  });
});

// ── Shell structure ───────────────────────────────────────────────────────────

describe("ChatShell — shell structure", () => {
  it("renders the dark sidebar", async () => {
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue([]);
    renderChat();

    expect(await screen.findByTestId("chat-sidebar")).toBeInTheDocument();
  });

  it("sidebar has NIC Chat branding", async () => {
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue([]);
    renderChat();

    const sidebar = await screen.findByTestId("chat-sidebar");
    expect(sidebar).toHaveTextContent("NIC Chat");
    expect(sidebar).toHaveTextContent("Workspace NIC-Labs");
  });
});

// ── Loading state ─────────────────────────────────────────────────────────────

describe("ChatSidebar — loading state", () => {
  it("shows loading skeleton while request is pending", async () => {
    let resolveChannels: (v: Channel[]) => void;
    let resolveDMs: (v: DMConversation[]) => void;
    mockFetchChannels.mockReturnValue(new Promise((r) => (resolveChannels = r)));
    mockFetchDMs.mockReturnValue(new Promise((r) => (resolveDMs = r)));

    renderChat();

    await screen.findByTestId("chat-sidebar");
    expect(screen.getByRole("status", { name: /carregando/i })).toBeInTheDocument();

    resolveChannels!([]);
    resolveDMs!([]);
  });
});

// ── Channels ──────────────────────────────────────────────────────────────────

describe("ChatSidebar — channels", () => {
  it("renders the channels section", async () => {
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue([]);
    renderChat();

    await waitFor(() => {
      expect(screen.getByText("Canais")).toBeInTheDocument();
    });
  });

  it("renders #geral channel", async () => {
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue([]);
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toBeInTheDocument();
    });
  });

  it("renders all channels", async () => {
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue([]);
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toBeInTheDocument();
    });

    expect(screen.getByRole("option", { name: /canal infraestrutura/i })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /canal privado projetos/i })).toBeInTheDocument();
  });

  it("renders private channel indicator in accessible label", async () => {
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue([]);
    renderChat();

    await waitFor(() => {
      const privateBtn = screen.getByRole("option", { name: /privado projetos/i });
      expect(privateBtn).toBeInTheDocument();
    });
  });

  it("shows empty channels state when list is empty", async () => {
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue(SAMPLE_DMS);
    renderChat();

    await waitFor(() => {
      expect(screen.getByText(/nenhum canal disponível/i)).toBeInTheDocument();
    });
  });
});

// ── DMs ───────────────────────────────────────────────────────────────────────

describe("ChatSidebar — DMs", () => {
  it("renders the DMs section", async () => {
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue(SAMPLE_DMS);
    renderChat();

    await waitFor(() => {
      expect(screen.getByText("Mensagens diretas")).toBeInTheDocument();
    });
  });

  it("renders 1:1 DM entry", async () => {
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue(SAMPLE_DMS);
    renderChat();

    await waitFor(() => {
      expect(
        screen.getByRole("option", { name: /mensagem direta com juliane lino/i }),
      ).toBeInTheDocument();
    });
  });

  it("renders group DM indicator in accessible label", async () => {
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue(SAMPLE_DMS);
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /grupo equipe infra/i })).toBeInTheDocument();
    });
  });

  it("shows empty DMs state when list is empty", async () => {
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue([]);
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
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue([]);
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
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue(SAMPLE_DMS);
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
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue([]);
    renderChat();

    const btn = await screen.findByRole("option", { name: /canal geral/i });
    await user.click(btn);

    await waitFor(() => {
      expect(screen.getByTestId("chat-channel")).toBeInTheDocument();
    });
  });

  it("clicking a DM renders the DM route", async () => {
    const user = userEvent.setup();
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue(SAMPLE_DMS);
    renderChat();

    const btn = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    await user.click(btn);

    await waitFor(() => {
      expect(screen.getByTestId("chat-dm")).toBeInTheDocument();
    });
  });

  it("channel active on matching route param", async () => {
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue([]);
    renderChat("/chat/channel/geral");

    const btn = await screen.findByRole("option", { name: /canal geral/i });
    expect(btn).toHaveAttribute("aria-selected", "true");
  });

  it("DM active on matching route param", async () => {
    mockFetchChannels.mockResolvedValue([]);
    mockFetchDMs.mockResolvedValue(SAMPLE_DMS);
    renderChat("/chat/dm/dm-juliane");

    const btn = await screen.findByRole("option", { name: /mensagem direta com juliane lino/i });
    expect(btn).toHaveAttribute("aria-selected", "true");
  });
});

// ── Error state ───────────────────────────────────────────────────────────────

describe("ChatSidebar — error state", () => {
  it("shows error state when API fails", async () => {
    mockFetchChannels.mockRejectedValue(new Error("network failure"));
    mockFetchDMs.mockRejectedValue(new Error("network failure"));
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
    });

    expect(screen.getByText(/não foi possível carregar os canais/i)).toBeInTheDocument();
  });

  it("error state shows retry button", async () => {
    mockFetchChannels.mockRejectedValue(new Error("fail"));
    mockFetchDMs.mockRejectedValue(new Error("fail"));
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /tentar novamente/i })).toBeInTheDocument();
    });
  });

  it("retry button reloads data", async () => {
    const user = userEvent.setup();
    mockFetchChannels
      .mockRejectedValueOnce(new Error("fail"))
      .mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs
      .mockRejectedValueOnce(new Error("fail"))
      .mockResolvedValue([]);

    renderChat();

    const retryBtn = await screen.findByRole("button", { name: /tentar novamente/i });
    await user.click(retryBtn);

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toBeInTheDocument();
    });
  });
});

// ── Security ──────────────────────────────────────────────────────────────────

describe("ChatSidebar — security", () => {
  it("does not persist tokens to localStorage on render", async () => {
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue(SAMPLE_DMS);
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toBeInTheDocument();
    });

    const lsKeys = Object.keys(localStorage);
    const tokenKeys = lsKeys.filter((k) =>
      /token|access_token|refresh_token|bearer/i.test(k),
    );
    expect(tokenKeys).toHaveLength(0);
  });

  it("does not persist tokens to sessionStorage on render", async () => {
    mockFetchChannels.mockResolvedValue(SAMPLE_CHANNELS);
    mockFetchDMs.mockResolvedValue(SAMPLE_DMS);
    renderChat();

    await waitFor(() => {
      expect(screen.getByRole("option", { name: /canal geral/i })).toBeInTheDocument();
    });

    const ssKeys = Object.keys(sessionStorage);
    const tokenKeys = ssKeys.filter((k) =>
      /token|access_token|refresh_token|bearer/i.test(k),
    );
    expect(tokenKeys).toHaveLength(0);
  });
});
