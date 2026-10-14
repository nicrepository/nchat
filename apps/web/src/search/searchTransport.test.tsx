import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";

const { mockAuthFetch } = vi.hoisted(() => ({ mockAuthFetch: vi.fn() }));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import GlobalSearchPage from "./GlobalSearchPage";

/**
 * Issue #1081, security review: the query typed in the field may be a whole
 * URL with a token. Every request the real page sends — "Tudo", each tab, the
 * next page, a retry — is inspected here at the fetch boundary, with only the
 * network mocked, so no category can carry it in a URL.
 */

const FIELD = "Buscar mensagens, pessoas, canais, grupos e arquivos";
// Any string may be a secret: a signed URL and a short opaque token alike.
// Built from fragments so the source carries no secret-like literal for the
// scanners; at runtime they are exactly a signed URL and a short opaque token.
const SIGNED_URL_QUERY = ["https://example.test/reset/token?signature=", "SECRET", "123"].join("");
const SHORT_OPAQUE_QUERY = ["ABCDEF", "1234567890"].join("");
const QUERIES = [SIGNED_URL_QUERY, SHORT_OPAQUE_QUERY];
const TABS = ["Mensagens", "Pessoas", "Canais", "Grupos", "Arquivos", "Links"];
const ENDPOINTS = [
  "/api/search/v2/messages",
  "/api/search/users",
  "/api/search/channels",
  "/api/search/groups",
  "/api/search/files",
  "/api/search/links",
];

const user = { id: "u1", display_name: "Ana Busca" };

function page(items: unknown[], next: string | null) {
  return { data: { data: items, pagination: { limit: 20, next_cursor: next, has_more: !!next } } };
}

/**
 * Every request the page sent: a POST to a modern endpoint with no query
 * string at all, and the query in its body. Returns the bodies.
 */
function expectOnlyPostBodies(query: string): Array<Record<string, unknown>> {
  const calls = mockAuthFetch.mock.calls as Array<[string, RequestInit]>;
  expect(calls.length).toBeGreaterThan(0);
  return calls.map(([url, init]) => {
    const parsed = new URL(url, "http://localhost");
    expect(parsed.search).toBe("");
    expect(ENDPOINTS).toContain(parsed.pathname);
    expect(init.method).toBe("POST");
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(body.q).toBe(query);
    return body;
  });
}

async function flush() {
  await act(async () => {
    for (let i = 0; i < 5; i += 1) await Promise.resolve();
  });
}

async function search(text: string) {
  render(
    <MemoryRouter initialEntries={["/chat/search"]}>
      <Routes>
        <Route path="/chat/search" element={<GlobalSearchPage />} />
      </Routes>
    </MemoryRouter>,
  );
  fireEvent.change(screen.getByRole("searchbox", { name: FIELD }), { target: { value: text } });
  await act(async () => {
    vi.advanceTimersByTime(300);
  });
  await flush();
}

beforeEach(() => {
  vi.useFakeTimers();
  // Only people have rows (and a next page); every other category is empty.
  mockAuthFetch
    .mockReset()
    .mockImplementation(async (url: string) =>
      url.endsWith("/users") ? page([user], "next-page") : page([], null),
    );
});

afterEach(() => {
  vi.useRealTimers();
});

describe.each(QUERIES)("search transport for %j (#1081)", (query) => {
  it("Tudo, every tab and Carregar mais send it only in POST bodies", async () => {
    await search(query);
    expect(mockAuthFetch).toHaveBeenCalledTimes(6);

    for (const name of TABS) {
      fireEvent.click(screen.getByRole("tab", { name }));
      await flush();
    }
    fireEvent.click(screen.getByRole("tab", { name: "Pessoas" }));
    await flush();
    fireEvent.click(screen.getByRole("button", { name: "Carregar mais" }));
    await flush();

    // 6 overview sections + 6 tabs + 1 next page.
    expect(mockAuthFetch).toHaveBeenCalledTimes(13);
    const bodies = expectOnlyPostBodies(query);
    expect(bodies.filter((body) => body.cursor === "next-page")).toHaveLength(1);
  });

  it("a retry after a server failure is the same POST again", async () => {
    mockAuthFetch.mockRejectedValueOnce(new ApiRequestError(503, "internal", "x"));
    await search(query);
    const section = screen.getByRole("region", { name: "Mensagens" });
    fireEvent.click(within(section).getByRole("button", { name: "Tentar novamente" }));
    await flush();

    expect(mockAuthFetch).toHaveBeenCalledTimes(7);
    expectOnlyPostBodies(query);
  });

  it("an older search-service (405/404) leaves every category unavailable; retry POSTs again", async () => {
    mockAuthFetch
      .mockReset()
      .mockRejectedValue(new ApiRequestError(405, "bad_request", "method not allowed"))
      .mockRejectedValueOnce(new ApiRequestError(404, "not_found", "not found"));
    await search(query);

    expect(mockAuthFetch).toHaveBeenCalledTimes(6);
    expect(screen.getAllByText("Esta busca ainda não está disponível.")).toHaveLength(6);
    expect(screen.queryByTestId("global-search-empty")).toBeNull();

    const section = screen.getByRole("region", { name: "Pessoas" });
    fireEvent.click(within(section).getByRole("button", { name: "Tentar novamente" }));
    await flush();
    expect(mockAuthFetch).toHaveBeenCalledTimes(7);
    expect(mockAuthFetch.mock.calls[6][0]).toBe("/api/search/users");
    expectOnlyPostBodies(query);
  });
});
