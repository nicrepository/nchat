import { expect, test, type Browser, type Page, type TestInfo } from "@playwright/test";

import {
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  dropWebSocket,
  emitMessageCreated,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  messagesFor,
  uniqueId,
  type MessagingScenario,
} from "../helpers/messagingApi";

/**
 * Issue #947 — the timeline converges on what was persisted, without a reload.
 *
 * Two browser contexts share one mocked chat-service: what context A posts is
 * what context B's listing returns, exactly as two sessions share PostgreSQL.
 * B's transport gap is a real one — the browser goes offline, the socket drops
 * the way a network failure drops it, and coming back online is the app's own
 * reconnect path. Nothing here injects DOM or calls a test-only recovery hook.
 *
 * What these cannot prove is the server's half: that a skipped re-check marks a
 * gap, that recovery proves access before signalling, that a second replica owns
 * its own connections' gaps. Those live in
 * services/chat-service/internal/ws/hub_sync_recovery_test.go, where the
 * authorizer can actually fail.
 */

async function openConversation(page: Page, scenario: MessagingScenario) {
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/dm/${scenario.targetId}`);
  await expect(page.getByTestId("chat-composer-input")).toBeVisible();
  // A marker that only survives if the document is never reloaded.
  await page.evaluate(() => {
    (window as unknown as { __e2eSameDocument?: boolean }).__e2eSameDocument = true;
  });
}

async function sameDocument(page: Page): Promise<boolean> {
  return page.evaluate(
    () => (window as unknown as { __e2eSameDocument?: boolean }).__e2eSameDocument === true,
  );
}

async function send(page: Page, targetId: string, text: string) {
  await fillComposer(page, text);
  const posted = page.waitForResponse(
    (response) =>
      response.url().includes(`/api/chat/dm/${targetId}/messages`) &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Enviar mensagem" }).click();
  await posted;
}

async function twoSessions(browser: Browser, testInfo: TestInfo) {
  const targetId = uniqueId(testInfo, "dm");
  const scenario = createScenario({
    kind: "dm",
    targetId,
    targetName: OTHER_USER_NAME,
    messages: [makeMessage({ id: `${targetId}-m100`, body_text: "antes da queda" })],
  });
  const contextA = await browser.newContext();
  const contextB = await browser.newContext();
  const pageA = await contextA.newPage();
  const pageB = await contextB.newPage();
  await openConversation(pageA, scenario);
  await openConversation(pageB, scenario);
  await expect(messageBubble(pageB, `${targetId}-m100`)).toBeVisible();
  return { targetId, scenario, contextA, contextB, pageA, pageB };
}

test.describe("convergência do realtime após reconexão (#947)", () => {
  test("mensagens enviadas durante a queda aparecem em B sem reload, uma vez cada", async ({
    browser,
  }, testInfo) => {
    const { targetId, scenario, contextA, contextB, pageA, pageB } = await twoSessions(
      browser,
      testInfo,
    );
    const texts = ["gap M101", "gap M102", "gap M103"];

    // B loses the network; its socket drops with no close code, as it would.
    await contextB.setOffline(true);
    await dropWebSocket(pageB);

    for (const text of texts) await send(pageA, targetId, text);
    const persisted = messagesFor(scenario).filter((message) => texts.includes(message.body_text));
    expect(persisted).toHaveLength(3);
    for (const message of persisted) await expect(messageBubble(pageA, message.id)).toBeVisible();
    // Nothing reached B while it was offline.
    for (const message of persisted) await expect(messageBubble(pageB, message.id)).toHaveCount(0);

    await contextB.setOffline(false);

    for (const message of persisted) {
      await expect(messageBubble(pageB, message.id)).toHaveCount(1);
      await expect(messageBubble(pageB, message.id)).toContainText(message.body_text);
    }
    expect(await sameDocument(pageB)).toBe(true);

    // The socket redelivering a message the catch-up already read changes
    // nothing: identity is the message id.
    await emitMessageCreated(pageB, scenario, {
      kind: "dm",
      targetId,
      message: persisted[1],
      eventId: `${persisted[1].id}-redelivered`,
    });
    for (const message of persisted) await expect(messageBubble(pageB, message.id)).toHaveCount(1);
    await expect(messageBubble(pageB, `${targetId}-m100`)).toHaveCount(1);

    await contextA.close();
    await contextB.close();
  });

  // #947 follow-up (P3): something persisted after the database answered the
  // first page and before the subscription existed is in neither, and no event
  // will ever carry it. The first subscription owes a catch-up for exactly that.
  test("uma mensagem persistida entre a primeira página e a assinatura aparece sem reload", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "primeira" })],
    });
    await installMessagingMocks(page, scenario);
    const late = makeMessage({
      id: `${targetId}-m2`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "persistida na janela",
      created_at: "2026-07-15T12:05:00.000Z",
      updated_at: "2026-07-15T12:05:00.000Z",
    });
    let snapshotTaken = false;
    // Registered after the shared mocks, so it answers first: the first page is
    // the store as it was, and the message lands in the store right after.
    await page.route(`**/api/chat/dm/${targetId}/messages`, async (route) => {
      if (route.request().method() !== "GET" || snapshotTaken) {
        await route.fallback();
        return;
      }
      snapshotTaken = true;
      const body = JSON.stringify({
        data: { messages: [...messagesFor(scenario)], next_cursor: "" },
      });
      messagesFor(scenario).push(late);
      await route.fulfill({ status: 200, contentType: "application/json", body });
    });

    await page.goto(`/chat/dm/${targetId}`);
    await expect(messageBubble(page, `${targetId}-m1`)).toBeVisible();
    await page.evaluate(() => {
      (window as unknown as { __e2eSameDocument?: boolean }).__e2eSameDocument = true;
    });

    await expect(messageBubble(page, late.id)).toContainText("persistida na janela");
    await expect(messageBubble(page, late.id)).toHaveCount(1);
    expect(await sameDocument(page)).toBe(true);
  });

  // The client half of a skipped delivery: the server could not prove access
  // when the message was published, so the event never came; once it can, it
  // sends conversation.sync_required and the client reads the listing back.
  test("um pedido de ressincronização do servidor traz a mensagem que o socket não entregou", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "primeira" })],
    });
    await openConversation(page, scenario);
    await expect(messageBubble(page, `${targetId}-m1`)).toBeVisible();

    const skipped = makeMessage({
      id: `${targetId}-m2`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "entregue só pela reconciliação",
      created_at: "2026-07-15T12:05:00.000Z",
      updated_at: "2026-07-15T12:05:00.000Z",
    });
    messagesFor(scenario).push(skipped);
    await expect(messageBubble(page, skipped.id)).toHaveCount(0);

    await page.evaluate((id) => {
      (
        window as unknown as { __e2eEmitWebSocketEvent: (event: Record<string, unknown>) => void }
      ).__e2eEmitWebSocketEvent({
        type: "conversation.sync_required",
        target_type: "dm",
        target_id: id,
      });
    }, targetId);

    await expect(messageBubble(page, skipped.id)).toContainText("entregue só pela reconciliação");
    await expect(messageBubble(page, skipped.id)).toHaveCount(1);
    expect(await sameDocument(page)).toBe(true);
  });
});
