import { afterEach, describe, expect, it, vi } from "vitest";

import { AdminApiError } from "./client";
import {
  getOverview,
  listHealthChecks,
  parseHealthState,
  refreshHealthChecks,
} from "./observabilityApi";
import {
  healthSnapshotPayload,
  overviewPayload,
  POSTGRES_HEALTHY,
  SMTP_DISABLED,
} from "../test/healthFixtures";
import { jsonResponse } from "../test/harness";

afterEach(() => {
  vi.restoreAllMocks();
});

function stubFetch(response: Response) {
  // Typed with the arguments fetch really receives, so a spec can assert what
  // travelled — the URL, the method, and above all the absence of a body.
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    void input;
    void init;
    return Promise.resolve(response);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("parseHealthState", () => {
  it("accepts the five declared states", () => {
    for (const state of ["healthy", "degraded", "unavailable", "disabled", "unknown"]) {
      expect(parseHealthState(state)).toBe(state);
    }
  });

  it("degrades an unrecognised state into unknown, never into healthy", () => {
    // A console meeting a newer server should report "we do not know" rather
    // than guessing — the same rule the server applies to itself.
    expect(parseHealthState("mostly_fine")).toBe("unknown");
    expect(parseHealthState("")).toBe("unknown");
  });
});

describe("listHealthChecks", () => {
  it("reads the snapshot in one request", async () => {
    const fetchMock = stubFetch(jsonResponse(healthSnapshotPayload()));
    const snapshot = await listHealthChecks();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0][0])).toBe("/api/admin/health/services");
    expect(snapshot.services).toHaveLength(5);
    expect(snapshot.overall).toBe("degraded");
  });

  it("keeps an unmeasured latency null rather than zero", async () => {
    stubFetch(jsonResponse(healthSnapshotPayload([SMTP_DISABLED, POSTGRES_HEALTHY])));
    const snapshot = await listHealthChecks();

    const smtp = snapshot.services.find((service) => service.id === "smtp");
    const postgres = snapshot.services.find((service) => service.id === "postgres");
    expect(smtp?.latencyMS).toBeNull();
    expect(postgres?.latencyMS).toBe(12);
  });

  it("defaults the fields the API omits when there is nothing to say", async () => {
    stubFetch(jsonResponse(healthSnapshotPayload([POSTGRES_HEALTHY])));
    const [postgres] = (await listHealthChecks()).services;

    expect(postgres.errorCategory).toBe("");
    expect(postgres.detail).toBe("");
    expect(postgres.version).toBe("");
  });

  it("refuses a response that is not the shape the API promises", async () => {
    stubFetch(jsonResponse({ data: { collected_at: 42, overall: "healthy", services: [] } }));
    await expect(listHealthChecks()).rejects.toBeInstanceOf(AdminApiError);
  });

  it("refuses a service entry missing a required field", async () => {
    const broken = { ...POSTGRES_HEALTHY } as Record<string, unknown>;
    delete broken.state;
    stubFetch(jsonResponse(healthSnapshotPayload([broken])));
    await expect(listHealthChecks()).rejects.toBeInstanceOf(AdminApiError);
  });
});

describe("refreshHealthChecks", () => {
  it("posts, and sends no body at all", async () => {
    const fetchMock = stubFetch(jsonResponse(healthSnapshotPayload()));
    await refreshHealthChecks();

    const init = fetchMock.mock.calls[0][1];
    expect(init?.method).toBe("POST");
    // There is nothing about a refresh for a client to parameterise, and a
    // body is where a destination would travel if one ever could.
    expect(init?.body).toBeUndefined();
    expect(String(fetchMock.mock.calls[0][0])).toBe("/api/admin/health/refresh");
  });
});

describe("getOverview", () => {
  it("reads the whole dashboard in one request", async () => {
    const fetchMock = stubFetch(jsonResponse(overviewPayload()));
    const summary = await getOverview();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(summary.overall).toBe("degraded");
    expect(summary.metrics).toHaveLength(3);
    expect(summary.alerts).toHaveLength(1);
    expect(summary.stateCounts.unavailable).toBe(1);
  });

  it("carries an unavailable metric as null rather than as zero", async () => {
    stubFetch(
      jsonResponse(
        overviewPayload({
          metrics_available: false,
          metrics: [
            {
              key: "users.total",
              label: "Usuários",
              definition: "Contas existentes.",
              window: "cumulative",
              unit: "count",
              available: false,
            },
          ],
        }),
      ),
    );
    const summary = await getOverview();
    expect(summary.metricsAvailable).toBe(false);
    expect(summary.metrics[0].value).toBeNull();
  });

  it("refuses a metric that claims to be available with no number", async () => {
    // Rendering that as zero would put an invented figure on an operational
    // dashboard, which is the one thing this contract exists to prevent.
    stubFetch(
      jsonResponse(
        overviewPayload({
          metrics: [
            {
              key: "users.total",
              label: "Usuários",
              definition: "Contas existentes.",
              window: "cumulative",
              unit: "count",
              available: true,
            },
          ],
        }),
      ),
    );
    await expect(getOverview()).rejects.toBeInstanceOf(AdminApiError);
  });

  it("refuses a metric with a window this build does not know", async () => {
    stubFetch(
      jsonResponse(
        overviewPayload({
          metrics: [
            {
              key: "users.total",
              label: "Usuários",
              definition: "Contas existentes.",
              window: "last_decade",
              unit: "count",
              value: 1,
              available: true,
            },
          ],
        }),
      ),
    );
    await expect(getOverview()).rejects.toBeInstanceOf(AdminApiError);
  });

  it("fills in a state counter the server omitted", async () => {
    stubFetch(jsonResponse(overviewPayload({ state_counts: { healthy: 2 } })));
    const summary = await getOverview();

    // These count rows in the same payload, so a missing key really does mean
    // none of them — unlike a metric, where it means "we could not find out".
    expect(summary.stateCounts.healthy).toBe(2);
    expect(summary.stateCounts.unknown).toBe(0);
  });

  it("propagates an authorization failure as an AdminApiError", async () => {
    stubFetch(jsonResponse({ error: { code: "forbidden", message: "forbidden" } }, 403));
    await expect(getOverview()).rejects.toMatchObject({ status: 403 });
  });
});
