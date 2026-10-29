import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, useState } from "react";
import { MemoryRouter, Outlet, Route, Routes, useLocation, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import { setTokens } from "../lib/authSession";

const { mockSearchCategory } = vi.hoisted(() => ({ mockSearchCategory: vi.fn() }));

vi.mock("./searchApi", async () => {
  const actual = await vi.importActual<typeof import("./searchApi")>("./searchApi");
  return { ...actual, searchCategory: (...args: unknown[]) => mockSearchCategory(...args) };
});

import GlobalSearchPage from "./GlobalSearchPage";
import {
  fileResult,
  groupResult,
  linkResult,
  messageResult,
  resultPage,
  userResult,
} from "./searchFixtures";
import { withRestoredSearch } from "./searchNavigation";
import type { SearchCategory } from "./searchTypes";

const FIELD = "Buscar mensagens, pessoas, canais, grupos e arquivos";

const results: Record<SearchCategory, ReturnType<typeof resultPage>> = {
  messages: resultPage([messageResult()], "more"),
  users: resultPage([userResult()]),
  channels: resultPage([]),
  groups: resultPage([groupResult(), groupResult({ id: "g2", title: "Backup Squad" })]),
  files: resultPage([fileResult()]),
  links: resultPage([linkResult()]),
};

function Elsewhere() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  return (
    <button type="button" onClick={() => navigate(-1)}>
      voltar de {pathname}
    </button>
  );
}

/** The confirmed identity the chat shell hands its routes, as the test sets it. */
let setIdentity: (identity: string | null) => void = () => {};

/** The rest of the shell's context a test sets: a failed sidebar, its retry. */
let shellContext: Record<string, unknown> = {};

function Shell({ initial }: { initial: string | null }) {
  const [sessionIdentity, set] = useState(initial);
  useEffect(() => {
    setIdentity = set;
  }, []);
  return <Outlet context={{ ...shellContext, sessionIdentity }} />;
}

