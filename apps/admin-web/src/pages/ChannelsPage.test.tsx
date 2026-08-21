import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import { errorResponse, jsonResponse, renderWithSession, requestedURLs } from "../test/harness";
import ChannelsPage from "./ChannelsPage";

const READ = ["admin.channels.read"];
const MANAGE = ["admin.channels.read", "admin.channels.manage"];

function channel(overrides: Record<string, unknown> = {}) {
  return {
    id: "c-eng",
    workspace_id: "w1",
    workspace_name: "NChat",
    slug: "engenharia",
    display_name: "Engenharia",
    type: "private",
    status: "active",
    is_general: false,
    member_count: 12,
    moderator_count: 1,
    created_by_name: "Root",
    created_by_email: "root@example.test",
    created_at: "2026-01-01T00:00:00Z",
    last_activity_at: "2026-08-01T10:00:00Z",
    ...overrides,
  };
}

function conversation(overrides: Record<string, unknown> = {}) {
  return {
    id: "d-1",
    workspace_id: "w1",
    workspace_name: "NChat",
    type: "group",
    status: "active",
    participant_count: 4,
    message_count: 120,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-08-01T10:00:00Z",
    last_activity_at: "2026-08-01T10:00:00Z",
    ...overrides,
  };
}

/** Routes by URL so the two listings on this page can answer independently. */
function stubPage(channels: unknown[], conversations: unknown[], nextCursor: string | null = null) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (init?.method === "PATCH") {
      return jsonResponse({ data: channel({ status: "archived" }) });
    }
    if (url.includes("/conversations")) {
      return jsonResponse({
        data: { conversations, pagination: { next_cursor: null, has_more: false } },
      });
    }
    return jsonResponse({
      data: {
        channels,
        pagination: { next_cursor: nextCursor, has_more: nextCursor !== null },
      },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  _resetCSRFToken();
});

