import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
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
    reactions: [],
    isFavorited: true,
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

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/chat/favorites"]}>
      <FavoritesPage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
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
    expect(mockFetchFavorites).toHaveBeenLastCalledWith("cursor-1");
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
});
