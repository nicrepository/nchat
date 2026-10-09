import { expect, test, type Page, type TestInfo } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  emitConversationAvailable,
  emitMessageCreated,
  installMessagingMocks,
  makeMessage,
  uniqueId,
  type MessagingScenario,
} from "../helpers/messagingApi";

/**
 * #1082 — read state follows what the reader actually saw, not the tail.
 *
 * The sidebar badge used to stay at N until the bottom sentinel confirmed the
 * very last pixel; these specs prove it now falls as rows reach the screen,
 * never because a conversation was merely opened, and never back up.
 *
 * The messaging mock implements the server's read cursor (forward-only, unread
 * derived from it, the read_cursor marker and precise_read_cursor capability —
 * see messagingApi's …/read route), so a sidebar refetch here reports what the
 * backend would. The backend's own guarantees — validation,
 * monotonicity under concurrent and out-of-order writes — are proven against
 * PostgreSQL in conversation_read_state_store_postgres_test.go: a mocked E2E
 * cannot prove them, so it does not pretend to.
 */

const LIST = ".chat-msg-area__list";

function conversation(testInfo: TestInfo, label: string, read: number, unread: number) {
  const targetId = uniqueId(testInfo, label);
  const at = (minute: number) =>
    new Date(Date.UTC(2026, 6, 15, 9, 0, 0) + minute * 60_000).toISOString();
  const messages = [
    ...Array.from({ length: read }, (_, i) =>
      makeMessage({
        id: `${targetId}-read-${i}`,
        sender_id: OTHER_USER_ID,
        sender_display_name: OTHER_USER_NAME,
        body_text: `Mensagem lida ${i}`,
        created_at: at(i),
      }),
    ),
    ...Array.from({ length: unread }, (_, i) =>
      makeMessage({
        id: `${targetId}-unread-${i}`,
        sender_id: OTHER_USER_ID,
        sender_display_name: OTHER_USER_NAME,
        body_text: `Mensagem não lida ${i}`,
        created_at: at(read + i),
      }),
    ),
  ];
  const scenario = createScenario({ kind: "channel", targetId, targetName: "Leitura", messages });
  scenario.sidebarChannels.find((channel) => channel.id === targetId)!.unread_count = unread;
  // The server's cursor on the last read message, so the sidebar reports the
  // full read state — count and read point — as the backend does.
  scenario.readCursors.set(`channel:${targetId}`, read - 1);
  return { targetId, scenario, lastId: messages[messages.length - 1].id };
}

/**
 * The target's sidebar badge as a number; 0 when there is no badge at all.
 *
 * One read inside the page, deliberately: asking "is there a badge" and then
 * "what does it say" as two locator calls races the badge disappearing in
 * between, and the second call then waits for an element that is gone.
 */
function sidebarUnread(page: Page): Promise<number> {
  return page.evaluate(() => {
    const badge = document.querySelector(".chat-sidebar__unread-badge");
    if (!badge) return 0;
    return Number(/\d+/.exec(badge.getAttribute("aria-label") ?? "")?.[0] ?? Number.NaN);
  });
}

/**
 * The opening trip to the unread boundary has ended: the separator is on
 * screen and the contextual control, which only takes its settled form once
 * the navigation hands the scrollport back, offers the end with a count.
 */
async function landedOnBoundary(page: Page) {
  await expect(page.getByRole("separator", { name: "Novas mensagens" })).toBeInViewport();
  await expect(
    page.getByRole("button", { name: /^Ir para o final da conversa, \d+ novas mensagens$/ }),
  ).toBeVisible();
}

/** The opening trip to the end has ended: the real tail is on screen. */
async function landedOnTail(page: Page) {
  await expect.poll(() => distanceFromTail(page)).toBeLessThanOrEqual(2);
  await expect(page.getByTestId("chat-bottom-sentinel")).toBeInViewport();
  await expect(page.getByRole("button", { name: /Ir para o final|Começar pelas/ })).toBeHidden();
}

function distanceFromTail(page: Page) {
  return page.locator(LIST).evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight);
}

/** The unread count the mocked server holds for the target right now. */
function serverUnread(scenario: MessagingScenario, targetId: string) {
  const key = `channel:${targetId}`;
  const cursor = scenario.readCursors.get(key) ?? -1;
  return scenario.messagesByTarget
    .get(key)!
    .slice(cursor + 1)
    .filter((message) => message.status === "active" && message.sender_id !== CURRENT_USER_ID)
    .length;
}

/** The message ids the server's cursor was moved to, in request order. */
function cursorWrites(scenario: MessagingScenario) {
  return scenario.requests.reads.map((read) => read.lastReadMessageId);
}