describe("ChannelsPage", () => {
  it("lists channels with their size, owner and last activity", async () => {
    stubPage([channel()], []);
    renderWithSession(<ChannelsPage />, READ);

    expect(await screen.findByRole("rowheader", { name: /Engenharia/ })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Membros" })).toBeInTheDocument();
    expect(screen.getByText("Root")).toBeInTheDocument();
  });

  // Listing a private channel is what admin.channels.read authorizes; it is not
  // a way to read one, and the row carries no message and no member name.
  it("lists a private channel without exposing anything inside it", async () => {
    stubPage([channel()], []);
    renderWithSession(<ChannelsPage />, READ);

    await screen.findByRole("rowheader", { name: /Engenharia/ });
    expect(screen.getAllByText("Privado").length).toBeGreaterThan(0);
  });

  it("sends the filters to the server", async () => {
    const fetchMock = stubPage([channel()], []);
    renderWithSession(<ChannelsPage />, READ);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.selectOptions(screen.getByLabelText("Visibilidade"), "private");
    await userEvent.selectOptions(screen.getByLabelText("Situação"), "archived");
    await userEvent.selectOptions(screen.getByLabelText("Tamanho"), "10");
    await userEvent.selectOptions(screen.getByLabelText("Atividade recente"), "30d");

    await waitFor(() => {
      const last =
        requestedURLs(fetchMock)
          .filter((url) => url.includes("/channels?"))
          .at(-1) ?? "";
      expect(last).toContain("type=private");
      expect(last).toContain("status=archived");
      expect(last).toContain("min_members=10");
      expect(last).toContain("active_within=30d");
    });
  });

  it("debounces the channel search", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = stubPage([channel()], []);
    renderWithSession(<ChannelsPage />, READ);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.type(screen.getByLabelText("Buscar por nome ou identificador"), "eng", {
      delay: null,
    });
    await vi.advanceTimersByTimeAsync(400);
    await waitFor(() =>
      expect(requestedURLs(fetchMock).some((url) => url.includes("q=eng"))).toBe(true),
    );
    vi.useRealTimers();
  });

  it("offers archiving only with the capability", async () => {
    stubPage([channel()], []);
    const { unmount } = renderWithSession(<ChannelsPage />, READ);
    await screen.findByRole("rowheader", { name: /Engenharia/ });
    expect(screen.queryByRole("button", { name: "Arquivar" })).not.toBeInTheDocument();
    unmount();

    stubPage([channel()], []);
    renderWithSession(<ChannelsPage />, MANAGE);
    await screen.findByRole("rowheader", { name: /Engenharia/ });
    expect(screen.getByRole("button", { name: "Arquivar" })).toBeInTheDocument();
  });

  // #geral is immutable in chat-service; the console does not offer a button
  // the API would refuse.
  it("does not offer to archive the workspace's general channel", async () => {
    stubPage([channel({ is_general: true, slug: "geral", display_name: "Geral" })], []);
    renderWithSession(<ChannelsPage />, MANAGE);

    await screen.findByRole("rowheader", { name: /Geral/ });
    expect(screen.queryByRole("button", { name: "Arquivar" })).not.toBeInTheDocument();
    expect(screen.getByText("canal geral")).toBeInTheDocument();
  });

  it("confirms archiving, states that history is preserved, and reports the result", async () => {
    stubPage([channel()], []);
    renderWithSession(<ChannelsPage />, MANAGE);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.click(screen.getByRole("button", { name: "Arquivar" }));
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByText(/histórico e as pessoas do canal são preservados/),
    ).toBeInTheDocument();

    await userEvent.click(within(dialog).getByRole("button", { name: "Arquivar" }));
    expect(await screen.findByTestId("admin-feedback")).toHaveTextContent("O histórico permanece");
  });

  it("reports a refused archive", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PATCH") return errorResponse(409, "conflict");
      if (String(input).includes("/conversations")) {
        return jsonResponse({
          data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
        });
      }
      return jsonResponse({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<ChannelsPage />, MANAGE);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.click(screen.getByRole("button", { name: "Arquivar" }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Arquivar" }),
    );

    expect(await screen.findByTestId("admin-feedback")).toBeInTheDocument();
  });

  it("opens the channel record on demand", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/conversations")) {
        return jsonResponse({
          data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
        });
      }
      if (/\/channels\/c-eng/.test(url)) {
        return jsonResponse({
          data: {
            ...channel(),
            category_name: "Times",
            moderators: [
              { user_id: "u1", display_name: "Ana", email: "ana@example.test", role: "moderator" },
            ],
            workspace_admins: [
              { user_id: "u2", display_name: "Root", email: "root@example.test", role: "owner" },
            ],
            members: [
              { user_id: "u3", display_name: "Zoe", email: "zoe@example.test", role: "member" },
            ],
            message_count: 4200,
          },
        });
      }
      return jsonResponse({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<ChannelsPage />, READ);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.click(screen.getByRole("button", { name: "Detalhes" }));
    const dialog = await screen.findByRole("dialog");
    // The two authorities are shown as two lists, because they are two things.
    expect(
      within(dialog).getByRole("heading", { name: "Moderadores do canal" }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("heading", { name: "Administradores do workspace" }),
    ).toBeInTheDocument();
    expect(within(dialog).getByText("4200")).toBeInTheDocument();
  });

  it("pages the channel listing", async () => {
    const fetchMock = stubPage([channel()], [], "cursor-2");
    renderWithSession(<ChannelsPage />, READ);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.click(screen.getAllByRole("button", { name: "Próxima página" })[0]);
    await waitFor(() =>
      expect(requestedURLs(fetchMock).some((url) => url.includes("cursor=cursor-2"))).toBe(true),
    );
  });

  it("says plainly when no channel matches", async () => {
    stubPage([], []);
    renderWithSession(<ChannelsPage />, READ);

    expect(
      await screen.findByText("Nenhum canal corresponde aos filtros aplicados."),
    ).toBeInTheDocument();
  });

  it("separates a refusal from a failure", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(403)));
    renderWithSession(<ChannelsPage />, READ);

    const alerts = await screen.findAllByRole("alert");
    expect(alerts[0]).toHaveTextContent("não tem permissão");
  });
});

