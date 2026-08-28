import { render, type RenderResult } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { ReactElement } from "react";
import { vi } from "vitest";

import type { AdminBootstrap } from "../api/adminApi";
import { AdminSessionContext, type AdminSessionValue } from "../session/AdminSessionContext";

/**
 * Test scaffolding for the management screens.
 *
 * The session is provided as a real context value rather than by mocking the
 * hook module, so the specs exercise the same `can()` the shell uses — which is
 * the point when what is under test is a capability changing what renders.
 */

export const TEST_USER_ID = "11111111-1111-1111-1111-111111111111";

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

export function errorResponse(status: number, code = "forbidden"): Response {
  return jsonResponse({ error: { code, message: code } }, status);
}

export function bootstrapFor(capabilities: string[]): AdminBootstrap {
  return {
    identity: {
      user_id: TEST_USER_ID,
      email: "admin@example.test",
      display_name: "Admin",
      avatar_url: "",
    },
    capabilities,
    environment: "STAGING",
    build: { service: "admin-service", version: "0.0.0", commit: "test" },
    session: {
      idle_expires_at: "2099-01-01T00:00:00Z",
      absolute_expires_at: "2099-01-01T08:00:00Z",
    },
    csrf_token: "csrf-test",
  };
}

export function sessionValue(capabilities: string[]): AdminSessionValue {
  const held = new Set(capabilities);
  return {
    status: "ready",
    bootstrap: bootstrapFor(capabilities),
    message: "",
    reload: vi.fn(),
    adopt: vi.fn(),
    signOut: vi.fn(),
    can: (capability: string) => held.has(capability) || held.has("admin.superuser"),
  };
}

/**
 * Renders a screen inside a router and a session.
 *
 * `initialEntries` exists so a spec can describe a deep link — the integrations
 * cards link into the configuration screen with a search term — without
 * building a second harness for it.
 */
export function renderWithSession(
  ui: ReactElement,
  capabilities: string[],
  initialEntries: string[] = ["/"],
): RenderResult {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <AdminSessionContext.Provider value={sessionValue(capabilities)}>
        {ui}
      </AdminSessionContext.Provider>
    </MemoryRouter>,
  );
}

/**
 * A fetch stub that answers by URL fragment.
 *
 * Routing on the request rather than on call order is what lets a spec assert
 * that a *later* request replaced an earlier one: the order the answers arrive
 * in is the thing under test, so it must not also be the thing the stub relies
 * on.
 */
export function routedFetch(
  routes: { match: string; respond: () => Response | Promise<Response> }[],
) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input.toString();
    for (const route of routes) {
      if (url.includes(route.match)) return route.respond();
    }
    throw new Error(`unstubbed request: ${url}`);
  });
}

/** Records every URL fetch was called with, for asserting query parameters. */
export function requestedURLs(fetchMock: ReturnType<typeof vi.fn>): string[] {
  return fetchMock.mock.calls.map((call) => String(call[0]));
}

/**
 * A promise the test settles by hand.
 *
 * Ordering specs must not be expressed with timers: `setTimeout` makes the
 * outcome depend on how fast the machine happens to be, which is exactly the
 * property a race test cannot have. Holding each response open and settling it
 * at the precise moment the scenario calls for makes the interleaving explicit
 * and the result deterministic.
 */
export function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}
