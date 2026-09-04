import { expect, test, type Page } from "@playwright/test";

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

/**
 * #788: neutralises Chrome's native scroll anchoring (`overflow-anchor`
 * defaults to `auto`) for one page.
 *
 * Two reasons, both measured rather than assumed:
 *
 * - PR #792's first regression test was vacuous — it passed with the
 *   ChatMessageArea tail-lock removed entirely, because the browser's own
 *   anchoring was re-adjusting scrollTop and satisfying every assertion.
 * - A DEV capture of the real conversation with anchoring disabled ended
 *   2051px away from the tail, so anchoring is not what makes the defect
 *   appear or disappear — it only partially compensates for it (the same
 *   capture with anchoring on still ended 1445px away).
 *
 * Adopted via CSSOM rather than an injected <style> so it does not depend on
 * the page's style-src CSP.
 */
async function disableNativeScrollAnchoring(page: Page) {
  await page.addInitScript(() => {
    const install = () => {
      const sheet = new CSSStyleSheet();
      sheet.replaceSync(".chat-msg-area__list { overflow-anchor: none; }");
      document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
    };
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", install, { once: true });
    } else {
      install();
    }
  });
}

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

  /**
   * #788: a conversation resolved to the bottom on initial open must stay
   * pinned to the real tail through a later async reflow (an attachment /
   * document / media preview finishing its layout), not merely during an
   * explicit "go to bottom" animation (the case above).
   *
   * # Why this test switches native scroll anchoring off
   *
   * The first version of this test (PR #792) was vacuous: it passed with the
   * ChatMessageArea tail-lock removed entirely. Chrome's own scroll anchoring
   * (`overflow-anchor: auto`, the default) already re-adjusts scrollTop when
   * content above the anchor node grows, so the browser — not the
   * application — was satisfying every assertion. Verified by mutation:
   * forcing the tail-lock's `holdsTail` to false left this scenario passing
   * while the #492 "go-to-bottom during a layout shift" test above failed.
   *
   * Scroll anchoring is a heuristic with its own suppression rules, so the
   * application cannot delegate the requirement to it. Neutralising it here
   * is what makes the assertions measure ChatMessageArea's own ResizeObserver
   * tail-lock, which is the behaviour #788 asks for. The stylesheet is
   * adopted via CSSOM rather than an injected <style> so it does not depend
   * on the page's style-src CSP.
   */
  test("holds the real tail through an async reflow using its own tail-lock, not the browser's scroll anchoring", async ({
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

    await disableNativeScrollAnchoring(page);

    await page.goto(`/chat/channel/${targetId}`);

    // The real tail, not merely "near" it: the same TAIL_EPSILON_PX tolerance
    // chatViewportState.ts uses, so sub-pixel rounding cannot mask a drift.
    const distanceFromTail = () =>
      page
        .locator(".chat-msg-area__list")
        .evaluate((el) => Math.round(el.scrollHeight - el.scrollTop - el.clientHeight));

    const bottomSentinel = page.getByTestId("chat-bottom-sentinel");
    await expect(page.getByText("Mensagem 59")).toBeInViewport();
    await expect(bottomSentinel).toBeInViewport();
    expect(await distanceFromTail()).toBeLessThanOrEqual(2);

    // A historical message's attachment/document/media preview finishes
    // loading and grows well after the initial positioning — simulated
    // generically, on an ordinary message bubble, never coupled to any
    // specific attachment component's internals, because #788 requires the
    // behaviour for ANY variable-height content.
    await page.waitForTimeout(50);
    await page.locator(`[data-message-id="${targetId}-msg-10"]`).evaluate((el) => {
      const filler = document.createElement("div");
      filler.style.height = "800px";
      filler.setAttribute("data-testid", "reflow-filler");
      el.appendChild(filler);
    });

    // The real bottom is regained without any user action, and the reflow
    // alone never surfaces "Ir para o final".
    await expect.poll(distanceFromTail, { timeout: 5000 }).toBeLessThanOrEqual(2);
    await expect(bottomSentinel).toBeInViewport();
    await expect(page.getByText("Mensagem 59")).toBeInViewport();
    await expect(page.getByRole("button", { name: /Ir para o final da conversa/ })).toBeHidden();
  });

  /**
   * #788 root cause, reproduced from a DEV capture of the real conversation.
   *
   * The measured failing sequence, with native scroll anchoring disabled so
   * only the application's own logic is under test:
   *
   *   T+3967  content grows 21px      → tail-lock pins scrollTop (+21), dist 0
   *   T+3982  the pin's scroll event is delivered — but a SECOND reflow
   *           (+340px) landed in between, so the handler reads a scrollHeight
   *           that already includes it and computes dist=340
   *   T+3982  the ResizeObserver for that +340 runs, and no longer holds the
   *           tail: the scroll handler just recorded "not at the tail"
   *   T+3999  +84px    → uncorrected
   *   T+4016  +1628px  → uncorrected, ending 2051px from the tail
   *
   * The race is reproduced deterministically rather than by timing: a second
   * ResizeObserver registered after ChatMessageArea's own fires immediately
   * after its pin, in the same delivery loop, and grows the timeline again
   * there — so the pin's already-queued scroll event is dispatched against a
   * scrollHeight that has moved underneath it, exactly as captured.
   */
  test("keeps following the tail when a second reflow lands before the previous correction's scroll event", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "scroll-reflow-race");
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
      targetName: "Canal com reflow em cascata",
      messages,
    });
    await installMessagingMocks(page, scenario);
    await disableNativeScrollAnchoring(page);

    await page.goto(`/chat/channel/${targetId}`);

    const distanceFromTail = () =>
      page
        .locator(".chat-msg-area__list")
        .evaluate((el) => Math.round(el.scrollHeight - el.scrollTop - el.clientHeight));

    await expect(page.getByText("Mensagem 59")).toBeInViewport();
    expect(await distanceFromTail()).toBeLessThanOrEqual(2);

    // Arm the racer: its first (initial) notification is consumed here, two
    // frames before the reflow, so the next one is genuinely the first growth.
    await page.evaluate((msgId) => {
      const content = document.querySelector(".chat-msg-area__list-content") as HTMLElement;
      const w = window as unknown as { __raceArmed?: boolean };
      let seen = 0;
      const observer = new ResizeObserver(() => {
        seen++;
        if (seen < 2) return;
        observer.disconnect();
        const target = document.querySelector(`[data-message-id="${msgId}"]`) as HTMLElement;
        const filler = document.createElement("div");
        filler.style.height = "340px";
        filler.setAttribute("data-testid", "reflow-second");
        target.appendChild(filler);
      });
      observer.observe(content);
      w.__raceArmed = true;
    }, `${targetId}-msg-10`);
    await page.evaluate(
      () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
    );

    // First, small reflow — the one the tail-lock corrects programmatically.
    await page.locator(`[data-message-id="${targetId}-msg-10"]`).evaluate((el) => {
      const filler = document.createElement("div");
      filler.style.height = "21px";
      filler.setAttribute("data-testid", "reflow-first");
      el.appendChild(filler);
    });

    // The contaminated scroll event must not be read as the reader leaving
    // the tail: the reflow that followed it has to be corrected too.
    await expect.poll(distanceFromTail, { timeout: 5000 }).toBeLessThanOrEqual(2);

    // A much larger late reflow — in the DEV capture this was +1628px and was
    // left entirely uncorrected once the tail-lock had been disarmed.
    await page.locator(`[data-message-id="${targetId}-msg-12"]`).evaluate((el) => {
      const filler = document.createElement("div");
      filler.style.height = "1628px";
      filler.setAttribute("data-testid", "reflow-large");
      el.appendChild(filler);
    });

    await expect.poll(distanceFromTail, { timeout: 5000 }).toBeLessThanOrEqual(2);
    await expect(page.getByTestId("chat-bottom-sentinel")).toBeInViewport();
    await expect(page.getByRole("button", { name: /Ir para o final da conversa/ })).toBeHidden();
  });

  /**
   * #788's other half: a reader who deliberately left the tail is never
   * dragged back by a reflow. Without this the fix above would be a
   * regression — "a posição só deve deixar de ser mantida no final quando
   * houver ação real do usuário", and equally, once there is such an action
   * no layout shift may undo it.
   *
   * A real wheel gesture, not a scrollTop assignment: the distinction the fix
   * relies on must hold for the input path an actual reader uses.
   */
  test("does not pull a reader who scrolled up back to the tail when content reflows", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "scroll-reflow-manual");
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
      targetName: "Canal com leitura manual",
      messages,
    });
    await installMessagingMocks(page, scenario);
    await disableNativeScrollAnchoring(page);

    await page.goto(`/chat/channel/${targetId}`);

    const list = page.locator(".chat-msg-area__list");
    const scrollTop = () => list.evaluate((el) => Math.round(el.scrollTop));
    await expect(page.getByText("Mensagem 59")).toBeInViewport();

    await list.hover();
    await page.mouse.wheel(0, -100);
    await expect.poll(scrollTop).toBeLessThan(await list.evaluate((el) => el.scrollHeight));
    const afterWheel = await scrollTop();
    expect(afterWheel).toBeGreaterThan(0);

    // Content above grows well after the reader stopped: the viewport must
    // stay exactly where they left it.
    await page.locator(`[data-message-id="${targetId}-msg-10"]`).evaluate((el) => {
      const filler = document.createElement("div");
      filler.style.height = "800px";
      filler.setAttribute("data-testid", "reflow-filler");
      el.appendChild(filler);
    });
    await page.waitForTimeout(500);

    expect(await scrollTop()).toBe(afterWheel);
    await expect(page.getByTestId("chat-bottom-sentinel")).not.toBeInViewport();
  });
});