describe("ChannelsPage conversations", () => {
  // The Slice C contract, asserted on the rendered screen: metadata only.
  it("shows conversation metadata and nothing from inside a conversation", async () => {
    stubPage([channel()], [conversation()]);
    renderWithSession(<ChannelsPage />, READ);

    const section = await screen.findByRole("region", { name: "Conversas privadas" });
    expect(within(section).getByText("Grupo")).toBeInTheDocument();
    expect(within(section).getByText("4")).toBeInTheDocument();
    expect(within(section).getByText("120")).toBeInTheDocument();
    expect(
      within(section).getByRole("columnheader", { name: "Participantes" }),
    ).toBeInTheDocument();
    // No column exists that could ever carry content.
    expect(
      within(section).queryByRole("columnheader", { name: /Mensagem|Título|Prévia/ }),
    ).toBeNull();
  });

  it("filters conversations by kind, server-side", async () => {
    const fetchMock = stubPage([channel()], [conversation()]);
    renderWithSession(<ChannelsPage />, READ);
    await screen.findByRole("region", { name: "Conversas privadas" });

    await userEvent.selectOptions(screen.getByLabelText("Tipo de conversa"), "direct");
    await waitFor(() =>
      expect(
        requestedURLs(fetchMock).some(
          (url) => url.includes("/conversations") && url.includes("type=direct"),
        ),
      ).toBe(true),
    );
  });

  it("says plainly when there is no private conversation", async () => {
    stubPage([channel()], []);
    renderWithSession(<ChannelsPage />, READ);

    expect(await screen.findByText("Nenhuma conversa privada registrada.")).toBeInTheDocument();
  });
});

