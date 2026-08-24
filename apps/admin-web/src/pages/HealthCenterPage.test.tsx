import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import {
  deferred,
  errorResponse,
  jsonResponse,
  renderWithSession,
  routedFetch,
} from "../test/harness";
import {
  healthSnapshotPayload,
  CLAMAV_DEGRADED,
  COLLECTED_AT,
  LIVEKIT_UNAVAILABLE,
  POSTGRES_HEALTHY,
  SMTP_DISABLED,
  TURN_UNKNOWN,
} from "../test/healthFixtures";
import { AUTO_REFRESH_MS } from "../lib/useAutoRefresh";
import HealthCenterPage from "./HealthCenterPage";

const OBSERVER = ["admin.infrastructure.read"];

/** Two collection instants, so a stale snapshot is visible as an old one. */
const T1 = "2026-08-22T12:00:00.000Z";
const T2 = "2026-08-22T12:05:00.000Z";

afterEach(() => {
  vi.restoreAllMocks();
  _resetCSRFToken();
});

function renderHealthCenter(payload: unknown = healthSnapshotPayload()) {
  const fetchMock = routedFetch([
    { match: "/health/refresh", respond: () => jsonResponse(payload) },
    { match: "/health/services", respond: () => jsonResponse(payload) },
  ]);
  vi.stubGlobal("fetch", fetchMock);
  renderWithSession(<HealthCenterPage />, OBSERVER);
  return fetchMock;
}

async function rows() {
  return within(await screen.findByRole("table"))
    .getAllByRole("row")
    .slice(1);
}

