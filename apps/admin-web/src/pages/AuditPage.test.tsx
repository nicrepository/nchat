import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AdminSessionContext } from "../session/AdminSessionContext";
import { errorResponse, jsonResponse, requestedURLs, sessionValue } from "../test/harness";
import AuditPage from "./AuditPage";

const ANA = "11111111-1111-1111-1111-111111111111";
const OTTO = "22222222-2222-2222-2222-222222222222";

function auditEvent(overrides: Record<string, unknown> = {}) {
  return {
    id: "7",
    occurred_at: "2026-08-20T10:00:00Z",
    actor_user_id: "u1",
    actor_email: "admin@example.test",
    action: "admin.session.create",
    resource: "admin.session",
    result: "success",
    correlation_id: "req-7",
    ...overrides,
  };
}

function userRecord(fullName: string) {
  return {
    data: {
      id: ANA,
      email: "ana@example.test",
      display_name: "Ana",
      full_name: fullName,
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
      memberships: [],
      channel_count: 0,
      role_grants: [],
      available_roles: [],
    },
  };
}

/**
 * Routes by URL so a spec can assert what the audit request carried, and answer
 * the directory lookup separately — the heading's name and the trail are
 * independent on purpose.
 */
function stubAudit(
  events: unknown[],
  options: { auditStatus?: number; userStatus?: number; fullName?: string } = {},
) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/users/")) {
      return (options.userStatus ?? 200) === 200
        ? jsonResponse(userRecord(options.fullName ?? "Ana Lima"))
        : errorResponse(options.userStatus ?? 403);
    }
    return (options.auditStatus ?? 200) === 200
      ? jsonResponse({ data: { events } })
      : errorResponse(options.auditStatus ?? 403);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPage(path = "/audit", capabilities = ["admin.audit.read", "admin.users.read"]) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AdminSessionContext.Provider value={sessionValue(capabilities)}>
        <AuditPage />
      </AdminSessionContext.Provider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("AuditPage", () => {
  it("shows a loading state first", () => {
    vi.stubGlobal("fetch", vi.fn().mockReturnValue(new Promise(() => {})));
    renderPage();

    expect(screen.getByRole("status")).toHaveTextContent("Carregando…");
  });

  it("renders the trail with column headers", async () => {
    stubAudit([auditEvent()]);
    renderPage();

    expect(await screen.findByRole("table")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Ação" })).toBeInTheDocument();
    expect(screen.getByText("admin.session.create")).toBeInTheDocument();
    // The result is spelled out, not signalled by colour alone.
    expect(screen.getByText("success")).toBeInTheDocument();
  });

  it("shows an empty state rather than an empty table", async () => {
    stubAudit([]);
    renderPage();

    expect(
      await screen.findByText("Nenhum evento administrativo registrado ainda."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });

  // Reaching the page without the capability is not a rendering problem: the
  // server refuses, and the page says so instead of showing an empty trail that
  // would read as "nothing ever happened".
  it("reports a capability refusal", async () => {
    stubAudit([], { auditStatus: 403 });
    renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent("não tem permissão");
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("reports a server failure", async () => {
    stubAudit([], { auditStatus: 500 });
    renderPage();

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("asks for the platform-wide trail when nobody is named", async () => {
    const fetchMock = stubAudit([auditEvent()]);
    renderPage();
    await screen.findByRole("table");

    const audit = requestedURLs(fetchMock).find((url) => url.includes("/audit/events"));
    expect(audit).not.toContain("user_id");
    expect(screen.queryByTestId("admin-audit-filter")).not.toBeInTheDocument();
  });
});

describe("AuditPage filtered by a person", () => {
  // The whole point of the filter: it goes to the server, so an event outside
  // the global window is still found.
  it("sends the identifier to the API rather than filtering locally", async () => {
    const fetchMock = stubAudit([
      auditEvent({ id: "9", action: "admin.user.status.update", resource: `admin.user:${ANA}` }),
    ]);
    renderPage(`/audit?user=${ANA}`);
    await screen.findByRole("table");

    const audit = requestedURLs(fetchMock).find((url) => url.includes("/audit/events"));
    expect(audit).toContain(`user_id=${ANA}`);
  });

  it("names the person in the heading, not the identifier", async () => {
    stubAudit([auditEvent({ resource: `admin.user:${ANA}` })]);
    renderPage(`/audit?user=${ANA}`);

    expect(await screen.findByRole("heading", { name: /Ana Lima/ })).toBeInTheDocument();
    expect(screen.getByTestId("admin-audit-filter")).toHaveTextContent("Ana Lima");
    // The identifier is a filter value, not something the operator reads.
    expect(screen.queryByRole("heading", { name: new RegExp(ANA) })).not.toBeInTheDocument();
  });

  it("shows only the events the server returned for that person", async () => {
    stubAudit([
      auditEvent({ id: "9", action: "admin.user.status.update", resource: `admin.user:${ANA}` }),
    ]);
    renderPage(`/audit?user=${ANA}`);

    const table = await screen.findByRole("table");
    expect(within(table).getByText("admin.user.status.update")).toBeInTheDocument();
    expect(within(table).queryByText(`admin.user:${OTTO}`)).not.toBeInTheDocument();
  });

  // The trail exists whether or not the name can be read. An auditor holding
  // only admin.audit.read cannot query the directory, and the history is still
  // the answer to their question.
  it("still shows the history when the name cannot be resolved", async () => {
    stubAudit([auditEvent({ resource: `admin.user:${ANA}` })], { userStatus: 403 });
    renderPage(`/audit?user=${ANA}`, ["admin.audit.read", "admin.users.read"]);

    expect(await screen.findByRole("table")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /usuário selecionado/ })).toBeInTheDocument();
  });

  it("does not query the directory without the capability to read it", async () => {
    const fetchMock = stubAudit([auditEvent({ resource: `admin.user:${ANA}` })]);
    renderPage(`/audit?user=${ANA}`, ["admin.audit.read"]);
    await screen.findByRole("table");

    expect(requestedURLs(fetchMock).some((url) => url.includes("/users/"))).toBe(false);
    expect(screen.getByRole("heading", { name: /usuário selecionado/ })).toBeInTheDocument();
  });

  it("says plainly when a person has no administrative history", async () => {
    stubAudit([]);
    renderPage(`/audit?user=${ANA}`);

    expect(
      await screen.findByText("Nenhum evento administrativo registrado para esta conta."),
    ).toBeInTheDocument();
  });

  it("keeps the capability refusal distinct while filtered", async () => {
    stubAudit([], { auditStatus: 403 });
    renderPage(`/audit?user=${ANA}`);

    expect(await screen.findByRole("alert")).toHaveTextContent("não tem permissão");
  });

  // A hand-edited URL must not become a request the API would refuse.
  it("drops a malformed identifier instead of sending it", async () => {
    const fetchMock = stubAudit([auditEvent()]);
    renderPage("/audit?user=not-a-uuid");
    await screen.findByRole("table");

    const audit = requestedURLs(fetchMock).find((url) => url.includes("/audit/events"));
    expect(audit).not.toContain("user_id");
    expect(screen.getByRole("heading", { name: "Auditoria" })).toBeInTheDocument();
  });

  it("returns to the global trail from the filter notice", async () => {
    const fetchMock = stubAudit([auditEvent({ resource: `admin.user:${ANA}` })]);
    renderPage(`/audit?user=${ANA}`);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("link", { name: "Ver toda a auditoria" }));

    await waitFor(() => {
      const last = requestedURLs(fetchMock)
        .filter((url) => url.includes("/audit/events"))
        .at(-1);
      expect(last).not.toContain("user_id");
    });
    expect(screen.queryByTestId("admin-audit-filter")).not.toBeInTheDocument();
  });
});
