import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * #492 — behaviors that genuinely need a real browser: a real pixel
 * threshold for the go-to-bottom button, a real IntersectionObserver
 * confirming the true tail, and a real reflow ("layout shift") that must not
 * strand the scroll-to-bottom animation partway through. Everything else
 * (state-machine transitions, mark-read gating, anchor priority) is covered
 * by ChatMessageArea.test.tsx in jsdom, where these three cannot be
 * faithfully simulated.
 *
 * The shared messagingApi mock always answers GET messages with every seeded
 * message in one page (next_cursor: "") — it does not fake keyset pagination
 * — so these specs stay within a single page and do not exercise the bounded
 * backward search for an unread boundary older than the first page; that
 * path is unit-tested in ChatMessageArea.test.tsx instead.
 */

test.describe("chat scroll navigation (#492)", () => {
  test("opens a long channel at the first unread message, shows the divider, and marks it read only once the real bottom is reached", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "scroll-unread");
    const readMessages = Array.from({ length: 25 }, (_, i) =>
      makeMessage({
        id: `${targetId}-read-${i}`,
        sender_id: OTHER_USER_ID,
        sender_display_name: OTHER_USER_NAME,
        body_text: `Mensagem antiga ${i}`,
        created_at: `2026-07-15T10:${String(i).padStart(2, "0")}:00.000Z`,
      }),
    );
    const unreadMessages = Array.from({ length: 30 }, (_, i) =>
      makeMessage({
        id: `${targetId}-unread-${i}`,
        sender_id: OTHER_USER_ID,
        sender_display_name: OTHER_USER_NAME,
        body_text: `Mensagem não lida ${i}`,
        created_at: `2026-07-15T11:${String(i).padStart(2, "0")}:00.000Z`,
      }),
    );
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal com não lidas",
      messages: [...readMessages, ...unreadMessages],
    });
    scenario.sidebarChannels[0].unread_count = unreadMessages.length;

    await installMessagingMocks(page, scenario);

    const readReceipt = page.waitForRequest(
      (request) =>
        request.url().endsWith(`/api/chat/channels/${targetId}/read`) &&
        request.method() === "POST",
    );

    await page.goto(`/chat/channel/${targetId}`);

    // Lands directly on the boundary: the separator is visible without any
    // scrolling, and the container is not scrolled all the way to its real
    // tail (there is still content below the viewport — the read history
    // above the boundary is not shown, but the container did not jump past
    // the unread region to the true bottom either).
    await expect(page.getByText("Novas mensagens")).toBeInViewport();
    const list = page.locator('[role="log"]');
    await expect(async () => {
      const atRealBottom = await list.evaluate(
        (el) => el.scrollTop + el.clientHeight >= el.scrollHeight - 1,
      );
      expect(atRealBottom).toBe(false);
    }).toPass({ timeout: 2000 });

    // Opening alone must not have sent the receipt.
    let receiptSent = false;
    void readReceipt.then(() => {
      receiptSent = true;
    });
    await page.waitForTimeout(300);
    expect(receiptSent).toBe(false);

    // Scroll the real container to its real bottom.
    await page.locator('[role="log"]').evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });

    await readReceipt;
  });

  test("go-to-bottom reaches the true tail even when content grows during the animation", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "scroll-layout-shift");
    const messages = Array.from({ length: 40 }, (_, i) =>
      makeMessage({
        id: `${targetId}-msg-${i}`,
        sender_id: i % 5 === 0 ? OTHER_USER_ID : CURRENT_USER_ID,
        sender_display_name: i % 5 === 0 ? OTHER_USER_NAME : undefined,
        body_text: `Mensagem ${i}`,
        created_at: `2026-07-15T09:${String(i % 60).padStart(2, "0")}:00.000Z`,
      }),
    );
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal com scroll longo",
      messages,
    });
    await installMessagingMocks(page, scenario);

    await page.goto(`/chat/channel/${targetId}`);
    await expect(page.getByText("Mensagem 39")).toBeInViewport();

    const list = page.locator('[role="log"]');
    await list.evaluate((el) => {
      el.scrollTop = 0;
    });

    const button = page.getByRole("button", { name: /Ir para o final da conversa/ });
    await expect(button).toBeVisible();
    await button.click();

    // Simulate a media element finishing its load mid-animation: grow the
    // last message's height well after the click, the way an image would
    // once it has dimensions. The operation must not have already ended.
    // Grown inside that message's own bubble — never as a new trailing node
    // after the bottom sentinel, which no real attachment could ever be:
    // an attachment always grows a message that already sits before it.
    await page.waitForTimeout(50);
    await page.locator(`[data-message-id="${targetId}-msg-39"]`).evaluate((el) => {
      const filler = document.createElement("div");
      filler.style.height = "600px";
      filler.setAttribute("data-testid", "layout-shift-filler");
      el.appendChild(filler);
    });

    // The button only disappears once the bottom sentinel actually confirms
    // arrival at the (now taller) real tail — not merely once scrollIntoView
    // returned.
    await expect(button).toBeHidden({ timeout: 5000 });
    await expect(page.getByTestId("layout-shift-filler")).toBeInViewport();
  });

  // #788: a follow-up to #492 — a conversation resolved to the bottom on
  // initial open must stay pinned to the real tail through a later async
  // reflow (an attachment/document/media preview finishing its layout), not
  // merely during an explicit "go to bottom" animation (the case above).
  test("stays at the real tail when historical content grows asynchronously after opening at the bottom", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "scroll-initial-reflow");
    const messages = Array.from({ length: 60 }, (_, i) =>
      makeMessage({
        id: `${targetId}-msg-${i}`,
        sender_id: i % 7 === 0 ? OTHER_USER_ID : CURRENT_USER_ID,
        sender_display_name: i % 7 === 0 ? OTHER_USER_NAME : undefined,
        body_text: `Mensagem ${i}`,
        created_at: `2026-07-15T09:${String(i % 60).padStart(2, "0")}:00.000Z`,
      }),
    );
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal com reflow assíncrono",
      messages,
    });
    // unread_count stays 0 (createScenario's default) and no viewport anchor
    // is seeded — resolution has no unread/history to land on, so it targets
    // the bottom.
    await installMessagingMocks(page, scenario);

    await page.goto(`/chat/channel/${targetId}`);

    // Initial resolution lands on the real tail.
    await expect(page.getByText("Mensagem 59")).toBeInViewport();
    const bottomSentinel = page.getByTestId("chat-bottom-sentinel");
    await expect(bottomSentinel).toBeInViewport();

    // A historical message's attachment/document/media preview finishes
    // loading and grows well after the initial positioning — simulated
    // generically, on an ordinary message bubble, never coupled to any
    // specific attachment component's internals.
    await page.waitForTimeout(50);
    await page.locator(`[data-message-id="${targetId}-msg-10"]`).evaluate((el) => {
      const filler = document.createElement("div");
      filler.style.height = "800px";
      filler.setAttribute("data-testid", "reflow-filler");
      el.appendChild(filler);
    });

    // The real bottom is regained without any user action, and the reflow
    // alone never surfaces "Ir para o final".
    await expect(bottomSentinel).toBeInViewport();
    await expect(page.getByText("Mensagem 59")).toBeInViewport();
    await expect(page.getByRole("button", { name: /Ir para o final da conversa/ })).toBeHidden();
  });
});