test.describe("read cursor (#1082)", () => {
  test("the badge falls as messages are read, and clears without reaching the last pixel", async ({
    page,
  }, testInfo) => {
    const { targetId, scenario, lastId } = conversation(testInfo, "progressive", 20, 30);
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    // Opens on the boundary. The rows that landed on screen are read; the rest
    // of the thirty are not, so the badge has moved but is far from gone.
    await landedOnBoundary(page);
    await expect.poll(() => sidebarUnread(page)).toBeLessThan(30);
    const afterOpening = await sidebarUnread(page);
    expect(afterOpening).toBeGreaterThan(0);
    expect(await distanceFromTail(page)).toBeGreaterThan(2);

    // Half a screen further: fewer again, still not zero.
    await page.locator(LIST).evaluate((el) => {
      el.scrollTop += el.clientHeight / 2;
    });
    await expect.poll(() => sidebarUnread(page)).toBeLessThan(afterOpening);
    expect(await sidebarUnread(page)).toBeGreaterThan(0);

    // The last message mostly on screen, a third of it still below the fold:
    // nowhere near the tail's 2px confirmation, and the badge clears anyway.
    await page.locator(LIST).evaluate((el) => {
      const rows = el.querySelectorAll("[data-message-id]");
      const last = rows[rows.length - 1].getBoundingClientRect();
      el.scrollTop += last.bottom - el.getBoundingClientRect().bottom - last.height / 3;
    });
    await expect.poll(() => sidebarUnread(page)).toBe(0);
    expect(await distanceFromTail(page)).toBeGreaterThan(2);

    // A handful of coalesced writes, each further than the last, ending on the
    // last message — never one per message read.
    await expect.poll(() => cursorWrites(scenario).at(-1)).toBe(lastId);
    const writes = cursorWrites(scenario);
    expect(writes.length).toBeLessThan(10);
    const order = writes.map((id) =>
      scenario.messagesByTarget.get(`channel:${targetId}`)!.findIndex((m) => m.id === id),
    );
    expect([...order].sort((a, b) => a - b)).toEqual(order);
    expect(new Set(order).size).toBe(order.length);
  });

  // R3 (#1082 third review): a refetch that brings messages realtime never
  // delivered shows them, on top of exactly what this session already read.
  test("messages realtime missed show on the next refetch, beside what was read", async ({
    page,
  }, testInfo) => {
    const { targetId, scenario } = conversation(testInfo, "missed", 20, 30);
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await landedOnBoundary(page);
    // The reads were persisted, and the badge shows the server's answer: fewer
    // than the thirty it opened with, more than none.
    await expect
      .poll(async () => {
        const server = serverUnread(scenario, targetId);
        return server < 30 && (await sidebarUnread(page)) === server;
      })
      .toBe(true);
    expect(serverUnread(scenario, targetId)).toBeGreaterThan(0);

    // Four messages reach the server with no realtime event for them.
    scenario.messagesByTarget.get(`channel:${targetId}`)!.push(
      ...Array.from({ length: 4 }, (_, i) =>
        makeMessage({
          id: `${targetId}-missed-${i}`,
          sender_id: OTHER_USER_ID,
          sender_display_name: OTHER_USER_NAME,
          body_text: `Mensagem perdida ${i}`,
          created_at: new Date(Date.UTC(2026, 6, 15, 12, i)).toISOString(),
        }),
      ),
    );
    await emitConversationAvailable(page, { kind: "channel", targetId });

    // The refetch brings them: the badge is the server's count, the four
    // missed messages included and nothing read hidden.
    await expect.poll(() => sidebarUnread(page)).toBe(serverUnread(scenario, targetId));
    expect(serverUnread(scenario, targetId)).toBeGreaterThanOrEqual(4);
  });

  test("the contextual control leads to the boundary, then the end, clearing the badge in one trip", async ({
    page,
  }, testInfo) => {
    const { targetId, scenario } = conversation(testInfo, "control", 40, 30);
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await landedOnBoundary(page);

    // Up into history: the control offers the boundary, counting what is
    // still unread — the same number the sidebar shows.
    await page.locator(LIST).evaluate((el) => {
      el.scrollTop = 0;
    });
    const toBoundary = page.getByRole("button", { name: /^Começar pelas \d+ novas mensagens$/ });
    await expect(toBoundary).toBeVisible();
    const offered = Number(/\d+/.exec((await toBoundary.getAttribute("aria-label")) ?? "")?.[0]);
    await expect.poll(() => sidebarUnread(page)).toBe(offered);

    await toBoundary.click();
    await expect(page.getByRole("separator", { name: "Novas mensagens" })).toBeInViewport();

    // One press to the end, and the badge is gone: no second press, no nudge.
    await page.getByRole("button", { name: /^Ir para o final da conversa/ }).click();
    await expect.poll(() => sidebarUnread(page)).toBe(0);
    await expect(page.getByRole("button", { name: /Ir para o final|Começar pelas/ })).toBeHidden();
  });

  test("a read row growing later neither re-raises the badge nor blocks it from clearing", async ({
    page,
  }, testInfo) => {
    const { targetId, scenario } = conversation(testInfo, "reflow", 20, 30);
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await landedOnBoundary(page);
    await expect.poll(() => sidebarUnread(page)).toBeLessThan(30);
    const partial = await sidebarUnread(page);
    const heightBefore = await page.locator(LIST).evaluate((el) => el.scrollHeight);

    // A message already read grows by a screenful, the way a preview finishing
    // its layout does, and the virtualizer remeasures it.
    const grownBy = await page
      .locator(`${LIST} [data-message-id="${targetId}-unread-0"]`)
      .evaluate((el) => {
        const before = parseFloat(getComputedStyle(el).paddingBottom) || 0;
        (el as HTMLElement).style.paddingBottom = "600px";
        return 600 - before;
      });
    // The virtualizer has taken the new height in.
    await expect
      .poll(() => page.locator(LIST).evaluate((el) => el.scrollHeight))
      .toBeGreaterThanOrEqual(heightBefore + grownBy);
    expect(await sidebarUnread(page)).toBeLessThanOrEqual(partial);

    await page.locator(LIST).evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    await expect.poll(() => sidebarUnread(page)).toBe(0);
  });

  // The viewport restore after a conversation switch is not exercised here: on
  // develop the anchor a wheel-scrolled timeline leaves behind is saved without
  // a message (anchorMessageId null, a pre-existing #492 defect reproduced on an
  // untouched checkout), so the conversation reopens at the end whatever the
  // read cursor does. What #1082 owns is proven below: reading history never
  // moves the read state, in either direction.
  test("reading history neither re-reads nor un-reads, and an arrival behind the reader waits to be seen", async ({
    page,
  }, testInfo) => {
    const { targetId, scenario } = conversation(testInfo, "history", 60, 0);
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await landedOnTail(page);

    // Up into history with the wheel: everything there was read already, so
    // nothing becomes unread and nothing is written.
    const box = (await page.locator(LIST).boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await expect
      .poll(async () => {
        await page.mouse.wheel(0, -2000);
        return page.locator(LIST).evaluate((el) => el.scrollTop);
      })
      .toBe(0);
    await expect(page.getByRole("button", { name: "Ir para o final da conversa" })).toBeVisible();
    expect(await sidebarUnread(page)).toBe(0);
    expect(cursorWrites(scenario)).toEqual([]);

    // A message lands at the end while the reader is up here: unread, on the
    // badge and on the control, until it is actually on screen.
    const arrived = makeMessage({
      id: `${targetId}-live`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "Chegou enquanto lia",
      created_at: "2026-07-15T12:00:00.000Z",
    });
    await emitMessageCreated(page, scenario, { kind: "channel", targetId, message: arrived });
    await expect.poll(() => sidebarUnread(page)).toBe(1);
    await expect(page.getByRole("button", { name: /1 novas mensagens/ })).toBeVisible();
    expect(cursorWrites(scenario)).toEqual([]);

    await page.getByRole("button", { name: /1 novas mensagens/ }).click();
    await expect.poll(() => sidebarUnread(page)).toBe(0);
    await expect.poll(() => cursorWrites(scenario)).toEqual([arrived.id]);
  });

  test("a message arriving while the reader is at the tail is read as it lands", async ({
    page,
  }, testInfo) => {
    const { targetId, scenario } = conversation(testInfo, "arrival", 30, 0);
    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);
    await landedOnTail(page);

    const arrived = makeMessage({
      id: `${targetId}-live`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "Acabou de chegar",
      created_at: "2026-07-15T12:00:00.000Z",
    });
    await emitMessageCreated(page, scenario, { kind: "channel", targetId, message: arrived });

    await expect(page.getByText("Acabou de chegar")).toBeInViewport();
    await expect.poll(() => cursorWrites(scenario).at(-1)).toBe(arrived.id);
    expect(await sidebarUnread(page)).toBe(0);
  });

  test("a second tab reading further is what the first converges on", async ({
    context,
  }, testInfo) => {
    // Two tabs of the same user over one (mocked) server state.
    const { targetId, scenario, lastId } = conversation(testInfo, "tabs", 20, 30);
    const tabA = await context.newPage();
    const tabB = await context.newPage();
    await installMessagingMocks(tabA, scenario);
    await installMessagingMocks(tabB, scenario);

    await tabA.goto(`/chat/channel/${targetId}`);
    await landedOnBoundary(tabA);
    await expect.poll(() => cursorWrites(scenario).length).toBeGreaterThan(0);
    const readByA = await sidebarUnread(tabA);
    expect(readByA).toBeGreaterThan(0);

    // Tab B opens where A left the server, and reads to the end.
    await tabB.goto(`/chat/channel/${targetId}`);
    await tabB.locator(LIST).evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    await expect.poll(() => cursorWrites(scenario).at(-1)).toBe(lastId);

    // Tab A's next authoritative read converges on B's cursor.
    await tabA.reload();
    await expect.poll(() => sidebarUnread(tabA)).toBe(0);
  });
});