describe("HealthCenterPage", () => {
  it("shows a loading state before the collection arrives", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    );
    renderWithSession(<HealthCenterPage />, OBSERVER);
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("lists every dependency with its state, latency and last check", async () => {
    renderHealthCenter();
    await screen.findByRole("table");

    expect(await rows()).toHaveLength(5);
    const livekit = screen.getByTestId("health-row-livekit");
    expect(within(livekit).getByText("Indisponível")).toBeInTheDocument();
    expect(within(livekit).getByText("3.0 s")).toBeInTheDocument();
    expect(
      within(livekit).getByText(new Date(COLLECTED_AT).toLocaleString("pt-BR")),
    ).toBeInTheDocument();
  });

  it("puts the most troubled dependency first", async () => {
    renderHealthCenter();
    const listed = await rows();
    expect(within(listed[0]).getByText("LiveKit")).toBeInTheDocument();
  });

  it("keeps the five states distinct on screen", async () => {
    renderHealthCenter();
    await screen.findByRole("table");

    // Every state appears as a word, so an operator reading a monochrome
    // screenshot can tell them apart. The legend accounts for a second
    // occurrence of each.
    for (const label of ["Indisponível", "Degradado", "Desconhecido", "Saudável", "Desabilitado"]) {
      expect(screen.getAllByText(label).length).toBeGreaterThanOrEqual(2);
    }
  });

  it("shows an em dash instead of a latency for a check that never ran", async () => {
    renderHealthCenter(healthSnapshotPayload([SMTP_DISABLED]));
    const smtp = await screen.findByTestId("health-row-smtp");
    expect(within(smtp).getByText("—")).toBeInTheDocument();
    expect(within(smtp).queryByText("0 ms")).not.toBeInTheDocument();
  });

  it("filters by state without asking the server again", async () => {
    const fetchMock = renderHealthCenter();
    await screen.findByRole("table");
    const before = fetchMock.mock.calls.length;

    await userEvent.selectOptions(screen.getByLabelText("Filtrar por estado"), "unavailable");

    expect(await rows()).toHaveLength(1);
    expect(screen.getByTestId("health-row-livekit")).toBeInTheDocument();
    expect(screen.queryByTestId("health-row-postgres")).not.toBeInTheDocument();
    // A dozen rows do not need a round trip to hide three of them.
    expect(fetchMock.mock.calls.length).toBe(before);
  });

  it("says so when a filter matches nothing", async () => {
    renderHealthCenter(healthSnapshotPayload([POSTGRES_HEALTHY]));
    await screen.findByRole("table");

    await userEvent.selectOptions(screen.getByLabelText("Filtrar por estado"), "unavailable");
    expect(screen.getByText("Nenhuma dependência neste estado.")).toBeInTheDocument();
  });

  it("reorders the table on request", async () => {
    renderHealthCenter();
    await screen.findByRole("table");

    await userEvent.selectOptions(screen.getByLabelText("Ordenar por"), "name");
    const listed = await rows();
    expect(within(listed[0]).getByText("ClamAV")).toBeInTheDocument();
  });

  it("opens a diagnosis with the sanitized category and the runbook", async () => {
    renderHealthCenter();
    const livekit = await screen.findByTestId("health-row-livekit");

    await userEvent.click(within(livekit).getByRole("button", { name: "Detalhes" }));

    const detail = document.getElementById("health-detail-livekit");
    expect(detail).not.toBeNull();
    expect(within(detail as HTMLElement).getByText("Tempo limite de conexão")).toBeInTheDocument();
    expect(
      within(detail as HTMLElement).getByText("docs/runbooks/task-livekit-coturn-dev.md"),
    ).toBeInTheDocument();
  });

  it("explains a disabled integration as a decision rather than a failure", async () => {
    renderHealthCenter(healthSnapshotPayload([SMTP_DISABLED]));
    const smtp = await screen.findByTestId("health-row-smtp");

    await userEvent.click(within(smtp).getByRole("button", { name: "Detalhes" }));
    const detail = document.getElementById("health-detail-smtp") as HTMLElement;
    expect(
      within(detail).getByText(/Não é uma falha, e nenhuma verificação foi executada/),
    ).toBeInTheDocument();
  });

  it("explains an unknown state as a blind spot and not as health", async () => {
    renderHealthCenter(healthSnapshotPayload([TURN_UNKNOWN]));
    const turn = await screen.findByTestId("health-row-turn");

    await userEvent.click(within(turn).getByRole("button", { name: "Detalhes" }));
    const detail = document.getElementById("health-detail-turn") as HTMLElement;
    expect(
      within(detail).getByText(/Este é o estado desconhecido — não é saudável/),
    ).toBeInTheDocument();
  });

  it("shows a sanitized version when the dependency reports one", async () => {
    renderHealthCenter(healthSnapshotPayload([CLAMAV_DEGRADED]));
    const clamav = await screen.findByTestId("health-row-clamav");

    await userEvent.click(within(clamav).getByRole("button", { name: "Detalhes" }));
    expect(screen.getByText("ClamAV 1.4.1")).toBeInTheDocument();
  });

  it("collapses an open diagnosis again", async () => {
    renderHealthCenter();
    const livekit = await screen.findByTestId("health-row-livekit");
    const toggle = within(livekit).getByRole("button", { name: "Detalhes" });

    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    await userEvent.click(within(livekit).getByRole("button", { name: "Ocultar" }));
    expect(within(livekit).getByRole("button", { name: "Detalhes" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
  });

  it("refreshes on demand and shows the new collection time", async () => {
    const refreshed = {
      data: {
        collected_at: "2026-08-22T12:05:00.000Z",
        overall: "healthy",
        services: [POSTGRES_HEALTHY],
      },
    };
    const fetchMock = routedFetch([
      { match: "/health/refresh", respond: () => jsonResponse(refreshed) },
      { match: "/health/services", respond: () => jsonResponse(healthSnapshotPayload()) },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<HealthCenterPage />, OBSERVER);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("button", { name: "Atualizar agora" }));

    await waitFor(() => {
      expect(
        screen.getByText(
          `Última coleta: ${new Date("2026-08-22T12:05:00.000Z").toLocaleString("pt-BR")}`,
        ),
      ).toBeInTheDocument();
    });
  });

  it("reports a failed refresh without losing the collection on screen", async () => {
    const fetchMock = routedFetch([
      { match: "/health/refresh", respond: () => errorResponse(503, "unavailable") },
      { match: "/health/services", respond: () => jsonResponse(healthSnapshotPayload()) },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<HealthCenterPage />, OBSERVER);
    await screen.findByRole("table");

    await userEvent.click(screen.getByRole("button", { name: "Atualizar agora" }));

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    // The table is still there: a failed refresh must not blank the page.
    expect(screen.getByTestId("health-row-livekit")).toBeInTheDocument();
  });

  it("one unreachable dependency does not break the page", async () => {
    renderHealthCenter(healthSnapshotPayload([LIVEKIT_UNAVAILABLE, POSTGRES_HEALTHY]));
    await screen.findByRole("table");

    expect(screen.getByTestId("health-row-livekit")).toBeInTheDocument();
    expect(screen.getByTestId("health-row-postgres")).toBeInTheDocument();
  });

  it("reports a missing capability as a permission problem, not as an outage", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => errorResponse(403, "forbidden")),
    );
    renderWithSession(<HealthCenterPage />, []);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Você não tem permissão para esta seção.",
    );
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("offers a retry when the listing fails", async () => {
    let attempts = 0;
    const fetchMock = vi.fn(async () => {
      attempts += 1;
      return attempts === 1
        ? errorResponse(500, "internal")
        : jsonResponse(healthSnapshotPayload());
    });
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<HealthCenterPage />, OBSERVER);

    await userEvent.click(await screen.findByRole("button", { name: "Tentar novamente" }));
    expect(await screen.findByRole("table")).toBeInTheDocument();
  });

  it("says so when the server declares no dependency at all", async () => {
    renderHealthCenter(healthSnapshotPayload([]));
    expect(await screen.findByText("Nenhuma dependência declarada.")).toBeInTheDocument();
  });

  it("refreshes in the background without dropping the table", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let listings = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes("/health/services")) {
        listings += 1;
        return jsonResponse(
          listings === 1
            ? healthSnapshotPayload()
            : {
                data: {
                  collected_at: "2026-08-22T12:09:00.000Z",
                  overall: "healthy",
                  services: [POSTGRES_HEALTHY],
                },
              },
        );
      }
      throw new Error(`unstubbed: ${String(input)}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<HealthCenterPage />, OBSERVER);
    await screen.findByRole("table");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS);
    });

    // The new collection replaced the old one in place. The skeleton never
    // came back: a periodic refresh must not take the table away from an
    // operator who is reading it.
    await waitFor(() => {
      expect(screen.queryByTestId("health-row-livekit")).not.toBeInTheDocument();
    });
    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(screen.getByTestId("health-row-postgres")).toBeInTheDocument();
    vi.useRealTimers();
  });

  it("does not let a slow manual refresh overwrite a newer background one", async () => {
    // The manual button and the interval write the same snapshot and never
    // pass through each other's code, so this is the interleaving the ordering
    // rule exists for: the manual request starts first, the periodic one
    // overtakes it, and the manual answer arrives last carrying an older
    // collection.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const manual = deferred<Response>();
    const periodic = deferred<Response>();
    let listings = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/health/refresh")) return manual.promise;
        listings += 1;
        return listings === 1
          ? Promise.resolve(
              jsonResponse(healthSnapshotPayload([{ ...POSTGRES_HEALTHY, state: "degraded" }])),
            )
          : periodic.promise;
      }),
    );
    renderWithSession(<HealthCenterPage />, OBSERVER);
    await screen.findByRole("table");

    // A: the manual refresh, started first and held open.
    await userEvent.click(screen.getByRole("button", { name: "Atualizar agora" }));
    // B: the periodic refresh, started after it.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS);
    });

    // B lands first: PostgreSQL healthy, collected at T2.
    await act(async () => {
      periodic.resolve(
        jsonResponse({
          data: {
            collected_at: T2,
            overall: "healthy",
            services: [{ ...POSTGRES_HEALTHY, checked_at: T2 }],
          },
        }),
      );
      await periodic.promise;
    });
    await waitFor(() => {
      expect(screen.getByTestId("health-row-postgres")).toHaveTextContent("Saudável");
    });

    // A lands last: PostgreSQL degraded, collected at T1 — the state of the
    // platform before B looked at it.
    await act(async () => {
      manual.resolve(
        jsonResponse({
          data: {
            collected_at: T1,
            overall: "degraded",
            services: [{ ...POSTGRES_HEALTHY, state: "degraded", checked_at: T1 }],
          },
        }),
      );
      await manual.promise;
    });

    const row = screen.getByTestId("health-row-postgres");
    expect(row).toHaveTextContent("Saudável");
    expect(row).not.toHaveTextContent("Degradado");
    // The timestamp must describe the collection actually on screen.
    expect(
      screen.getByText(`Última coleta: ${new Date(T2).toLocaleString("pt-BR")}`),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(`Última coleta: ${new Date(T1).toLocaleString("pt-BR")}`),
    ).not.toBeInTheDocument();
    vi.useRealTimers();
  });

  it("keeps the last good collection when a background refresh fails", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let listings = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes("/health/services")) {
        listings += 1;
        return listings === 1
          ? jsonResponse(healthSnapshotPayload())
          : errorResponse(503, "unavailable");
      }
      throw new Error(`unstubbed: ${String(input)}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<HealthCenterPage />, OBSERVER);
    await screen.findByRole("table");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS);
    });

    // A failed background refresh is not applied, and it does not blank the
    // page: the timestamp on screen still says how old what is shown is.
    expect(screen.getByTestId("health-row-livekit")).toBeInTheDocument();
    vi.useRealTimers();
  });

  it("explains every state on the page itself", async () => {
    renderHealthCenter();
    await screen.findByRole("table");

    const legend = screen.getByRole("region", { name: "O que cada estado significa" });
    expect(
      within(legend).getByText(/Integração desligada na configuração deste ambiente/),
    ).toBeInTheDocument();
    expect(within(legend).getByText(/Não é saudável/)).toBeInTheDocument();
  });

  it("is operable from the keyboard alone", async () => {
    renderHealthCenter(healthSnapshotPayload([LIVEKIT_UNAVAILABLE]));
    await screen.findByRole("table");

    // Tab order: filter, sort, refresh, then the row's disclosure.
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Atualizar agora" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByLabelText("Filtrar por estado")).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByLabelText("Ordenar por")).toHaveFocus();
    await userEvent.tab();

    const toggle = screen.getByRole("button", { name: "Detalhes" });
    expect(toggle).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    expect(screen.getByRole("button", { name: "Ocultar" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });
});
