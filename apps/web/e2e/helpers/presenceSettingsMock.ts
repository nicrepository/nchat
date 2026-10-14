import type { Page } from "@playwright/test";

/**
 * A stand-in for chat-service's /api/chat/presence/me (issue #798).
 *
 * The state lives here, in the test process, so two tabs of the same account
 * talk to one "server" — which is what lets a spec prove that a status chosen
 * in one tab reaches another. It validates what the real handler validates
 * (an allowlisted state, a future expiry, no identity in the body) so a client
 * that started sending something else would fail here as it would in
 * production.
 *
 * It behaves as the real one does around the two moments a client cannot see:
 * a write tells every session of the user (`presence.settings_changed`) before
 * it answers, and a stored state whose end has passed on the server's clock
 * reads as automatic — without anybody being told, because the real server's
 * hint for an expiry comes from a sweep a spec cannot schedule.
 */
export interface PresenceSettingsServer {
  state: string | null;
  expiresAt: string | null;
  /** The rollout gate: closed, reads answer and writes are refused with 503. */
  writable: boolean;
  /** The server's clock; a spec that moves the browser's moves this too. */
  now: () => number;
  requests: Array<{ method: string; body?: Record<string, unknown> }>;
  /** Every page whose sessions belong to this user. */
  pages: Page[];
}

const MANUAL_STATES = new Set(["available", "busy", "dnd", "brb", "away", "appear_offline"]);

export function createPresenceSettingsServer(): PresenceSettingsServer {
  return {
    state: null,
    expiresAt: null,
    writable: true,
    now: () => Date.now(),
    requests: [],
    pages: [],
  };
}

function body(server: PresenceSettingsServer) {
  const expired = server.expiresAt !== null && Date.parse(server.expiresAt) <= server.now();
  const state = expired ? null : server.state;
  const expiresAt = expired ? null : server.expiresAt;
  return JSON.stringify({
    data: { state, expires_at: expiresAt, writable: server.writable },
  });
}

/** What the real handler does after a write, before it answers. */
async function tellSessions(server: PresenceSettingsServer) {
  for (const page of server.pages) await emitPresenceSettingsChanged(page);
}

function invalid(request: Record<string, unknown>, now: number): boolean {
  const keys = Object.keys(request).sort().join(",");
  const expires = Date.parse(String(request["expires_at"]));
  return (
    keys !== "expires_at,state" ||
    !MANUAL_STATES.has(String(request["state"])) ||
    !Number.isFinite(expires) ||
    expires < now + 60_000
  );
}

export async function installPresenceSettingsServer(page: Page, server: PresenceSettingsServer) {
  server.pages.push(page);
  await page.route("**/api/chat/presence/me", async (route) => {
    const method = route.request().method();
    if (method === "GET") {
      server.requests.push({ method });
      await route.fulfill({ status: 200, contentType: "application/json", body: body(server) });
      return;
    }
    if (!server.writable) {
      server.requests.push({ method });
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "manual_presence_unavailable", message: "manual presence is not enabled" },
        }),
      });
      return;
    }
    if (method === "DELETE") {
      server.requests.push({ method });
      server.state = null;
      server.expiresAt = null;
      await tellSessions(server);
      await route.fulfill({ status: 200, contentType: "application/json", body: body(server) });
      return;
    }
    const request = route.request().postDataJSON() as Record<string, unknown>;
    server.requests.push({ method, body: request });
    if (invalid(request, server.now())) {
      await route.fulfill({
        status: 400,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "bad_request", message: "invalid request" } }),
      });
      return;
    }
    server.state = String(request["state"]);
    server.expiresAt = new Date(String(request["expires_at"])).toISOString();
    await tellSessions(server);
    await route.fulfill({ status: 200, contentType: "application/json", body: body(server) });
  });
}

/** The hint the real server sends a user's sessions when their settings change. */
export async function emitPresenceSettingsChanged(page: Page) {
  await page.waitForFunction(
    () =>
      typeof (window as unknown as { __e2eEmitWebSocketEvent?: unknown })
        .__e2eEmitWebSocketEvent === "function",
  );
  await page.evaluate(() => {
    (
      window as unknown as { __e2eEmitWebSocketEvent: (event: Record<string, unknown>) => void }
    ).__e2eEmitWebSocketEvent({ schema_version: 1, type: "presence.settings_changed" });
  });
}

/** Every frame this tab has sent over its socket. */
export async function sentFrames(page: Page): Promise<Array<Record<string, unknown>>> {
  return page.evaluate(() =>
    (
      window as unknown as { __e2eWebSocketMessages: () => Array<Record<string, unknown>> }
    ).__e2eWebSocketMessages(),
  );
}

/**
 * Leaves the tab as a person switching to another one does: the document is
 * hidden and the window loses focus. Nothing about the browser's own tab
 * switching is simulated beyond what the application can observe.
 */
export async function leaveTab(page: Page) {
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("blur"));
  });
}

/** Comes back to the tab: visible, focused. */
export async function returnToTab(page: Page) {
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("focus"));
  });
}