function renderPage(
  entries: { pathname: string; state?: unknown }[] = [
    { pathname: "/chat/channel/geral" },
    { pathname: "/chat/search" },
  ],
  identity: string | null = "u1:w1",
) {
  return render(
    <MemoryRouter initialEntries={entries} initialIndex={entries.length - 1}>
      <Routes>
        <Route element={<Shell initial={identity} />}>
          <Route path="/chat/search" element={<GlobalSearchPage />} />
          <Route path="/chat/:kind/:id" element={<Elsewhere />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function search(text: string) {
  fireEvent.change(screen.getByRole("searchbox", { name: FIELD }), { target: { value: text } });
  await act(async () => {
    vi.advanceTimersByTime(300);
  });
  await flush();
}

beforeEach(() => {
  vi.useFakeTimers();
  // JSDOM has no layout; model a scrollable reading area for the offset assertions.
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(1000);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(300);
  shellContext = {};
  mockSearchCategory
    .mockReset()
    .mockImplementation((category: SearchCategory) => Promise.resolve(results[category]));
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("GlobalSearchPage", () => {
  it("a new session while mounted does not re-read the previous session's saved search", async () => {
    renderPage([
      {
        pathname: "/chat/search",
        state: withRestoredSearch(null, { query: "segredo", tab: "groups" }, "u1:w1"),
      },
    ]);
    await flush();
    expect(screen.getByRole("searchbox")).toHaveValue("segredo");
    act(() => setTokens("new-session"));
    await flush();
    expect(screen.getByRole("searchbox")).toHaveValue("");
    expect(screen.getByRole("tab", { name: "Tudo" })).toHaveAttribute("aria-selected", "true");
  });

  it("keeps pagination and focus intent across a failed revalidation and explicit retry", async () => {
    mockSearchCategory.mockImplementation(
      (category: SearchCategory, _query: string, options?: { cursor?: string; limit?: number }) => {
        if (category !== "groups" || options?.limit === 5) return Promise.resolve(resultPage([]));
        return Promise.resolve(
          resultPage(
            [
              groupResult({
                id: options?.cursor ? "g2" : "g1",
                title: options?.cursor ? "Segunda página" : "Primeira página",
              }),
            ],
            options?.cursor ? null : "c1",
          ),
        );
      },
    );
    renderPage();
    await search("backup");
    fireEvent.click(screen.getByRole("tab", { name: "Grupos" }));
    await flush();
    fireEvent.click(screen.getByRole("button", { name: /carregar mais/i }));
    await flush();
    screen.getByRole("tab", { name: "Grupos" }).focus();
    const root = screen.getByTestId("global-search");
    root.scrollTop = 180;
    fireEvent.scroll(root);
    act(() => setIdentity(null));
    const fetchPage = mockSearchCategory.getMockImplementation()!;
    mockSearchCategory.mockRejectedValueOnce(new Error("offline"));
    act(() => setIdentity("u1:w1"));
    await flush();
    expect(screen.queryByRole("button", { name: /Segunda página/ })).not.toBeInTheDocument();
    // Model the browser clamping a scrollport whose results were replaced by an error.
    screen.getByTestId("global-search").scrollTop = 0;
    fireEvent.scroll(screen.getByTestId("global-search"));
    mockSearchCategory.mockImplementation(fetchPage);
    fireEvent.click(screen.getByRole("button", { name: /tentar novamente/i }));
    await flush();
    expect(screen.getByRole("button", { name: /Segunda página/ })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Grupos" })).toHaveFocus();
    expect(screen.getByTestId("global-search").scrollTop).toBe(180);
  });

  it("restores the selected tab focus and reading scroll after same-owner revalidation", async () => {
    renderPage();
    await search("backup");
    fireEvent.click(screen.getByRole("tab", { name: "Grupos" }));
    await flush();
    screen.getByRole("tab", { name: "Grupos" }).focus();
    const root = screen.getByTestId("global-search");
    root.scrollTop = 180;
    fireEvent.scroll(root);
    act(() => setIdentity(null));
    act(() => setIdentity("u1:w1"));
    await flush();
    expect(screen.getByRole("tab", { name: "Grupos" })).toHaveFocus();
    expect(screen.getByTestId("global-search").scrollTop).toBe(180);
  });

  it("revalidates a paginated tab after confirmation without losing its depth or fresh cursor", async () => {
    let refreshed = false;
    mockSearchCategory.mockImplementation(
      (category: SearchCategory, _query: string, options?: { cursor?: string; limit?: number }) => {
        if (category !== "groups" || options?.limit === 5) return Promise.resolve(resultPage([]));
        if (options?.cursor)
          return Promise.resolve(
            resultPage(
              [groupResult({ id: "g2", title: "Segunda página" })],
              refreshed ? "new-2" : "old-2",
            ),
          );
        return Promise.resolve(resultPage([groupResult()], refreshed ? "new-1" : "old-1"));
      },
    );
    renderPage();
    await search("backup");
    fireEvent.click(screen.getByRole("tab", { name: "Grupos" }));
    await flush();
    fireEvent.click(screen.getByRole("button", { name: /carregar mais/i }));
    await flush();
    expect(screen.getByRole("button", { name: /Segunda página/ })).toBeInTheDocument();
    refreshed = true;
    mockSearchCategory.mockClear();
    act(() => setIdentity(null));
    expect(screen.queryByRole("button", { name: /Segunda página/ })).not.toBeInTheDocument();
    act(() => setIdentity("u1:w1"));
    await flush();
    expect(screen.getByRole("searchbox")).toHaveValue("backup");
    expect(screen.getByRole("tab", { name: "Grupos" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("button", { name: /Segunda página/ })).toBeInTheDocument();
    expect(mockSearchCategory).toHaveBeenCalledTimes(2);
    expect(mockSearchCategory).toHaveBeenLastCalledWith(
      "groups",
      "backup",
      expect.objectContaining({ cursor: "new-1" }),
    );
    fireEvent.click(screen.getByRole("button", { name: /carregar mais/i }));
    await flush();
    expect(mockSearchCategory).toHaveBeenLastCalledWith(
      "groups",
      "backup",
      expect.objectContaining({ cursor: "new-2" }),
    );
    expect(screen.getAllByRole("button", { name: /Segunda página/ })).toHaveLength(1);
  });

  it("does not steal focus moved outside the search during same-owner confirmation", async () => {
    renderPage();
    await search("backup");
    act(() => setIdentity(null));
    const external = document.createElement("button");
    document.body.append(external);
    external.focus();
    act(() => setIdentity("u1:w1"));
    await flush();
    expect(external).toHaveFocus();
    external.remove();
  });

  it("opens on Tudo with seven tabs, a focused labelled field and no request", () => {
    renderPage();
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      "Tudo",
      "Mensagens",
      "Pessoas",
      "Canais",
      "Grupos",
      "Arquivos",
      "Links",
    ]);
    expect(screen.getByRole("tab", { name: "Tudo" })).toHaveAttribute("aria-selected", "true");
    expect(tabs.every((tab) => tab.getAttribute("aria-controls") === "global-search-panel")).toBe(
      true,
    );
    expect(screen.getByRole("tabpanel")).toHaveAccessibleName("Tudo");
    expect(screen.getByRole("searchbox", { name: FIELD })).toHaveFocus();
    expect(screen.getByTestId("global-search-initial")).toHaveTextContent(
      "Buscar no NChatPesquise mensagens, pessoas, canais, grupos e arquivos.",
    );
    expect(mockSearchCategory).not.toHaveBeenCalled();
  });

  it("draws no avatar or profile of the signed-in user in the search header", async () => {
    renderPage();
    await search("backup");
    const header = screen.getByTestId("global-search-header");
    // Only the field and its clear control live there.
    expect(within(header).getAllByRole("searchbox")).toHaveLength(1);
    expect(
      within(header)
        .getAllByRole("button")
        .map((b) => b.getAttribute("aria-label")),
    ).toEqual(["Limpar busca"]);
    expect(within(header).queryByRole("img")).toBeNull();
    expect(header.querySelector("img, [class*='avatar']")).toBeNull();
    // Result cards do carry avatars — the assertion above is about the header.
    expect(screen.getByRole("tabpanel").querySelector("[class*='avatar']")).not.toBeNull();
  });

  it("stops showing the old results as soon as the field changes, before the debounce", async () => {
    renderPage();
    await search("backup");
    expect(screen.getByRole("region", { name: "Pessoas" })).toBeInTheDocument();
    mockSearchCategory.mockClear();

    fireEvent.change(screen.getByRole("searchbox", { name: FIELD }), {
      target: { value: "outra" },
    });
    await act(async () => {
      vi.advanceTimersByTime(100);
    });

    expect(screen.getByRole("searchbox", { name: FIELD })).toHaveValue("outra");
    expect(screen.queryByRole("region", { name: "Pessoas" })).toBeNull();
    expect(screen.queryByText("Juliane Lino")).toBeNull();
    expect(screen.getByTestId("global-search-pending")).toBeInTheDocument();
    expect(mockSearchCategory).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(200);
    });
    await flush();
    expect(mockSearchCategory).toHaveBeenCalledTimes(6);
    expect(mockSearchCategory.mock.calls.every(([, query]) => query === "outra")).toBe(true);
  });

  it("shows the initial state at once when the field is emptied", async () => {
    renderPage();
    await search("backup");
    fireEvent.change(screen.getByRole("searchbox", { name: FIELD }), { target: { value: "  " } });
    expect(screen.getByTestId("global-search-initial")).toBeInTheDocument();
  });

  it("shows only sections with results, in fixed order, with exact counts", async () => {
    renderPage();
    await search("backup");

    const headings = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
    expect(headings).toEqual(["Mensagens", "Pessoas", "Grupos", "Arquivos", "Links"]);
    const people = screen.getByRole("region", { name: "Pessoas" });
    expect(within(people).getByText("1 resultado")).toBeInTheDocument();
    const groups = screen.getByRole("region", { name: "Grupos" });
    expect(within(groups).getByText("2 resultados")).toBeInTheDocument();
    // "Mensagens" has more than it shows: no number, a "Ver todos" instead.
    const messages = screen.getByRole("region", { name: "Mensagens" });
    expect(within(messages).queryByText(/resultado/)).toBeNull();
    expect(within(people).queryByRole("button", { name: /Ver todos/ })).toBeNull();
  });

  it("Ver todos switches to the category's tab and keeps the query", async () => {
    renderPage();
    await search("backup");
    fireEvent.click(screen.getByRole("button", { name: "Ver todos os resultados em Mensagens" }));
    await flush();

    expect(screen.getByRole("tab", { name: "Mensagens" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("searchbox", { name: FIELD })).toHaveValue("backup");
    expect(mockSearchCategory).toHaveBeenLastCalledWith(
      "messages",
      "backup",
      expect.objectContaining({ limit: 20 }),
    );
    expect(screen.getByRole("button", { name: "Carregar mais" })).toBeInTheDocument();
  });

  it("says so when no category found anything", async () => {
    mockSearchCategory.mockResolvedValue(resultPage([]));
    renderPage();
    await search("inexistente");
    expect(screen.getByTestId("global-search-empty")).toHaveTextContent(
      "Nenhum resultado para “inexistente”.",
    );
  });

  it("keeps the other sections when one fails, and retries only that one", async () => {
    mockSearchCategory.mockImplementation((category: SearchCategory) =>
      category === "users"
        ? Promise.reject(new ApiRequestError(503, "internal", "x"))
        : Promise.resolve(results[category]),
    );
    renderPage();
    await search("backup");

    const people = screen.getByRole("region", { name: "Pessoas" });
    expect(within(people).getByRole("alert")).toHaveTextContent("Não foi possível buscar agora.");
    expect(screen.getByRole("region", { name: "Grupos" })).toBeInTheDocument();

    mockSearchCategory.mockClear().mockResolvedValue(resultPage([userResult()]));
    fireEvent.click(within(people).getByRole("button", { name: "Tentar novamente" }));
    await flush();
    expect(mockSearchCategory).toHaveBeenCalledTimes(1);
    expect(
      within(screen.getByRole("region", { name: "Pessoas" })).getByText("1 resultado"),
    ).toBeInTheDocument();
  });

  it("shows skeletons while loading and announces it once", async () => {
    mockSearchCategory.mockReturnValue(new Promise(() => {}));
    renderPage();
    await search("backup");
    expect(screen.getAllByTestId("global-search-skeleton")).toHaveLength(6);
    expect(screen.getByRole("status")).toHaveTextContent("Buscando…");
  });

  it("moves between tabs with the arrow keys, Home and End", () => {
    renderPage();
    const all = screen.getByRole("tab", { name: "Tudo" });
    all.focus();
    fireEvent.keyDown(all, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: "Mensagens" })).toHaveFocus();
    expect(screen.getByRole("tab", { name: "Mensagens" })).toHaveAttribute("tabindex", "0");
    fireEvent.keyDown(document.activeElement!, { key: "End" });
    expect(screen.getByRole("tab", { name: "Links" })).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(document.activeElement!, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: "Tudo" })).toHaveFocus();
    fireEvent.keyDown(document.activeElement!, { key: "ArrowLeft" });
    expect(screen.getByRole("tab", { name: "Links" })).toHaveFocus();
    fireEvent.keyDown(document.activeElement!, { key: "Home" });
    expect(screen.getByRole("tab", { name: "Tudo" })).toHaveFocus();
    fireEvent.keyDown(document.activeElement!, { key: "a" });
    expect(screen.getByRole("tab", { name: "Tudo" })).toHaveAttribute("aria-selected", "true");
  });

  it("follows the APG tab order: field, clear, the selected tab only, then the panel", async () => {
    renderPage();
    await search("backup");
    // userEvent drives real timers; the debounce is already behind us.
    vi.useRealTimers();
    const user = userEvent.setup();
    const field = screen.getByRole("searchbox", { name: FIELD });
    field.focus();

    await user.tab();
    expect(screen.getByRole("button", { name: "Limpar busca" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("tab", { name: "Tudo" })).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "Mensagens" })).toHaveFocus();
    expect(screen.getByRole("tab", { name: "Mensagens" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "Tudo" })).toHaveAttribute("aria-selected", "false");
    expect(screen.getByRole("tab", { name: "Tudo" })).toHaveAttribute("tabindex", "-1");
    expect(screen.getByRole("tabpanel")).toHaveAccessibleName("Mensagens");

    // One Tab leaves the whole tab list — no stop on the unselected tabs.
    await user.tab();
    expect(screen.getByRole("tabpanel")).toHaveFocus();
    await user.tab({ shift: true });
    expect(screen.getByRole("tab", { name: "Mensagens" })).toHaveFocus();
  });

  it("clears the query back to the initial state", async () => {
    renderPage();
    await search("backup");
    fireEvent.click(screen.getByRole("button", { name: "Limpar busca" }));
    await act(async () => {
      vi.advanceTimersByTime(300);
    });
    expect(screen.getByRole("searchbox", { name: FIELD })).toHaveFocus();
    expect(screen.getByTestId("global-search-initial")).toBeInTheDocument();
  });

  it("paginates, and shows empty and error states inside a tab", async () => {
    renderPage();
    await search("backup");
    fireEvent.click(screen.getByRole("tab", { name: "Canais" }));
    await flush();
    expect(screen.getByTestId("global-search-empty")).toHaveTextContent(
      "Nenhum resultado para “backup”.",
    );

    mockSearchCategory.mockRejectedValueOnce(new ApiRequestError(400, "bad_request", "x"));
    fireEvent.click(screen.getByRole("tab", { name: "Arquivos" }));
    await flush();
    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível interpretar essa busca.");
    fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    await flush();
    expect(screen.getByText("1 resultado para “backup”")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Mensagens" }));
    await flush();
    mockSearchCategory.mockRejectedValueOnce(new ApiRequestError(403, "forbidden", "x"));
    fireEvent.click(screen.getByRole("button", { name: "Carregar mais" }));
    await flush();
    expect(screen.getByRole("alert")).toHaveTextContent("Você não tem permissão");
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
  });

  it("delegates Escape to the shell's safe contextual return", () => {
    const goBack = vi.fn(() => false);
    shellContext = { goBack };
    renderPage();
    fireEvent.keyDown(screen.getByRole("searchbox", { name: FIELD }), { key: "Escape" });
    expect(goBack).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("searchbox", { name: FIELD })).toBeInTheDocument();
  });

  it("comes back from a result with the same query, tab and results", async () => {
    renderPage();
    await search("backup");
    fireEvent.click(screen.getByRole("tab", { name: "Grupos" }));
    await flush();
    fireEvent.click(screen.getByRole("button", { name: /Grupo Backup Squad/ }));

    fireEvent.click(screen.getByRole("button", { name: "voltar de /chat/dm/g2" }));
    await flush();

    expect(screen.getByRole("searchbox", { name: FIELD })).toHaveValue("backup");
    expect(screen.getByRole("tab", { name: "Grupos" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("button", { name: /Grupo Backup Squad/ })).toBeInTheDocument();
  });

  it("Links: a section in Tudo, its own paginated tab, and no section when empty", async () => {
    const more = linkResult({ id: "m8:k", messageId: "m8", url: "https://docs.example.com/b" });
    mockSearchCategory.mockImplementation((category: SearchCategory, _q: string, options) =>
      Promise.resolve(
        category !== "links"
          ? results[category]
          : options?.cursor
            ? resultPage([more])
            : resultPage([linkResult()], options?.limit === 5 ? null : "links-2"),
      ),
    );
    renderPage();
    await search("docs.example.com");
    const section = screen.getByRole("region", { name: "Links" });
    expect(within(section).getByText("1 resultado")).toBeInTheDocument();
    expect(within(section).getByRole("button", { name: /Link docs\.example\.com/ })).toBeVisible();

    fireEvent.click(screen.getByRole("tab", { name: "Links" }));
    await flush();
    const list = screen.getByRole("list", { name: "Links" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Carregar mais" }));
    await flush();
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(mockSearchCategory).toHaveBeenLastCalledWith(
      "links",
      "docs.example.com",
      expect.objectContaining({ cursor: "links-2", limit: 20 }),
    );

    mockSearchCategory.mockImplementation((category: SearchCategory) =>
      Promise.resolve(category === "links" ? resultPage([]) : results[category]),
    );
    fireEvent.click(screen.getByRole("tab", { name: "Tudo" }));
    await search("backup");
    expect(screen.queryByRole("region", { name: "Links" })).toBeNull();
    expect(screen.getByRole("region", { name: "Arquivos" })).toBeInTheDocument();
  });

  it("Links on a search-service without the route is an explicit, retryable error", async () => {
    mockSearchCategory.mockImplementation((category: SearchCategory) =>
      category === "links"
        ? Promise.reject(new ApiRequestError(404, "not_found", "x"))
        : Promise.resolve(results[category]),
    );
    renderPage();
    await search("backup");
    const section = screen.getByRole("region", { name: "Links" });
    expect(within(section).getByRole("alert")).toHaveTextContent(
      "Esta busca ainda não está disponível.",
    );
    expect(screen.getByRole("region", { name: "Mensagens" })).toBeInTheDocument();

    mockSearchCategory.mockClear().mockResolvedValue(resultPage([linkResult()]));
    fireEvent.click(within(section).getByRole("button", { name: "Tentar novamente" }));
    await flush();
    expect(mockSearchCategory).toHaveBeenCalledTimes(1);
    expect(mockSearchCategory).toHaveBeenCalledWith("links", "backup", expect.anything());
    expect(
      within(screen.getByRole("region", { name: "Links" })).getByText("1 resultado"),
    ).toBeInTheDocument();
  });

  describe("whose search it is (CQ-07)", () => {
    const savedBy = (owner: string | null) => [
      { pathname: "/chat/channel/geral" },
      {
        pathname: "/chat/search",
        state: withRestoredSearch(null, { query: "segredo", tab: "groups" }, owner),
      },
    ];
    const field = () => screen.queryByRole("searchbox", { name: FIELD });
    const confirming = () => screen.queryByText("Confirmando sua sessão…");

    it("brings a search back for the identity that left it, and for nobody else", async () => {
      renderPage(savedBy("u1:w1"), "u1:w1");
      await flush();
      expect(field()).toHaveValue("segredo");
    });

    it.each([
      ["another user", "u2:w1"],
      ["another workspace", "u1:w2"],
    ])("never brings back a search left by %s", async (_, identity) => {
      renderPage(savedBy("u1:w1"), identity);
      await flush();
      expect(field()).toHaveValue("");
      expect(screen.getByRole("tab", { name: "Tudo" })).toHaveAttribute("aria-selected", "true");
      expect(mockSearchCategory).not.toHaveBeenCalled();
    });

    it("never brings back a search nobody confirmed left, nor one without an owner", async () => {
      const first = renderPage(savedBy(null), "u1:w1");
      await flush();
      expect(field()).toHaveValue("");
      first.unmount();

      renderPage(
        [{ pathname: "/chat/search", state: { globalSearch: { query: "x", tab: "all" } } }],
        "u1:w1",
      );
      await flush();
      expect(field()).toHaveValue("");
    });

    it("landing while the identity is in doubt: no field to type into, then the owner's search", async () => {
      renderPage(savedBy("u1:w1"), null);
      expect(field()).not.toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveTextContent("Confirmando sua sessão…");
      expect(screen.getByTestId("global-search")).toHaveAttribute("aria-busy", "true");
      expect(mockSearchCategory).not.toHaveBeenCalled();

      act(() => setIdentity("u1:w1"));
      await flush();
      expect(field()).toHaveValue("segredo");
      expect(field()).toHaveFocus();
      expect(screen.getByRole("tab", { name: "Grupos" })).toHaveAttribute("aria-selected", "true");
    });

    it("landing while the identity is in doubt, confirmed as another: never restored", async () => {
      renderPage(savedBy("u1:w1"), null);
      act(() => setIdentity("u2:w1"));
      await flush();
      expect(field()).toHaveValue("");
      expect(mockSearchCategory).not.toHaveBeenCalled();
    });

    it("a mounted search hides while confirming, keeps query and tab, and searches again once confirmed", async () => {
      renderPage();
      await search("backup");
      fireEvent.click(screen.getByRole("tab", { name: "Grupos" }));
      await flush();
      mockSearchCategory.mockClear();

      act(() => setIdentity(null));
      expect(field()).not.toBeInTheDocument();
      expect(screen.queryByText("backup")).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /Backup Squad/ })).not.toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveTextContent("Confirmando sua sessão…");
      await flush();
      expect(mockSearchCategory).not.toHaveBeenCalled();

      act(() => setIdentity("u1:w1"));
      expect(confirming()).not.toBeInTheDocument();
      expect(field()).toHaveValue("backup");
      await flush();
      expect(field()).toHaveFocus();
      expect(screen.getByRole("tab", { name: "Grupos" })).toHaveAttribute("aria-selected", "true");
      await flush();
      // The results are asked for again, under the identity just confirmed.
      expect(mockSearchCategory).toHaveBeenCalledWith("groups", "backup", expect.anything());
      expect(screen.getByRole("button", { name: /Backup Squad/ })).toBeInTheDocument();
    });

    it("an answer requested before the doubt is dropped, even when the same user is confirmed (CQ-10)", async () => {
      renderPage();
      let answerLate!: (page: ReturnType<typeof resultPage>) => void;
      mockSearchCategory.mockImplementation((category: SearchCategory) =>
        category === "groups"
          ? new Promise((resolve) => (answerLate = resolve))
          : Promise.resolve(resultPage([])),
      );
      await search("backup");

      act(() => setIdentity(null));
      await act(async () => answerLate(results.groups));
      mockSearchCategory.mockImplementation(() => Promise.resolve(resultPage([])));
      act(() => setIdentity("u1:w1"));
      await flush();

      expect(field()).toHaveValue("backup");
      expect(screen.queryByRole("button", { name: /Backup Squad/ })).not.toBeInTheDocument();
    });

    it("a query committed while in doubt is searched only once confirmed (CQ-10)", async () => {
      renderPage();
      fireEvent.change(screen.getByRole("searchbox", { name: FIELD }), {
        target: { value: "backup" },
      });
      act(() => setIdentity(null));
      await act(async () => {
        vi.advanceTimersByTime(300);
      });
      await flush();
      expect(mockSearchCategory).not.toHaveBeenCalled();

      act(() => setIdentity("u1:w1"));
      await flush();
      expect(field()).toHaveValue("backup");
      expect(mockSearchCategory).toHaveBeenCalledWith("messages", "backup", expect.anything());
    });

    it("what the reader typed over a restored search survives the same user's confirmation (CQ-11)", async () => {
      renderPage(savedBy("u1:w1"), "u1:w1");
      await flush();
      expect(field()).toHaveValue("segredo");
      fireEvent.change(field()!, { target: { value: "texto-novo-digitado" } });

      act(() => setIdentity(null));
      act(() => setIdentity("u1:w1"));
      await flush();
      expect(field()).toHaveValue("texto-novo-digitado");
      expect(screen.getByRole("tab", { name: "Grupos" })).toHaveAttribute("aria-selected", "true");
    });

    it("no identity because the sidebar failed: a retry, no field, no request; then usable (CQ-10)", async () => {
      const retrySidebar = vi.fn();
      shellContext = {
        sidebarLoading: false,
        currentUserId: "",
        refreshConversations: retrySidebar,
      };
      renderPage(undefined, null);
      expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível confirmar sua sessão.");
      expect(field()).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
      expect(retrySidebar).toHaveBeenCalledTimes(1);

      act(() => setIdentity("u1:w1"));
      await search("backup");
      expect(mockSearchCategory).toHaveBeenCalledWith("messages", "backup", expect.anything());
    });

    it("a mounted search starts over for another user, and a late answer for the old query never lands", async () => {
      renderPage();
      await search("backup");
      let answerLate!: (page: ReturnType<typeof resultPage>) => void;
      mockSearchCategory.mockImplementation(() => new Promise((resolve) => (answerLate = resolve)));
      await search("backup squad");
      const staleRequests = mockSearchCategory.mock.calls.length;

      act(() => setIdentity(null));
      act(() => setIdentity("u2:w1"));
      await flush();
      expect(field()).toHaveValue("");
      expect(screen.getByTestId("global-search-initial")).toBeInTheDocument();

      await act(async () => answerLate(results.groups));
      await flush();
      expect(screen.queryByRole("button", { name: /Backup Squad/ })).not.toBeInTheDocument();
      expect(field()).toHaveValue("");
      expect(mockSearchCategory).toHaveBeenCalledTimes(staleRequests);

      mockSearchCategory.mockImplementation((category: SearchCategory) =>
        Promise.resolve(results[category]),
      );
      await search("novo");
      expect(field()).toHaveValue("novo");
    });

    it("leaving for a result records the search with its owner", async () => {
      renderPage();
      await search("backup");
      fireEvent.click(screen.getByRole("tab", { name: "Grupos" }));
      await flush();
      fireEvent.click(screen.getByRole("button", { name: /Grupo Backup Squad/ }));

      // Back as another user: the entry is U1's, so nothing comes back.
      act(() => setIdentity("u2:w1"));
      fireEvent.click(screen.getByRole("button", { name: "voltar de /chat/dm/g2" }));
      await flush();
      expect(field()).toHaveValue("");
    });
  });
});