describe("ChannelsPage rendering details", () => {
  it("offers unarchiving for an archived channel with its own wording", async () => {
    stubPage([channel({ status: "archived" })], []);
    renderWithSession(<ChannelsPage />, MANAGE);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    expect(screen.getByText("Arquivado")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Desarquivar" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/volta a ficar ativo/)).toBeInTheDocument();
  });

  it("renders a public channel and one with no creator and no moderators", async () => {
    stubPage(
      [channel({ type: "public", moderator_count: 0, created_by_name: "", created_by_email: "" })],
      [],
    );
    renderWithSession(<ChannelsPage />, READ);

    await screen.findByRole("rowheader", { name: /Engenharia/ });
    expect(screen.getAllByText("Público").length).toBeGreaterThan(0);
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
  });

  it("renders a direct, archived conversation with no activity yet", async () => {
    stubPage(
      [channel()],
      [conversation({ type: "direct", status: "archived", last_activity_at: null })],
    );
    renderWithSession(<ChannelsPage />, READ);

    const section = await screen.findByRole("region", { name: "Conversas privadas" });
    expect(within(section).getByText("Direta")).toBeInTheDocument();
    expect(within(section).getByText("Arquivada")).toBeInTheDocument();
  });

  it("shows an empty channel detail without inventing people", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/conversations")) {
        return jsonResponse({
          data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
        });
      }
      if (/\/channels\/c-eng/.test(url)) {
        return jsonResponse({
          data: {
            ...channel(),
            category_name: "",
            moderators: [],
            workspace_admins: [],
            members: [],
            message_count: 0,
          },
        });
      }
      return jsonResponse({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<ChannelsPage />, READ);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.click(screen.getByRole("button", { name: "Detalhes" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Este canal não tem moderadores próprios."),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText("Nenhum owner ou admin ativo neste workspace."),
    ).toBeInTheDocument();

    await userEvent.click(within(dialog).getByRole("button", { name: "Fechar" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("closes the channel record on Escape", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/conversations")) {
        return jsonResponse({
          data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
        });
      }
      if (/\/channels\/c-eng/.test(url)) {
        return jsonResponse({
          data: {
            ...channel(),
            category_name: "Times",
            moderators: [],
            workspace_admins: [],
            members: [],
            message_count: 0,
          },
        });
      }
      return jsonResponse({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<ChannelsPage />, READ);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.click(screen.getByRole("button", { name: "Detalhes" }));
    await screen.findByRole("dialog");
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("pages the conversation listing independently of the channel one", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/conversations")) {
        return jsonResponse({
          data: {
            conversations: [conversation()],
            pagination: { next_cursor: "conv-2", has_more: true },
          },
        });
      }
      return jsonResponse({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<ChannelsPage />, READ);

    const section = await screen.findByRole("region", { name: "Conversas privadas" });
    await userEvent.click(within(section).getByRole("button", { name: "Próxima página" }));
    await waitFor(() =>
      expect(
        requestedURLs(fetchMock).some(
          (url) => url.includes("/conversations") && url.includes("cursor=conv-2"),
        ),
      ).toBe(true),
    );
    await userEvent.click(within(section).getByRole("button", { name: "Página anterior" }));
  });
});

describe("ChannelsPage administered-by filter", () => {
  function stubWithPeople(
    people: unknown[],
    onChannels?: (url: string) => void,
    peopleStatus = 200,
  ): ReturnType<typeof vi.fn> {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/users?")) {
        return peopleStatus === 200
          ? jsonResponse({
              data: { users: people, pagination: { next_cursor: null, has_more: false } },
            })
          : errorResponse(peopleStatus);
      }
      if (url.includes("/conversations")) {
        return jsonResponse({
          data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
        });
      }
      onChannels?.(url);
      return jsonResponse({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function person(overrides: Record<string, unknown> = {}) {
    return {
      id: "u-ana",
      email: "ana@example.test",
      display_name: "Ana",
      full_name: "Ana Lima",
      avatar_url: "",
      status: "active",
      auth_source: "manual",
      external_provider: "",
      identity_managed_externally: false,
      last_login_at: null,
      created_at: "2026-01-01T00:00:00Z",
      platform_admin: false,
      admin_roles: [],
      workspace_roles: [],
      active_sessions: 0,
      ...overrides,
    };
  }

  // The operator answers "show me Ana's channels" by typing "Ana", not by
  // knowing an identifier.
  it("searches people while typing and only filters after a selection", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const channelURLs: string[] = [];
    const fetchMock = stubWithPeople([person()], (url) => channelURLs.push(url));
    renderWithSession(<ChannelsPage />, [...READ, "admin.users.read"]);
    await screen.findByRole("rowheader", { name: /Engenharia/ });
    const before = channelURLs.length;

    await userEvent.type(screen.getByLabelText("Administrado por"), "Ana", { delay: null });
    await vi.advanceTimersByTimeAsync(400);

    // The people search ran server-side…
    await waitFor(() =>
      expect(
        requestedURLs(fetchMock).some((url) => url.includes("/users?") && url.includes("q=Ana")),
      ).toBe(true),
    );
    // …and the typed text never became a filter value.
    expect(channelURLs.length).toBe(before);
    expect(channelURLs.every((url) => !url.includes("administered_by"))).toBe(true);

    await userEvent.click(await screen.findByRole("option", { name: /Ana Lima/ }));
    await waitFor(() =>
      expect(channelURLs.some((url) => url.includes("administered_by=u-ana"))).toBe(true),
    );
    vi.useRealTimers();
  });

  // The old field sent "abc" and got a 400. Partial text must never reach the
  // channel listing at all now.
  it("never sends partial text as an identifier", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const channelURLs: string[] = [];
    stubWithPeople([], (url) => channelURLs.push(url));
    renderWithSession(<ChannelsPage />, [...READ, "admin.users.read"]);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    for (const partial of ["a", "ab", "abc", "11111111-1111"]) {
      await userEvent.clear(screen.getByLabelText("Administrado por"));
      await userEvent.type(screen.getByLabelText("Administrado por"), partial, { delay: null });
      await vi.advanceTimersByTimeAsync(400);
    }

    expect(channelURLs.every((url) => !url.includes("administered_by"))).toBe(true);
    // And the table never fell into an error state.
    expect(screen.getByRole("rowheader", { name: /Engenharia/ })).toBeInTheDocument();
    vi.useRealTimers();
  });

  it("clearing the selection removes the filter and restarts paging", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const channelURLs: string[] = [];
    stubWithPeople([person()], (url) => channelURLs.push(url));
    renderWithSession(<ChannelsPage />, [...READ, "admin.users.read"]);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.type(screen.getByLabelText("Administrado por"), "Ana", { delay: null });
    await vi.advanceTimersByTimeAsync(400);
    await userEvent.click(await screen.findByRole("option", { name: /Ana Lima/ }));
    await waitFor(() =>
      expect(channelURLs.some((url) => url.includes("administered_by=u-ana"))).toBe(true),
    );

    await userEvent.click(screen.getByRole("button", { name: "Trocar" }));
    await waitFor(() => {
      const last = channelURLs.at(-1) ?? "";
      expect(last).not.toContain("administered_by");
      expect(last).not.toContain("cursor=");
    });
    vi.useRealTimers();
  });

  // A slow "An" must not overwrite a fast "Ana".
  it("drops a stale people search", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/users?")) {
        const slow = url.includes("q=An&") || url.endsWith("q=An");
        const body = jsonResponse({
          data: {
            users: [slow ? person({ id: "u-stale", full_name: "Antigo Resultado" }) : person()],
            pagination: { next_cursor: null, has_more: false },
          },
        });
        return slow
          ? new Promise<Response>((resolve) => setTimeout(() => resolve(body), 200))
          : body;
      }
      if (url.includes("/conversations")) {
        return jsonResponse({
          data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
        });
      }
      return jsonResponse({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<ChannelsPage />, [...READ, "admin.users.read"]);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    const field = screen.getByLabelText("Administrado por");
    await userEvent.type(field, "An", { delay: null });
    await vi.advanceTimersByTimeAsync(400);
    await userEvent.type(field, "a", { delay: null });
    await vi.advanceTimersByTimeAsync(400);
    await vi.advanceTimersByTimeAsync(500);

    expect(await screen.findByRole("option", { name: /Ana Lima/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Antigo Resultado/ })).not.toBeInTheDocument();
    vi.useRealTimers();
  });

  it("says plainly when nobody matches", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubWithPeople([]);
    renderWithSession(<ChannelsPage />, [...READ, "admin.users.read"]);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.type(screen.getByLabelText("Administrado por"), "Zed", { delay: null });
    await vi.advanceTimersByTimeAsync(400);

    expect(await screen.findByText("Nenhuma pessoa encontrada.")).toBeInTheDocument();
    vi.useRealTimers();
  });

  // A failed search is a failure, not "nobody matches".
  it("shows a failed people search as an error", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubWithPeople([], undefined, 403);
    renderWithSession(<ChannelsPage />, [...READ, "admin.users.read"]);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    await userEvent.type(screen.getByLabelText("Administrado por"), "Ana", { delay: null });
    await vi.advanceTimersByTimeAsync(400);

    expect(await screen.findByText(/não tem permissão/)).toBeInTheDocument();
    expect(screen.queryByText("Nenhuma pessoa encontrada.")).not.toBeInTheDocument();
    vi.useRealTimers();
  });

  it("is not offered without the capability that searches people", async () => {
    stubWithPeople([person()]);
    renderWithSession(<ChannelsPage />, READ);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    expect(screen.queryByLabelText("Administrado por")).not.toBeInTheDocument();
    expect(screen.getByText(/exige a permissão/)).toBeInTheDocument();
  });

  it("sends nothing when nobody is selected", async () => {
    const fetchMock = stubWithPeople([person()]);
    renderWithSession(<ChannelsPage />, [...READ, "admin.users.read"]);
    await screen.findByRole("rowheader", { name: /Engenharia/ });

    expect(requestedURLs(fetchMock).some((url) => url.includes("administered_by"))).toBe(false);
  });
});

describe("ChannelDetailDialog membership", () => {
  function candidate(overrides: Record<string, unknown> = {}) {
    return {
      user_id: "u-ana",
      display_name: "Ana",
      full_name: "Ana Lima",
      email: "ana@example.test",
      avatar_url: "",
      workspace_role: "member",
      ...overrides,
    };
  }

  function detailResponse(overrides: Record<string, unknown> = {}) {
    return {
      data: {
        ...channel(),
        category_name: "Times",
        moderators: [],
        workspace_admins: [],
        members: [
          { user_id: "u-zoe", display_name: "Zoe", email: "zoe@example.test", role: "member" },
        ],
        message_count: 0,
        ...overrides,
      },
    };
  }

  function membership(overrides: Record<string, unknown> = {}) {
    return {
      channel_id: "c-eng",
      workspace_id: "w1",
      added: 1,
      already_members: 0,
      removed: true,
      member_count: 13,
      ...overrides,
    };
  }

  interface StubOptions {
    detail?: unknown;
    candidates?: unknown[];
    candidatesStatus?: number;
    mutation?: () => Response | Promise<Response>;
    onCandidateSearch?: (url: string) => void;
  }

  function stubDetail(options: StubOptions = {}): ReturnType<typeof vi.fn> {
    const detail = options.detail ?? detailResponse();
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === "POST" || init?.method === "DELETE") {
        return options.mutation ? options.mutation() : jsonResponse({ data: membership() });
      }
      if (url.includes("member-candidates")) {
        options.onCandidateSearch?.(url);
        if ((options.candidatesStatus ?? 200) !== 200) {
          return errorResponse(options.candidatesStatus ?? 403);
        }
        return jsonResponse({ data: { candidates: options.candidates ?? [candidate()] } });
      }
      if (url.includes("/conversations")) {
        return jsonResponse({
          data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
        });
      }
      if (/\/channels\/c-eng/.test(url)) return jsonResponse(detail);
      return jsonResponse({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  async function openDetail(capabilities: string[]) {
    renderWithSession(<ChannelsPage />, capabilities);
    await screen.findByRole("rowheader", { name: /Engenharia/ });
    await userEvent.click(screen.getByRole("button", { name: "Detalhes" }));
    return screen.findByRole("dialog");
  }

  it("lists the membership by name, without showing identifiers", async () => {
    stubDetail();
    const dialog = await openDetail(READ);

    expect(within(dialog).getByRole("rowheader", { name: "Zoe" })).toBeInTheDocument();
    // The identifier is internal. It reaches the API and stops there.
    expect(within(dialog).queryByText("u-zoe")).not.toBeInTheDocument();
  });

  it("offers no membership control without the managing capability", async () => {
    stubDetail();
    const dialog = await openDetail(READ);

    expect(within(dialog).queryByLabelText("Adicionar membro")).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Remover" })).not.toBeInTheDocument();
  });

  // The whole flow the review asked for: human search → selection → the request
  // carries the identifier the operator never saw.
  it("searches by name, and adds the person that was selected", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const searches: string[] = [];
    const fetchMock = stubDetail({ onCandidateSearch: (url) => searches.push(url) });
    const dialog = await openDetail(MANAGE);

    await userEvent.type(within(dialog).getByLabelText("Adicionar membro"), "Ana", { delay: null });
    await vi.advanceTimersByTimeAsync(400);

    // Server-side search, scoped to this channel.
    await waitFor(() => expect(searches.some((url) => url.includes("q=Ana"))).toBe(true));
    expect(searches.every((url) => url.includes("/channels/c-eng/member-candidates"))).toBe(true);

    await userEvent.click(await screen.findByRole("option", { name: /Ana Lima/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    expect(await screen.findByTestId("admin-membership-feedback")).toHaveTextContent(
      "13 membro(s)",
    );
    const post = fetchMock.mock.calls.find((call) => call[1]?.method === "POST");
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ user_ids: ["u-ana"] });
    vi.useRealTimers();
  });

  // Typed text is a search, never a value.
  it("does not add anybody while text is only typed", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = stubDetail();
    const dialog = await openDetail(MANAGE);

    await userEvent.type(within(dialog).getByLabelText("Adicionar membro"), "Ana", { delay: null });
    await vi.advanceTimersByTimeAsync(400);
    await screen.findByRole("option", { name: /Ana Lima/ });

    expect(within(dialog).getByRole("button", { name: "Adicionar" })).toBeDisabled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Adicionar" }));
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "POST")).toHaveLength(0);
    vi.useRealTimers();
  });

  it("is selectable with the keyboard", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = stubDetail({
      candidates: [candidate(), candidate({ user_id: "u-bruno", full_name: "Bruno Dias" })],
    });
    const dialog = await openDetail(MANAGE);

    const field = within(dialog).getByLabelText("Adicionar membro");
    await userEvent.type(field, "a", { delay: null });
    await userEvent.type(field, "n", { delay: null });
    await vi.advanceTimersByTimeAsync(400);
    await screen.findByRole("option", { name: /Ana Lima/ });

    await userEvent.keyboard("{ArrowDown}{Enter}");
    await userEvent.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    await waitFor(() =>
      expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "POST")).toHaveLength(1),
    );
    const post = fetchMock.mock.calls.find((call) => call[1]?.method === "POST");
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ user_ids: ["u-bruno"] });
    vi.useRealTimers();
  });

  // A slow earlier search must not replace a faster later one.
  it("drops a stale candidate search", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("member-candidates")) {
        const slow = url.includes("q=An&") || url.endsWith("q=An");
        const body = jsonResponse({
          data: {
            candidates: [
              slow ? candidate({ user_id: "u-stale", full_name: "Antigo Resultado" }) : candidate(),
            ],
          },
        });
        return slow
          ? new Promise<Response>((resolve) => setTimeout(() => resolve(body), 200))
          : body;
      }
      if (url.includes("/conversations")) {
        return jsonResponse({
          data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
        });
      }
      if (/\/channels\/c-eng/.test(url)) return jsonResponse(detailResponse());
      return jsonResponse({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    const dialog = await openDetail(MANAGE);

    const field = within(dialog).getByLabelText("Adicionar membro");
    await userEvent.type(field, "An", { delay: null });
    await vi.advanceTimersByTimeAsync(400);
    await userEvent.type(field, "a", { delay: null });
    await vi.advanceTimersByTimeAsync(400);
    await vi.advanceTimersByTimeAsync(500);

    expect(await screen.findByRole("option", { name: /Ana Lima/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Antigo Resultado/ })).not.toBeInTheDocument();
    vi.useRealTimers();
  });

  // Somebody already in the channel is not offered at all — the server excludes
  // them in the same query — so a useless mutation cannot be started.
  it("says plainly when the search finds nobody addable", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubDetail({ candidates: [] });
    const dialog = await openDetail(MANAGE);

    await userEvent.type(within(dialog).getByLabelText("Adicionar membro"), "Zoe", { delay: null });
    await vi.advanceTimersByTimeAsync(400);

    expect(await screen.findByText(/quem já é membro não aparece aqui/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Adicionar" })).toBeDisabled();
    vi.useRealTimers();
  });

  it("shows a failed candidate search as an error, not as an empty result", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubDetail({ candidatesStatus: 403 });
    const dialog = await openDetail(MANAGE);

    await userEvent.type(within(dialog).getByLabelText("Adicionar membro"), "Ana", { delay: null });
    await vi.advanceTimersByTimeAsync(400);

    expect(await screen.findByText(/não tem permissão/)).toBeInTheDocument();
    expect(screen.queryByText(/quem já é membro não aparece aqui/)).not.toBeInTheDocument();
    vi.useRealTimers();
  });

  it("reports a refused membership change instead of claiming success", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    stubDetail({ mutation: () => errorResponse(409, "conflict") });
    const dialog = await openDetail(MANAGE);

    await userEvent.type(within(dialog).getByLabelText("Adicionar membro"), "Ana", { delay: null });
    await vi.advanceTimersByTimeAsync(400);
    await userEvent.click(await screen.findByRole("option", { name: /Ana Lima/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    expect(await screen.findByTestId("admin-membership-feedback")).toHaveClass("admin-alert");
    vi.useRealTimers();
  });

  it("does not submit the same add twice", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let settle: (value: Response) => void = () => {};
    const fetchMock = stubDetail({
      mutation: () => new Promise<Response>((resolve) => (settle = resolve)),
    });
    const dialog = await openDetail(MANAGE);

    await userEvent.type(within(dialog).getByLabelText("Adicionar membro"), "Ana", { delay: null });
    await vi.advanceTimersByTimeAsync(400);
    await userEvent.click(await screen.findByRole("option", { name: /Ana Lima/ }));

    const add = within(dialog).getByRole("button", { name: "Adicionar" });
    await userEvent.click(add);
    await userEvent.click(screen.getByRole("button", { name: "Aplicando…" })).catch(() => {});

    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "POST")).toHaveLength(1);
    settle(jsonResponse({ data: membership() }));
    vi.useRealTimers();
  });

  it("confirms a removal, and cancelling calls nothing", async () => {
    const fetchMock = stubDetail();
    const dialog = await openDetail(MANAGE);

    await userEvent.click(within(dialog).getByRole("button", { name: "Remover" }));
    const confirmation = screen.getByRole("dialog", { name: "Remover esta pessoa do canal?" });
    expect(
      within(confirmation).getByText(/histórico do canal não são alterados/),
    ).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "DELETE")).toHaveLength(0);

    await userEvent.click(within(confirmation).getByRole("button", { name: "Cancelar" }));
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "DELETE")).toHaveLength(0);
  });

  it("removes a member once confirmed", async () => {
    const fetchMock = stubDetail();
    const dialog = await openDetail(MANAGE);

    await userEvent.click(within(dialog).getByRole("button", { name: "Remover" }));
    await userEvent.click(
      within(screen.getByRole("dialog", { name: "Remover esta pessoa do canal?" })).getByRole(
        "button",
        { name: "Remover" },
      ),
    );

    await waitFor(() =>
      expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "DELETE")).toHaveLength(1),
    );
    expect(await screen.findByTestId("admin-membership-feedback")).toHaveTextContent("removido");
  });

  it("offers both operations on an active channel", async () => {
    stubDetail({ detail: detailResponse({ status: "active" }) });
    const dialog = await openDetail(MANAGE);

    expect(within(dialog).getByLabelText("Adicionar membro")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Remover" })).toBeInTheDocument();
    expect(within(dialog).queryByText(/canal está arquivado/)).not.toBeInTheDocument();
  });

  // Adding and removing are two rules, not one. The shared eligibility rule
  // requires an active channel, so an archived one admits nobody — but the
  // backend's removal does not read the status at all, and the UI used to hide
  // an operation that works.
  it("blocks only the addition on an archived channel", async () => {
    stubDetail({ detail: detailResponse({ status: "archived" }) });
    const dialog = await openDetail(MANAGE);

    expect(within(dialog).queryByLabelText("Adicionar membro")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Remover" })).toBeInTheDocument();
    // The notice says which half is unavailable, not that membership is frozen.
    expect(within(dialog).getByText(/Novos membros não podem ser adicionados/)).toBeInTheDocument();
    expect(within(dialog).getByText(/ainda podem ser removidos/)).toBeInTheDocument();
    expect(within(dialog).getByRole("rowheader", { name: "Zoe" })).toBeInTheDocument();
  });

  it("removes a member from an archived channel, through the confirmation", async () => {
    const fetchMock = stubDetail({ detail: detailResponse({ status: "archived" }) });
    const dialog = await openDetail(MANAGE);

    await userEvent.click(within(dialog).getByRole("button", { name: "Remover" }));
    const confirmation = screen.getByRole("dialog", { name: "Remover esta pessoa do canal?" });
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "DELETE")).toHaveLength(0);

    await userEvent.click(within(confirmation).getByRole("button", { name: "Cancelar" }));
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "DELETE")).toHaveLength(0);

    await userEvent.click(within(dialog).getByRole("button", { name: "Remover" }));
    await userEvent.click(
      within(screen.getByRole("dialog", { name: "Remover esta pessoa do canal?" })).getByRole(
        "button",
        { name: "Remover" },
      ),
    );

    await waitFor(() =>
      expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "DELETE")).toHaveLength(1),
    );
    expect(await screen.findByTestId("admin-membership-feedback")).toHaveTextContent("removido");
  });

  // #geral is the mirror image: the backend accepts additions (a guest is not
  // enrolled automatically) and refuses removals with 403.
  it("offers only the addition on the general channel", async () => {
    stubDetail({
      detail: detailResponse({ is_general: true, slug: "geral", display_name: "Geral" }),
    });
    renderWithSession(<ChannelsPage />, MANAGE);
    await screen.findByRole("rowheader", { name: /Engenharia/ });
    await userEvent.click(screen.getByRole("button", { name: "Detalhes" }));
    const dialog = await screen.findByRole("dialog");

    expect(within(dialog).getByLabelText("Adicionar membro")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Remover" })).not.toBeInTheDocument();
    expect(within(dialog).getByText(/mas não remover/)).toBeInTheDocument();
  });

  // The response is the source of truth for the total; the console never
  // computes count ± 1 of its own.
  it("reports the member count the server confirmed", async () => {
    stubDetail({ mutation: () => jsonResponse({ data: membership({ member_count: 42 }) }) });
    const dialog = await openDetail(MANAGE);

    await userEvent.click(within(dialog).getByRole("button", { name: "Remover" }));
    await userEvent.click(
      within(screen.getByRole("dialog", { name: "Remover esta pessoa do canal?" })).getByRole(
        "button",
        { name: "Remover" },
      ),
    );

    expect(await screen.findByTestId("admin-membership-feedback")).toHaveTextContent(
      "42 membro(s)",
    );
  });
});
