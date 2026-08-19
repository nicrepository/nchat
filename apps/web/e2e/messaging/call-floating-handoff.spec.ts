import { randomUUID } from "node:crypto";

import { expect, test, type Page, type TestInfo } from "@playwright/test";

import {
  OTHER_USER_ID,
  createScenario,
  installMessagingMocks,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * CALLS-546 floating/dedicated-tab handoff — E2E.
 *
 * DOCUMENTED LIMITATION (code-review achado #5 explicitly requires this be
 * recorded rather than silently absent):
 *
 * No real LiveKit/media-service in this E2E project. This suite runs
 * against the Vite dev server only (see playwright.config.ts webServer);
 * every chat-service call is answered by the instrumented WebSocket mock in
 * ../helpers/messagingApi.ts, never by a real server, and there is no
 * LiveKit SFU to connect to. Because CallSessionProvider only grants local
 * ownership (`ownerState "local"`) from inside the media bridge's connect()
 * call — and only *after* the lease claim — floating "Expandir" is reachable
 * only once a call has genuinely reached that point, which needs a LiveKit
 * connection this environment cannot provide. This blocks: chamada ativa em
 * floating, ação Expandir, handoff conclui, chamada continua ativa, End Call
 * libera mídia, and anything requiring a real single-publisher confirmation,
 * physical devices, or a real Room reconnect (see the skipped block below).
 *
 * FIXED (previously a second blocker, not one of achado #1-#7, discovered
 * while first writing this file): CallSessionProvider used to create its
 * ownership coordinator via `useState(createOwnershipCoordinator)` but only
 * close it from a `useEffect` cleanup. React StrictMode (wrapping the whole
 * app in src/main.tsx) mounts every component, runs its effects, runs every
 * cleanup once, then mounts again — dev only. That permanently closed the
 * coordinator on the very first mount of any authenticated route in
 * `pnpm dev` (which is what this E2E project's webServer runs), before the
 * page ever got to call `.claim()`. Fixed in CallSessionProvider.tsx by
 * moving the coordinator onto a ref whose sole owner is a dedicated
 * lifecycle effect (see coordinatorRef/getOwnership there): the effect
 * recreates a fresh, open instance whenever the current one is closed or
 * absent, so the StrictMode probe closes a throwaway instance and leaves a
 * second, working one active — never a reused closed one. The two tests
 * below (previously `test.fixme`) are the regression coverage for exactly
 * that bug and now run for real.
 *
 * What IS covered below, deterministically and without any LiveKit
 * dependency: DedicatedCallPage's own callId validation, a dedicated tab
 * opened directly claiming ownership immediately, and a stale/reload lease
 * being reclaimed within the bounded recovery window (achado #1's actual
 * fix, exercised end to end through a real browser tab and real
 * localStorage — not mocked ownership).
 */

const LEASE_KEY = "nchat.call.owner.v1";

interface OwnerLease {
  v: 1;
  callId: string;
  tabId: string;
  epoch: number;
  role: "main" | "dedicated";
  expiresAt: number;
}

async function readLease(page: Page): Promise<OwnerLease | null> {
  return page.evaluate((key) => {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as OwnerLease) : null;
  }, LEASE_KEY);
}

function resourceCallEvent(fixture: {
  callId: string;
  requestId: string;
  targetId: string;
  targetType: "dm" | "channel";
}): Record<string, unknown> {
  return {
    type: "call.accepted",
    event_id: `${fixture.callId}-1`,
    target_type: fixture.targetType,
    target_id: fixture.targetId,
    call: {
      call_id: fixture.callId,
      request_id: fixture.requestId,
      caller_id: OTHER_USER_ID,
      call_type: "audio",
      status: "active",
      version: 1,
      created_at: "2026-08-18T12:00:00.000Z",
      occurred_at: "2026-08-18T12:00:00.000Z",
      expires_at: "2026-08-18T13:00:00.000Z",
      accepted_at: "2026-08-18T12:00:00.000Z",
      target_type: fixture.targetType,
      target_id: fixture.targetId,
    },
  };
}

async function stubMediaToken(page: Page) {
  // The dedicated tab's activation attempt (RF-24 resource join) reaches
  // for a LiveKit token once it becomes the local owner. There is no media
  // -service here, so this deliberately never resolves: media.status stays
  // "connecting" forever, which is fine for these scenarios — they assert
  // on the ownership lease, not on media ever reaching "connected".
  await page.route("**/api/media/media/livekit/token", () => {
    // Never fulfilled, on purpose.
  });
}

async function openDedicatedDirectly(
  page: Page,
  testInfo: TestInfo,
  suffix: string,
): Promise<{ callId: string; targetId: string }> {
  const targetId = uniqueId(testInfo, `${suffix}-target`);
  // The dedicated route validates callId as a UUID (DedicatedCallPage's
  // callIDPattern) before it ever reaches call.sync.
  const callId = randomUUID();
  const requestId = uniqueId(testInfo, `${suffix}-request`);
  const scenario = createScenario({
    kind: "dm",
    conversationType: "group",
    targetId,
    targetName: "E2E Grupo",
    messages: [],
  });
  await installMessagingMocks(page, scenario, {
    knownCalls: [
      { callId, event: resourceCallEvent({ callId, requestId, targetId, targetType: "dm" }) },
    ],
  });
  await stubMediaToken(page);
  await page.goto(`/call/${callId}`);
  return { callId, targetId };
}

test.describe("dedicated tab — callId validation (no ownership/LiveKit dependency)", () => {
  test("an invalid call id never resolves and never touches the ownership lease", async ({
    page,
  }, testInfo) => {
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId: uniqueId(testInfo, "invalid-target"),
      targetName: "E2E Grupo",
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await page.goto("/call/not-a-uuid");
    await expect(page.getByRole("alert")).toHaveText("Chamada inválida.");
    expect(await readLease(page)).toBeNull();
  });
});

test.describe("dedicated tab ownership lease — deterministic, no LiveKit required", () => {
  test("opening a dedicated tab directly (no other tab ever existed) claims ownership immediately", async ({
    page,
  }, testInfo) => {
    const { callId } = await openDedicatedDirectly(page, testInfo, "direct-open");

    await expect(page.getByRole("main", { name: /Chamada/ })).toBeVisible();
    await expect.poll(async () => (await readLease(page))?.callId, { timeout: 2_000 }).toBe(callId);
    // No competing owner ever existed, so the "brought back from another
    // tab" indicator must never appear (achado #1: never a silent hang).
    await expect(page.getByText("Chamada aberta em outra aba")).toHaveCount(0);
  });

  test("a stale lease left by this tab's own earlier life is reclaimed within the bounded recovery window (reload)", async ({
    page,
  }, testInfo) => {
    const { callId } = await openDedicatedDirectly(page, testInfo, "reload");
    await expect(page.getByRole("main", { name: /Chamada/ })).toBeVisible();
    await expect.poll(async () => (await readLease(page))?.callId).toBe(callId);

    // Overwrite with a lease as if an *earlier* instance of this dedicated
    // tab owned the call and never cleaned up — a live main tab is not
    // even a possibility here, only a dead, unresponsive former self.
    await page.evaluate(
      ({ key, callId: id }) => {
        localStorage.setItem(
          key,
          JSON.stringify({
            v: 1,
            callId: id,
            tabId: "stale-tab-from-before-reload",
            epoch: 5,
            role: "dedicated",
            expiresAt: Date.now() + 60_000,
          }),
        );
      },
      { key: LEASE_KEY, callId },
    );

    await page.reload();
    await expect(page.getByRole("main", { name: /Chamada/ })).toBeVisible();

    // Never an infinite spinner: within the bounded recovery window the
    // lease is reclaimed by *this* tab at a strictly higher epoch — not
    // left pointing at the dead tab forever.
    await expect
      .poll(async () => (await readLease(page))?.epoch, { timeout: 7_000 })
      .toBeGreaterThan(5);
    const finalLease = await readLease(page);
    expect(finalLease?.callId).toBe(callId);
    expect(finalLease?.tabId).not.toBe("stale-tab-from-before-reload");
    await expect(page.getByText("Chamada aberta em outra aba")).toHaveCount(0);
  });
});

test.describe
  .skip("full floating → dedicated handoff with an active LiveKit session (requires real media-service/LiveKit — not available to this E2E project, see file header)", () => {
  test("chamada ativa aparece como janela flutuante", () => {});
  test("Expandir abre uma dedicated tab em /call/:id", () => {});
  test("o handoff conclui: a dedicated tab passa a ser a única owner", () => {});
  test("a chamada permanece ativa durante e após o handoff", () => {});
  test("fechar a dedicated tab devolve a propriedade para a aba principal (floating)", () => {});
  test("Encerrar chamada libera a mídia em ambas as abas", () => {});
});
