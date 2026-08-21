import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import AuditPage from "./AuditPage";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function renderPage() {
  return render(
    <MemoryRouter>
      <AuditPage />
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("AuditPage", () => {
  it("shows a loading state first", () => {
    vi.stubGlobal("fetch", vi.fn().mockReturnValue(new Promise(() => {})));
    renderPage();

    expect(screen.getByRole("status")).toHaveTextContent("Carregando eventos…");
  });

  it("renders the trail with column headers", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          data: {
            events: [
              {
                id: "7",
                occurred_at: "2026-08-20T10:00:00Z",
                actor_user_id: "u1",
                actor_email: "admin@example.test",
                action: "admin.session.create",
                resource: "admin.session",
                result: "success",
                correlation_id: "req-7",
              },
            ],
          },
        }),
      ),
    );
    renderPage();

    expect(await screen.findByRole("table")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Ação" })).toBeInTheDocument();
    expect(screen.getByText("admin.session.create")).toBeInTheDocument();
    // The result is spelled out, not signalled by colour alone.
    expect(screen.getByText("success")).toBeInTheDocument();
  });

  it("shows an empty state rather than an empty table", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ data: { events: [] } })));
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
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse({ error: { code: "forbidden", message: "x" } }, 403)),
    );
    renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Você não tem permissão para consultar a auditoria.",
    );
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("reports a server failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(jsonResponse({ error: { code: "internal_error", message: "x" } }, 500)),
    );
    renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível carregar os eventos.",
    );
  });
});
