import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, useEffect, useState } from "react";
import { MemoryRouter, Outlet, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { FavoriteItem, FavoritesPage as FavoritesPageData } from "./chatTypes";

const { mockFetchFavorites, mockUnfavoriteMessage } = vi.hoisted(() => ({
  mockFetchFavorites: vi.fn(),
  mockUnfavoriteMessage: vi.fn(),
}));

vi.mock("./chatApi", () => ({
  fetchFavorites: (...args: unknown[]) => mockFetchFavorites(...args),
  unfavoriteMessage: (...args: unknown[]) => mockUnfavoriteMessage(...args),
}));

import FavoritesPage from "./FavoritesPage";

// ── Fixtures ──────────────────────────────────────────────────────────────────

const makeFavorite = (overrides: Partial<FavoriteItem> = {}): FavoriteItem => ({
  message: {
    id: "msg-1",
    senderId: "user-1",
    senderDisplayName: "Ana Souza",
    senderEmail: "ana@example.com",
    kind: "user",
    bodyText: "mensagem importante",
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    createdAt: "2025-01-15T10:00:00Z",
    updatedAt: "2025-01-15T10:00:00Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: true,
    isForwarded: false,
    ...(overrides.message ?? {}),
  },
  channelId: "ch-1",
  dmConversationId: "",
  favoritedAt: "2025-02-01T12:00:00Z",
  ...overrides,
});

const page = (favorites: FavoriteItem[], nextCursor = ""): FavoritesPageData => ({
  favorites,
  nextCursor,
});

/** The confirmed identity the chat shell hands its routes, as the test sets it. */
let setIdentity: (identity: string | null) => void = () => {};

function Shell({ initial }: { initial: string | null }) {
  const [sessionIdentity, set] = useState(initial);
  useEffect(() => {
    setIdentity = set;
  }, []);
  return <Outlet context={{ sessionIdentity }} />;
}

function renderPage(identity: string | null = "u1:w1") {
  return render(
    <MemoryRouter initialEntries={["/chat/favorites"]}>
      <Routes>
        <Route element={<Shell initial={identity} />}>
          <Route path="/chat/favorites" element={<FavoritesPage />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

const privateFavorite = (sender: string, body: string, id = `msg-${sender}`) =>
  makeFavorite({
    message: { ...makeFavorite().message, id, senderDisplayName: sender, bodyText: body },
    channelId: "",
    dmConversationId: `dm-${sender}`,
  });

beforeEach(() => {
  vi.clearAllMocks();
  // Held answers a test never released must not reach the next one.
  mockFetchFavorites.mockReset();
});

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("FavoritesPage", () => {
  it("shows loading, then the favorites list", async () => {
    mockFetchFavorites.mockResolvedValue(page([makeFavorite()]));
    renderPage();
    expect(screen.getByRole("status")).toHaveTextContent(/carregando/i);
    expect(await screen.findByTestId("favorite-item")).toBeInTheDocument();
    expect(screen.getByText("Ana Souza")).toBeInTheDocument();
    expect(screen.getByText(/mensagem importante/)).toBeInTheDocument();
  });

  it("shows the empty state when there are no favorites", async () => {
    mockFetchFavorites.mockResolvedValue(page([]));
    renderPage();
    expect(await screen.findByTestId("chat-favorites-empty")).toBeInTheDocument();
  });

  it("shows an error state with retry that reloads", async () => {
    mockFetchFavorites.mockRejectedValueOnce(new Error("boom"));
    mockFetchFavorites.mockResolvedValueOnce(page([makeFavorite()]));
    renderPage();
    const retry = await screen.findByRole("button", { name: /tentar novamente/i });
    await userEvent.click(retry);
    expect(await screen.findByTestId("favorite-item")).toBeInTheDocument();
  });

  it("renders the RF-14 placeholder for removed messages", async () => {
    const removed = makeFavorite({
      message: {
        ...makeFavorite().message,
        id: "msg-del",
        isRemoved: true,
        status: "deleted",
        bodyText: "",
      },
    });
    mockFetchFavorites.mockResolvedValue(page([removed]));
    renderPage();
    expect(await screen.findByText("Mensagem removida.")).toBeInTheDocument();
  });

  it("links to the source channel or DM conversation", async () => {
    const dmFav = makeFavorite({
      message: { ...makeFavorite().message, id: "msg-dm" },
      channelId: "",
      dmConversationId: "dm-9",
    });
    mockFetchFavorites.mockResolvedValue(page([makeFavorite(), dmFav]));
    renderPage();
    const channelLink = await screen.findByRole("link", { name: /ver no canal/i });
    expect(channelLink).toHaveAttribute("href", "/chat/channel/ch-1");
    expect(screen.getByRole("link", { name: /ver na conversa/i })).toHaveAttribute(
      "href",
      "/chat/dm/dm-9",
    );
  });

  it("removes an item after unfavoriting", async () => {
    mockFetchFavorites.mockResolvedValue(page([makeFavorite()]));
    mockUnfavoriteMessage.mockResolvedValue(undefined);
    renderPage();
    const remove = await screen.findByRole("button", { name: /remover dos favoritos/i });
    await userEvent.click(remove);
    await waitFor(() => expect(screen.queryByTestId("favorite-item")).not.toBeInTheDocument());
    expect(mockUnfavoriteMessage).toHaveBeenCalledWith("msg-1");
  });

  it("keeps the item and shows an error when unfavoriting fails", async () => {
    mockFetchFavorites.mockResolvedValue(page([makeFavorite()]));
    mockUnfavoriteMessage.mockRejectedValue(new Error("boom"));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /remover dos favoritos/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/não foi possível remover/i);
    expect(screen.getByTestId("favorite-item")).toBeInTheDocument();
  });

  it("loads the next page via Carregar mais and appends items", async () => {
    const older = makeFavorite({ message: { ...makeFavorite().message, id: "msg-old" } });
    mockFetchFavorites.mockResolvedValueOnce(page([makeFavorite()], "cursor-1"));
    mockFetchFavorites.mockResolvedValueOnce(page([older]));
    renderPage();
    const more = await screen.findByRole("button", { name: /carregar mais/i });
    await userEvent.click(more);
    await waitFor(() => expect(screen.getAllByTestId("favorite-item")).toHaveLength(2));
    expect(mockFetchFavorites).toHaveBeenLastCalledWith("cursor-1", expect.any(AbortSignal));
    expect(screen.queryByRole("button", { name: /carregar mais/i })).not.toBeInTheDocument();
  });

  it("shows an error and keeps the button when loading more fails", async () => {
    mockFetchFavorites.mockResolvedValueOnce(page([makeFavorite()], "cursor-1"));
    mockFetchFavorites.mockRejectedValueOnce(new Error("boom"));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /carregar mais/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/não foi possível carregar mais/i);
    expect(screen.getByRole("button", { name: /carregar mais/i })).toBeInTheDocument();
  });

  describe("whose favourites they are (CQ-09)", () => {
    it("requests nothing and shows nothing before an identity is confirmed", async () => {
      mockFetchFavorites.mockResolvedValue(page([privateFavorite("U2", "do u2")]));
      renderPage(null);
      expect(screen.getByRole("status")).toHaveTextContent("Confirmando sua sessão…");
      expect(mockFetchFavorites).not.toHaveBeenCalled();

      act(() => setIdentity("u2:w1"));
      expect(await screen.findByText("U2")).toBeInTheDocument();
    });

    it("a loaded list leaves the screen while confirming; another user loads their own", async () => {
      mockFetchFavorites.mockResolvedValueOnce(
        page([privateFavorite("Remetente U1", "segredo-u1")]),
      );
      renderPage();
      expect(await screen.findByText(/segredo-u1/)).toBeInTheDocument();

      act(() => setIdentity(null));
      expect(screen.queryByText(/segredo-u1/)).not.toBeInTheDocument();
      expect(screen.queryByText("Remetente U1")).not.toBeInTheDocument();
      expect(screen.queryByRole("link", { name: /ver na conversa/i })).not.toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveTextContent("Confirmando sua sessão…");

      mockFetchFavorites.mockResolvedValueOnce(page([privateFavorite("Remetente U2", "do-u2")]));
      act(() => setIdentity("u2:w1"));
      expect(await screen.findByText(/do-u2/)).toBeInTheDocument();
      expect(screen.queryByText(/segredo-u1/)).not.toBeInTheDocument();
      expect(screen.queryByText("Remetente U1")).not.toBeInTheDocument();
    });

    it("a late answer for the previous identity never lands", async () => {
      const late = deferred<ReturnType<typeof page>>();
      mockFetchFavorites.mockReturnValueOnce(late.promise);
      renderPage();
      const [, signal] = mockFetchFavorites.mock.calls[0] as [unknown, AbortSignal];

      act(() => setIdentity(null));
      expect(signal.aborted).toBe(true);
      mockFetchFavorites.mockResolvedValueOnce(page([]));
      act(() => setIdentity("u2:w1"));
      expect(await screen.findByTestId("chat-favorites-empty")).toBeInTheDocument();

      await act(async () => late.resolve(page([privateFavorite("Remetente U1", "segredo-u1")])));
      expect(screen.queryByText(/segredo-u1/)).not.toBeInTheDocument();
      expect(screen.getByTestId("chat-favorites-empty")).toBeInTheDocument();
    });

    it("a late next page for the previous identity never lands", async () => {
      const more = deferred<ReturnType<typeof page>>();
      mockFetchFavorites
        .mockResolvedValueOnce(page([privateFavorite("A", "primeira")], "c1"))
        .mockReturnValueOnce(more.promise)
        .mockResolvedValueOnce(page([]));
      renderPage();
      await userEvent.click(await screen.findByRole("button", { name: /carregar mais/i }));

      act(() => setIdentity(null));
      act(() => setIdentity("u2:w1"));
      expect(await screen.findByTestId("chat-favorites-empty")).toBeInTheDocument();
      await act(async () => more.resolve(page([privateFavorite("Remetente U1", "segredo-u1")])));
      expect(screen.queryByText(/segredo-u1/)).not.toBeInTheDocument();
      expect(screen.queryByText("Remetente U1")).not.toBeInTheDocument();
    });

    it("a next page answering after its list was reloaded is dropped", async () => {
      const more = deferred<ReturnType<typeof page>>();
      mockFetchFavorites
        .mockResolvedValueOnce(page([privateFavorite("A", "primeira")], "c1"))
        .mockReturnValueOnce(more.promise)
        .mockResolvedValueOnce(page([privateFavorite("A", "primeira")], "c1"));
      renderPage();
      await userEvent.click(await screen.findByRole("button", { name: /carregar mais/i }));
      const [, signal] = mockFetchFavorites.mock.calls[1] as [unknown, AbortSignal];

      act(() => setIdentity(null));
      expect(signal.aborted).toBe(true);
      act(() => setIdentity("u1:w1"));
      expect(await screen.findByText(/primeira/)).toBeInTheDocument();
      await act(async () => more.resolve(page([privateFavorite("B", "segunda")])));
      expect(screen.queryByText(/segunda/)).not.toBeInTheDocument();
    });

    it("an answer for a superseded load never overwrites the current one", async () => {
      // StrictMode runs the load twice; the first, aborted, answers last.
      const first = deferred<ReturnType<typeof page>>();
      mockFetchFavorites
        .mockReturnValueOnce(first.promise)
        .mockResolvedValueOnce(page([privateFavorite("A", "atual")]));
      render(
        <StrictMode>
          <MemoryRouter initialEntries={["/chat/favorites"]}>
            <Routes>
              <Route element={<Shell initial="u1:w1" />}>
                <Route path="/chat/favorites" element={<FavoritesPage />} />
              </Route>
            </Routes>
          </MemoryRouter>
        </StrictMode>,
      );
      expect(await screen.findByText(/atual/)).toBeInTheDocument();
      await act(async () => first.resolve(page([privateFavorite("B", "superada")])));
      expect(screen.queryByText(/superada/)).not.toBeInTheDocument();
      expect(screen.getByText(/atual/)).toBeInTheDocument();
    });

    it("another identity confirmed with no doubt in between still starts over", async () => {
      mockFetchFavorites.mockResolvedValueOnce(
        page([privateFavorite("Remetente U1", "segredo-u1")]),
      );
      renderPage();
      expect(await screen.findByText(/segredo-u1/)).toBeInTheDocument();

      mockFetchFavorites.mockResolvedValueOnce(page([privateFavorite("Remetente U2", "do-u2")]));
      act(() => setIdentity("u2:w1"));
      expect(screen.queryByText(/segredo-u1/)).not.toBeInTheDocument();
      expect(await screen.findByText(/do-u2/)).toBeInTheDocument();
    });

    it("the same user confirmed again gets their list back", async () => {
      mockFetchFavorites.mockResolvedValue(page([privateFavorite("A", "minha")]));
      renderPage();
      expect(await screen.findByText(/minha/)).toBeInTheDocument();

      act(() => setIdentity(null));
      act(() => setIdentity("u1:w1"));
      expect(await screen.findByText(/minha/)).toBeInTheDocument();
      expect(mockFetchFavorites).toHaveBeenCalledTimes(2);
    });
  });
});
