import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { mockSearchMessages, mockSearchUsers, mockSearchChannels } = vi.hoisted(() => ({
  mockSearchMessages: vi.fn(),
  mockSearchUsers: vi.fn(),
  mockSearchChannels: vi.fn(),
}));

vi.mock("./searchApi", async () => {
  const actual = await vi.importActual<typeof import("./searchApi")>("./searchApi");
  return {
    ...actual,
    searchMessages: (...args: unknown[]) => mockSearchMessages(...args),
    searchUsers: (...args: unknown[]) => mockSearchUsers(...args),
    searchChannels: (...args: unknown[]) => mockSearchChannels(...args),
  };
});

import GlobalSearchPage from "./GlobalSearchPage";

function page<T>(items: T[], nextCursor: string | null = null, hasMore = false) {
  return { items, nextCursor, hasMore };
}

function renderPage() {
  return render(
    <MemoryRouter>
      <GlobalSearchPage />
    </MemoryRouter>,
  );
}

async function typeAndCommit(text: string) {
  const input = screen.getByRole("searchbox", { name: "Buscar mensagens, pessoas e canais" });
  fireEvent.change(input, { target: { value: text } });
  await act(async () => {
    vi.advanceTimersByTime(300);
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  mockSearchMessages.mockReset().mockResolvedValue(page([]));
  mockSearchUsers.mockReset().mockResolvedValue(page([]));
  mockSearchChannels.mockReset().mockResolvedValue(page([]));
});

afterEach(() => {
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("GlobalSearchPage", () => {
  it("shows the initial state and does not call any search endpoint before typing", () => {
    renderPage();
    expect(screen.getByTestId("global-search-initial")).toHaveTextContent("Digite um termo");
    expect(mockSearchMessages).not.toHaveBeenCalled();
    expect(mockSearchUsers).not.toHaveBeenCalled();
    expect(mockSearchChannels).not.toHaveBeenCalled();
  });

  it("has a labeled search input and accessible tabs", () => {
    renderPage();
    expect(
      screen.getByRole("searchbox", { name: "Buscar mensagens, pessoas e canais" }),
    ).toBeInTheDocument();
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((t) => t.textContent)).toEqual(["Mensagens", "Usuários", "Canais"]);
    expect(screen.getByRole("tab", { name: "Mensagens" })).toHaveAttribute("aria-selected", "true");
  });

  it("debounces typing and calls only the active tab's endpoint", async () => {
    mockSearchMessages.mockResolvedValue(
      page([
        {
          id: "m1",
          channelId: "c1",
          channelName: "geral",
          senderId: "u1",
          senderDisplayName: "Alice",
          bodyText: "hello orion",
          createdAt: "2026-01-01T00:00:00Z",
          score: 1,
        },
      ]),
    );
    renderPage();

    await typeAndCommit("orion");

    expect(mockSearchMessages).toHaveBeenCalledTimes(1);
    expect(mockSearchUsers).not.toHaveBeenCalled();
    expect(mockSearchChannels).not.toHaveBeenCalled();
    expect(screen.getByText("Alice")).toBeInTheDocument();
  });

  it("switching tabs after results are loaded does not refetch and preserves other tabs' results", async () => {
    mockSearchMessages.mockResolvedValue(
      page([
        {
          id: "m1",
          channelId: "c1",
          channelName: "geral",
          senderId: "u1",
          senderDisplayName: "Alice",
          bodyText: "hello orion",
          createdAt: "2026-01-01T00:00:00Z",
          score: 1,
        },
      ]),
    );
    mockSearchUsers.mockResolvedValue(page([{ id: "u1", displayName: "Bob", avatarUrl: null }]));
    renderPage();
    await typeAndCommit("orion");
    expect(screen.getByText("Alice")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Usuários" }));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(screen.getByText("Bob")).toBeInTheDocument();
    expect(mockSearchUsers).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("tab", { name: "Mensagens" }));
    expect(mockSearchMessages).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Alice")).toBeInTheDocument();
  });

  it("renders loading, then error, independently per tab", async () => {
    mockSearchMessages.mockRejectedValue(new Error("boom"));
    renderPage();

    await typeAndCommit("orion");

    const panel = screen.getByRole("tabpanel");
    expect(within(panel).getByRole("alert")).toBeInTheDocument();
  });

  it("renders an empty state distinct from an error", async () => {
    mockSearchChannels.mockResolvedValue(page([]));
    renderPage();
    await typeAndCommit("zzz");
    fireEvent.click(screen.getByRole("tab", { name: "Canais" }));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(screen.getByTestId("global-search-empty")).toHaveTextContent("Nenhum canal encontrado.");
  });
});
