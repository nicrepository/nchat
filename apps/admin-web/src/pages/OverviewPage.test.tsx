import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import { deferred, errorResponse, jsonResponse, renderWithSession } from "../test/harness";
import { metricPayload, overviewPayload, COLLECTED_AT } from "../test/healthFixtures";
import { AUTO_REFRESH_MS } from "../lib/useAutoRefresh";
import OverviewPage from "./OverviewPage";

const OBSERVER = ["admin.infrastructure.read"];

afterEach(() => {
  vi.restoreAllMocks();
  _resetCSRFToken();
});

function renderOverview(payload: unknown = overviewPayload(), capabilities = OBSERVER) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    void input;
    return Promise.resolve(jsonResponse(payload));
  });
  vi.stubGlobal("fetch", fetchMock);
  renderWithSession(<OverviewPage />, capabilities);
  return fetchMock;
}

describe("OverviewPage", () => {
  it("loads the whole dashboard with one request", async () => {
    const fetchMock = renderOverview();
    await screen.findByRole("region", { name: "Estado da plataforma" });

    // One endpoint, not one per card.
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0][0])).toContain("/overview");
  });

  it("answers the platform's state before anything else", async () => {
    renderOverview();
    const state = await screen.findByRole("region", { name: "Estado da plataforma" });

    expect(within(state).getByRole("status").textContent).toContain("Degradado");
    expect(
      within(state).getByText("A dependência respondeu, mas algo na resposta exige atenção."),
    ).toBeInTheDocument();
    expect(
      within(state).getByText(`Coleta de ${new Date(COLLECTED_AT).toLocaleString("pt-BR")}.`, {
        exact: false,
      }),
    ).toBeInTheDocument();
  });

  it("counts the services in every state, including the empty ones", async () => {
    renderOverview(overviewPayload({ state_counts: { healthy: 4 } }));
    const state = await screen.findByRole("region", { name: "Estado da plataforma" });

    const counts = within(state).getAllByRole("listitem");
    expect(counts).toHaveLength(5);
    expect(within(counts[0]).getByText("Indisponível")).toBeInTheDocument();
    expect(within(counts[0]).getByText("0")).toBeInTheDocument();
  });

  it("shows a loading state before the summary arrives", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    );
    renderWithSession(<OverviewPage />, OBSERVER);
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("renders every metric with its window and its definition", async () => {
    renderOverview();
    const card = await screen.findByTestId("metric-users.active_now");

    expect(within(card).getByText("3")).toBeInTheDocument();
    expect(within(card).getByText("agora")).toBeInTheDocument();
    // The definition is what makes the number verifiable, so it is on the card
    // rather than behind a tooltip.
    expect(
      within(card).getByText("Contas distintas com ao menos uma sessão de chat viva."),
    ).toBeInTheDocument();
  });

  it("renders a byte metric in binary units", async () => {
    renderOverview();
    const card = await screen.findByTestId("metric-storage.stored_bytes");
    expect(within(card).getByText("2.0 GiB")).toBeInTheDocument();
  });

  it("marks unavailable counters as such instead of showing zeros", async () => {
    renderOverview(
      overviewPayload({
        metrics_available: false,
        metrics: [metricPayload({ available: false, value: undefined })],
      }),
    );
    const card = await screen.findByTestId("metric-users.active_now");

    expect(within(card).getByText("Indisponível")).toBeInTheDocument();
    expect(within(card).queryByText("0")).not.toBeInTheDocument();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /não puderam ser calculados nesta coleta/,
    );
  });

  it("keeps the health section when the counters are unavailable", async () => {
    renderOverview(
      overviewPayload({
        metrics_available: false,
        metrics: [metricPayload({ available: false, value: undefined })],
      }),
    );
    // The partial failure the dashboard is designed around: the page still
    // shows what would tell an operator why the counters failed.
    expect(await screen.findByRole("region", { name: "Estado da plataforma" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Requer atenção" })).toBeInTheDocument();
  });

  it("shows each alert with its impact, its action and where to go next", async () => {
    renderOverview();
    const alerts = await screen.findByRole("region", { name: "Requer atenção" });

    expect(within(alerts).getByText(/LiveKit indisponível/)).toBeInTheDocument();
    expect(within(alerts).getByText("Servidor de mídia das chamadas.")).toBeInTheDocument();
    expect(within(alerts).getByText("Verifique se a dependência está de pé.")).toBeInTheDocument();
    expect(within(alerts).getByRole("link", { name: "Ver diagnóstico" })).toHaveAttribute(
      "href",
      "/health?service=livekit",
    );
  });

  it("states plainly when there is nothing to act on", async () => {
    renderOverview(overviewPayload({ alerts: [] }));
    const alerts = await screen.findByRole("region", { name: "Requer atenção" });
    // "Nothing to act on" and "this section failed" must not look the same.
    expect(within(alerts).getByText("Nenhuma condição acionável no momento.")).toBeInTheDocument();
  });

  it("links to the Health Center", async () => {
    renderOverview();
    expect(await screen.findByRole("link", { name: "Abrir o Health Center" })).toHaveAttribute(
      "href",
      "/health",
    );
  });

  it("reports a failed summary without blanking the session details", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => errorResponse(503, "unavailable")),
    );
    renderWithSession(<OverviewPage />, OBSERVER);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "O serviço administrativo está indisponível.",
    );
    expect(screen.getByRole("region", { name: "Esta sessão" })).toBeInTheDocument();
  });

  it("offers a retry when the summary fails", async () => {
    let attempts = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        attempts += 1;
        return attempts === 1 ? errorResponse(500, "internal") : jsonResponse(overviewPayload());
      }),
    );
    renderWithSession(<OverviewPage />, OBSERVER);

    await userEvent.click(await screen.findByRole("button", { name: "Tentar novamente" }));
    expect(await screen.findByRole("region", { name: "Estado da plataforma" })).toBeInTheDocument();
  });

  it("refreshes in the background without dropping the dashboard", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(() => {
        calls += 1;
        return Promise.resolve(
          calls === 1
            ? jsonResponse(overviewPayload())
            : jsonResponse(overviewPayload({ overall: "healthy", alerts: [] })),
        );
      }),
    );
    renderWithSession(<OverviewPage />, OBSERVER);
    const state = await screen.findByRole("region", { name: "Estado da plataforma" });
    expect(within(state).getByRole("status").textContent).toContain("Degradado");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS);
    });

    // Swapped in place: the skeleton never came back, and the operator is not
    // looking at a loading state every minute.
    await waitFor(() => {
      expect(
        within(screen.getByRole("region", { name: "Estado da plataforma" })).getByRole("status")
          .textContent,
      ).toContain("Saudável");
    });
    expect(screen.queryByText("Carregando…")).not.toBeInTheDocument();
    vi.useRealTimers();
  });

  it("does not let a slow earlier refresh overwrite a newer one", async () => {
    // Two background refreshes overlap and land in the opposite order to the
    // one they started in. Nothing about HTTP forbids that, so the page has to.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const older = deferred<Response>();
    const newer = deferred<Response>();
    let call = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(() => {
        call += 1;
        if (call === 1) return Promise.resolve(jsonResponse(overviewPayload()));
        return call === 2 ? older.promise : newer.promise;
      }),
    );
    renderWithSession(<OverviewPage />, OBSERVER);
    await screen.findByRole("region", { name: "Estado da plataforma" });

    // A starts, then B starts. Both stay in flight.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS);
    });
    expect(call).toBe(3);

    // B lands first, and is what the operator sees.
    await act(async () => {
      newer.resolve(
        jsonResponse(
          overviewPayload({
            overall: "healthy",
            alerts: [],
            metrics: [metricPayload({ value: 20 })],
          }),
        ),
      );
      await newer.promise;
    });
    await waitFor(() => {
      expect(
        within(screen.getByRole("region", { name: "Estado da plataforma" })).getByRole("status")
          .textContent,
      ).toContain("Saudável");
    });
    expect(screen.getByTestId("metric-users.active_now")).toHaveTextContent("20");

    // A lands last, carrying the state of the platform as it was before B.
    await act(async () => {
      older.resolve(
        jsonResponse(
          overviewPayload({
            overall: "degraded",
            metrics: [metricPayload({ value: 10 })],
          }),
        ),
      );
      await older.promise;
    });

    // The dashboard must not travel backwards in time.
    expect(
      within(screen.getByRole("region", { name: "Estado da plataforma" })).getByRole("status")
        .textContent,
    ).toContain("Saudável");
    expect(screen.getByTestId("metric-users.active_now")).toHaveTextContent("20");
    expect(screen.getByTestId("metric-users.active_now")).not.toHaveTextContent("10");
    vi.useRealTimers();
  });

  it("still applies a later refresh that lands in order", async () => {
    // The guard must discard stale answers, not legitimate ones.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let call = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(() => {
        call += 1;
        return Promise.resolve(
          jsonResponse(overviewPayload({ metrics: [metricPayload({ value: call * 10 })] })),
        );
      }),
    );
    renderWithSession(<OverviewPage />, OBSERVER);
    await screen.findByRole("region", { name: "Estado da plataforma" });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS);
    });
    await waitFor(() => {
      expect(screen.getByTestId("metric-users.active_now")).toHaveTextContent("20");
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS);
    });
    await waitFor(() => {
      expect(screen.getByTestId("metric-users.active_now")).toHaveTextContent("30");
    });
    vi.useRealTimers();
  });

  it("keeps the last good summary when a background refresh fails", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(() => {
        calls += 1;
        return Promise.resolve(
          calls === 1 ? jsonResponse(overviewPayload()) : errorResponse(503, "unavailable"),
        );
      }),
    );
    renderWithSession(<OverviewPage />, OBSERVER);
    await screen.findByRole("region", { name: "Estado da plataforma" });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS);
    });

    // Not applied, and not blanking: the "coleta de" timestamp is what says
    // how old what is on screen is.
    expect(screen.getByRole("region", { name: "Requer atenção" })).toBeInTheDocument();
    expect(screen.getByText("LiveKit indisponível", { exact: false })).toBeInTheDocument();
    vi.useRealTimers();
  });

  it("does not request the dashboard at all without the capability", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(overviewPayload()));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<OverviewPage />, ["admin.audit.read"]);

    expect(
      screen.getByText(/não tem a permissão/, { selector: ".admin-notice" }),
    ).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
    // The session details are everyone's, so they stay.
    expect(screen.getByRole("region", { name: "Esta sessão" })).toBeInTheDocument();
  });

  it("still reports what this session is", async () => {
    renderOverview();
    const session = await screen.findByRole("region", { name: "Esta sessão" });
    expect(within(session).getByText("admin.infrastructure.read")).toBeInTheDocument();
  });

  it("says so when the session holds no capability at all", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => errorResponse(403, "forbidden")),
    );
    renderWithSession(<OverviewPage />, []);
    expect(screen.getByText("Nenhuma permissão administrativa atribuída.")).toBeInTheDocument();
  });

  it("announces the platform state politely, and only the platform state", async () => {
    renderOverview();
    await screen.findByRole("region", { name: "Estado da plataforma" });

    // One live region, on the one thing whose change must not be missed. More
    // than one would make a periodic refresh read the page aloud.
    const live = document.querySelectorAll("[aria-live]");
    expect(live).toHaveLength(1);
    expect(live[0].textContent).toContain("Degradado");
  });
});
