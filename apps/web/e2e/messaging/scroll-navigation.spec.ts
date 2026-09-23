import { expect, test, type Page } from "@playwright/test";

import { installPaginatedMessages } from "../helpers/largeConversationFixture";
import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  emitMessageCreated,
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
/**
 * A mensagem montada mais acima na janela.
 *
 * Desde a #675 a timeline virtualiza a partir de VIRTUALIZE_MIN_ROWS: uma
 * mensagem distante simplesmente não existe no DOM, e um seletor por índice
 * fixo não tem onde injetar o filler. O que a #788 descreve — conteúdo *acima
 * do leitor* crescendo tarde — é exatamente esta linha, e passou a ser a única
 * forma desse crescimento tardio ainda acontecer.
 */
function mountedMessage(page: Page, position: "first" | "second") {
  const all = page.locator(".chat-msg-area__list [data-message-id]");
  return position === "first" ? all.first() : all.nth(1);
}

/**
 * Espera a timeline parar de se mexer.
 *
 * Duas leituras iguais de (scrollTop, scrollHeight), e não um tempo. As duas
 * juntas porque cada uma sozinha mente: uma rolagem animada ainda em curso
 * mantém o scrollTop mudando com a altura parada, e uma linha ainda por medir
 * muda a altura com o scrollTop parado.
 *
 * A rolagem que a roda do mouse produz no Chromium é animada, e dura vários
 * quadros depois de o evento ser entregue. Medir a posição de leitura no meio
 * dela registra uma coordenada que a própria animação vai desmentir logo em
 * seguida — foi exatamente o que produziu uma "deriva" de 147px numa timeline
 * que não tinha movido o leitor um pixel sequer.
 */
async function settledTimeline(page: Page) {
  let previous = "";
  await expect
    .poll(
      async () => {
        const now = await page
          .locator(".chat-msg-area__list")
          .evaluate((el) => `${Math.round(el.scrollTop)}/${el.scrollHeight}`);
        const settled = now === previous;
        previous = now;
        return settled;
      },
      { timeout: 10_000, intervals: [100] },
    )
    .toBe(true);
}

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
    await mountedMessage(page, "first").evaluate((el) => {
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
    await page.evaluate(() => {
      const content = document.querySelector(".chat-msg-area__list-content") as HTMLElement;
      const w = window as unknown as { __raceArmed?: boolean };
      let seen = 0;
      const observer = new ResizeObserver(() => {
        seen++;
        if (seen < 2) return;
        observer.disconnect();
        const target = document.querySelector(
          ".chat-msg-area__list [data-message-id]",
        ) as HTMLElement;
        const filler = document.createElement("div");
        filler.style.height = "340px";
        filler.setAttribute("data-testid", "reflow-second");
        target.appendChild(filler);
      });
      observer.observe(content);
      w.__raceArmed = true;
    });
    await page.evaluate(
      () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
    );

    // First, small reflow — the one the tail-lock corrects programmatically.
    await mountedMessage(page, "first").evaluate((el) => {
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
    await mountedMessage(page, "second").evaluate((el) => {
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
    // A roda do Chromium rola de forma animada: sem esperar o fim dela, tudo o
    // que for medido a seguir descreve um quadro intermediário.
    await settledTimeline(page);
    const afterWheel = await scrollTop();
    expect(afterWheel).toBeGreaterThan(0);

    // Content above grows well after the reader stopped: what they are looking
    // at must stay exactly where they left it.
    //
    // Medido pelo conteúdo, não pelo scrollTop (#675): numa lista onde tudo
    // está montado, manter o offset e manter o conteúdo são a mesma coisa. Com
    // virtualização não são — quando uma linha acima da dobra cresce, o
    // virtualizador soma esse crescimento ao offset justamente para que o
    // conteúdo não desça. Afirmar o offset velho passaria a afirmar o oposto do
    // que a #788 protege.
    const readingBefore = await page.evaluate(() => {
      const list = document.querySelector(".chat-msg-area__list") as HTMLElement;
      const top = list.getBoundingClientRect().top;
      const first = [...list.querySelectorAll<HTMLElement>("[data-message-id]")].find(
        (element) => element.getBoundingClientRect().bottom > top,
      );
      return first
        ? {
            id: first.dataset.messageId,
            offset: Math.round(first.getBoundingClientRect().top - top),
          }
        : null;
    });
    expect(readingBefore).not.toBeNull();

    const heightBeforeReflow = await list.evaluate((el) => el.scrollHeight);
    await mountedMessage(page, "first").evaluate((el) => {
      const filler = document.createElement("div");
      filler.style.height = "800px";
      filler.setAttribute("data-testid", "reflow-filler");
      el.appendChild(filler);
    });
    // O crescimento foi medido — a linha foi remedida, e não apenas o nó
    // inserido no DOM — e depois a timeline voltou a ficar parada. Duas
    // condições observáveis, nenhuma delas um tempo de espera, e nenhuma delas
    // um número: "cresceu" e "parou de mudar" bastam. O quanto cresceu é
    // assunto do layout — margens fazem a linha ganhar um pouco menos que a
    // altura do preenchimento —, e fixar esse valor aqui só criaria um segundo
    // motivo de falha que não tem nada a ver com a posição de leitura.
    await expect
      .poll(() => list.evaluate((el) => el.scrollHeight), { timeout: 10_000 })
      .toBeGreaterThan(heightBeforeReflow);
    await settledTimeline(page);

    const readingAfter = await page.evaluate((id: string) => {
      const list = document.querySelector(".chat-msg-area__list") as HTMLElement;
      const top = list.getBoundingClientRect().top;
      const target = list.querySelector<HTMLElement>(`[data-message-id="${id}"]`);
      return target ? Math.round(target.getBoundingClientRect().top - top) : null;
    }, readingBefore!.id!);

    expect(readingAfter).not.toBeNull();
    expect(Math.abs(readingAfter! - readingBefore!.offset)).toBeLessThanOrEqual(2);

    // E continuam fora do tail — não foram puxados de volta. Medido pela
    // distância até o fim, e não pela visibilidade do sentinel: com a
    // virtualização compensando o crescimento acima da dobra, a distância até o
    // fim é a mesma que o leitor escolheu com a roda, e essa distância é menor
    // que uma viewport — o sentinel fica à vista sem que ninguém tenha sido
    // arrastado para lá.
    const distanceFromTail = await list.evaluate((el) =>
      Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
    );
    expect(distanceFromTail).toBeGreaterThan(0);
  });
});

/**
 * #880 — a programmatic navigation that keeps its destination.
 *
 * The defect these reproduce was captured in DEV with every scrollTop write
 * attributed to its caller. The sequence, on a conversation with a prepended
 * page and a row growing mid-trip:
 *
 *   t+36   click ↓          → one scrollIntoView at the sentinel
 *   t+64   a row reflows    → the sentinel is positioned by the virtualizer's
 *                             canvas, whose height trails that measurement, so
 *                             it reports "intersecting" with 214px still below
 *                             the fold — and the operation ends there
 *   t+81   the tail-lock pins to the (now taller) end
 *   t+108  the prepend restoration, re-armed by that very phase change, puts a
 *          stale anchor back: 238px short of the tail
 *   ...    nothing resizes again, so nothing corrects it: the reader is parked
 *          mid-conversation with the control hidden, claiming they arrived
 *
 * Both halves are asserted numerically here — the distance to the end, the
 * sentinel, and the control — because "it looks like it got there" is exactly
 * what the previous version also looked like.
 */
test.describe("chat tail navigation under reflow (#880)", () => {
  /** remainingPx = scrollHeight - scrollTop - clientHeight, as the issue defines it. */
  const remainingPx = (page: Page) =>
    page
      .locator(".chat-msg-area__list")
      .evaluate((el) => Math.round(el.scrollHeight - el.scrollTop - el.clientHeight));

  /**
   * Grows a mounted row on each of the next `count` scroll events.
   *
   * Reflows *during* the navigation, which is the case at issue, and driven by
   * the navigation's own movement rather than by a timer — no wait, no sleep,
   * and no assumption about how long the trip takes.
   */
  async function growRowsWhileNavigating(page: Page, count: number, px: number) {
    await page.evaluate(
      ([count, px]) => {
        const list = document.querySelector(".chat-msg-area__list") as HTMLElement;
        let done = 0;
        const onScroll = () => {
          if (done >= count) {
            list.removeEventListener("scroll", onScroll);
            return;
          }
          const rows = [...list.querySelectorAll<HTMLElement>("[data-message-id]")];
          const target = rows[rows.length - 1 - done];
          if (!target) return;
          done += 1;
          const filler = document.createElement("div");
          filler.style.height = `${px}px`;
          filler.setAttribute("data-testid", `reflow-${done}`);
          target.appendChild(filler);
        };
        list.addEventListener("scroll", onScroll);
      },
      [count, px],
    );
  }

  function longHistory(targetId: string, count: number) {
    return Array.from({ length: count }, (_, i) =>
      makeMessage({
        id: `${targetId}-msg-${i}`,
        sender_id: i % 3 === 0 ? OTHER_USER_ID : CURRENT_USER_ID,
        sender_display_name: i % 3 === 0 ? OTHER_USER_NAME : undefined,
        // Deliberately uneven: every row the same height would let a single
        // estimate stand in for the whole conversation, and the retargeting
        // under test is about estimates turning into real measurements.
        body_text: `Mensagem ${i} ` + "texto ".repeat((i * 37) % 90),
        created_at: new Date(Date.UTC(2026, 6, 15, 8, 0, 0) + i * 61_000).toISOString(),
      }),
    );
  }

  /** The end really reached: the geometry, the sentinel and the control agree. */
  async function expectAtTheRealTail(page: Page) {
    await expect.poll(() => remainingPx(page), { timeout: 10_000 }).toBeLessThanOrEqual(2);
    await expect(page.getByTestId("chat-bottom-sentinel")).toBeInViewport();
    await expect(page.getByRole("button", { name: /Ir para o final|Começar pelas/ })).toBeHidden();
  }

  test("reaches the tail through three consecutive reflows during the navigation", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "tail-reflows");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal longo",
      messages: longHistory(targetId, 300),
    });
    await installMessagingMocks(page, scenario);
    await disableNativeScrollAnchoring(page);

    await page.goto(`/chat/channel/${targetId}`);
    await expect(page.getByTestId("chat-bottom-sentinel")).toBeInViewport();

    const list = page.locator(".chat-msg-area__list");
    await list.evaluate((el) => {
      el.scrollTop = 0;
    });
    const button = page.getByRole("button", { name: /Ir para o final da conversa/ });
    await expect(button).toBeVisible();

    await growRowsWhileNavigating(page, 3, 400);
    await button.click();

    await expectAtTheRealTail(page);
    // The growth really happened, so the assertions above are about a timeline
    // that moved underneath the navigation rather than about a static one.
    await expect(page.getByTestId("reflow-3")).toHaveCount(1);
  });

  test("is not undone by the restoration a prepended page left behind", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "tail-after-prepend");
    const all = longHistory(targetId, 200);
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal paginado",
      messages: all,
    });
    await installMessagingMocks(page, scenario);
    const messages = await installPaginatedMessages(page, targetId, all);
    await disableNativeScrollAnchoring(page);

    await page.goto(`/chat/channel/${targetId}`);
    await expect(page.getByTestId("chat-bottom-sentinel")).toBeInViewport();

    const list = page.locator(".chat-msg-area__list");
    // Two pages of history, so "prepend" is the last mutation this timeline
    // saw and the position it would restore is screens away — the state the
    // capture above was taken in.
    for (let page_ = 0; page_ < 2; page_ += 1) {
      await list.evaluate((el) => {
        el.scrollTop = 0;
      });
      await expect.poll(() => messages.pagesInFlight()).toBe(1);
      await expect.poll(() => messages.pagesInFlight(), { timeout: 20_000 }).toBe(0);
      await settledTimeline(page);
    }

    // Walk down through the whole tail region first, so every row there is
    // already measured: without this the strand is hidden by an incidental
    // later remeasure, which is exactly how it survived until now.
    for (let top = 3_000; top < 20_000; top += 400) {
      await list.evaluate((el, value) => {
        el.scrollTop = value;
      }, top);
      await page.evaluate(
        () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))),
      );
    }
    await settledTimeline(page);
    await list.evaluate((el) => {
      el.scrollTop = 3_000;
    });
    // Quiet before the trip, as two equal readings rather than as a wait: with
    // a remeasure still in flight the strand is repaired by accident, which is
    // how this defect survived a suite that already covered reflow.
    await settledTimeline(page);

    const button = page.getByRole("button", { name: /Ir para o final da conversa/ });
    await expect(button).toBeVisible();
    await growRowsWhileNavigating(page, 1, 400);
    await button.click();

    await expectAtTheRealTail(page);
  });
});

/**
 * #880 — the one floating control, and what it means at each moment.
 *
 * `↓ N` takes the reader to the boundary the unread messages start at, and
 * only the *next* press takes them to the end. Jumping straight to the end
 * would skip exactly what they came back for.
 *
 * Run over a channel, a group and a direct conversation, from one body: the
 * navigation is shared, and a branch per kind is what this must not become.
 */
const CONTEXTUAL_BUTTON_KINDS = [
  { label: "canal", kind: "channel" as const, path: "channel" },
  { label: "grupo", kind: "dm" as const, path: "dm", conversationType: "group" as const },
  {
    label: "conversa direta",
    kind: "dm" as const,
    path: "dm",
    conversationType: "direct" as const,
  },
];

test.describe("the contextual scroll control (#880)", () => {
  for (const target of CONTEXTUAL_BUTTON_KINDS) {
    test(`leads to the unread boundary first and to the end afterwards — ${target.label}`, async ({
      page,
    }, testInfo) => {
      const targetId = uniqueId(testInfo, `unread-${target.path}-${target.conversationType ?? ""}`);
      const read = Array.from({ length: 40 }, (_, i) =>
        makeMessage({
          id: `${targetId}-read-${i}`,
          sender_id: OTHER_USER_ID,
          sender_display_name: OTHER_USER_NAME,
          body_text: `Mensagem lida ${i}`,
          created_at: new Date(Date.UTC(2026, 6, 15, 9, 0, 0) + i * 60_000).toISOString(),
        }),
      );
      const unread = Array.from({ length: 30 }, (_, i) =>
        makeMessage({
          id: `${targetId}-unread-${i}`,
          sender_id: OTHER_USER_ID,
          sender_display_name: OTHER_USER_NAME,
          body_text: `Mensagem não lida ${i}`,
          created_at: new Date(Date.UTC(2026, 6, 15, 11, 0, 0) + i * 60_000).toISOString(),
        }),
      );
      const scenario = createScenario({
        kind: target.kind,
        targetId,
        targetName: "Conversa com não lidas",
        conversationType: target.conversationType,
        messages: [...read, ...unread],
      });
      const sidebar =
        target.kind === "channel"
          ? scenario.sidebarChannels.find((channel) => channel.id === targetId)
          : scenario.sidebarDMs.find((dm) => dm.id === targetId);
      sidebar!.unread_count = unread.length;
      await installMessagingMocks(page, scenario);
      await disableNativeScrollAnchoring(page);

      await page.goto(`/chat/${target.path}/${targetId}`);

      // Opens on the boundary, with read context above it: the separator is on
      // screen and so is at least one message the reader had already seen.
      const separator = page.getByRole("separator", { name: "Novas mensagens" });
      await expect(separator).toBeInViewport();
      await expect(page.getByText("Mensagem lida 39")).toBeInViewport();
      const list = page.locator(".chat-msg-area__list");
      const separatorOffset = () =>
        page.evaluate(() => {
          const el = document.querySelector(".chat-msg-area__list") as HTMLElement;
          const row = document.querySelector('[role="separator"]') as HTMLElement;
          return Math.round(row.getBoundingClientRect().top - el.getBoundingClientRect().top);
        });
      expect(await separatorOffset()).toBeGreaterThan(0);

      // Up into the history: the boundary is below the fold again, and the
      // control goes back to offering it.
      await list.evaluate((el) => {
        el.scrollTop = 0;
      });
      await settledTimeline(page);
      const toBoundary = page.getByRole("button", { name: "Começar pelas 30 novas mensagens" });
      await expect(toBoundary).toBeVisible();

      // A message arrives while they read: the viewport must not move, and the
      // count is unread — not "messages below the fold".
      await emitMessageCreated(page, scenario, {
        kind: target.kind,
        targetId,
        message: makeMessage({
          id: `${targetId}-live-1`,
          sender_id: OTHER_USER_ID,
          sender_display_name: OTHER_USER_NAME,
          body_text: "Chegou agora",
          created_at: "2026-07-15T12:00:00.000Z",
        }),
      });
      await expect(
        page.getByRole("button", { name: "Começar pelas 31 novas mensagens" }),
      ).toBeVisible();
      expect(await list.evaluate((el) => Math.round(el.scrollTop))).toBe(0);

      // The first press lands on the boundary, not on the end.
      await page.getByRole("button", { name: "Começar pelas 31 novas mensagens" }).click();
      await expect(separator).toBeInViewport();
      await expect
        .poll(() => list.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight))
        .toBeGreaterThan(2);
      expect(await separatorOffset()).toBeGreaterThan(0);

      // And from there the same control means the end.
      const toTail = page.getByRole("button", { name: /^Ir para o final da conversa/ });
      await expect(toTail).toBeVisible();
      await toTail.click();
      await expect
        .poll(() =>
          list.evaluate((el) => Math.round(el.scrollHeight - el.scrollTop - el.clientHeight)),
        )
        .toBeLessThanOrEqual(2);
      await expect(page.getByTestId("chat-bottom-sentinel")).toBeInViewport();
      await expect(
        page.getByRole("button", { name: /Ir para o final|Começar pelas/ }),
      ).toBeHidden();
    });
  }
});

/**
 * #880 item 19 — the same control on a phone-sized viewport.
 *
 * The contextual offset is proportional to the viewport, so a short screen
 * must not spend it on already-read history; and the control has to stay above
 * the composer, where a thumb can reach it and the keyboard does not cover it.
 */
test.describe("the contextual scroll control on a small viewport (#880)", () => {
  test("keeps the boundary reachable and the control above the composer", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 760 });
    const targetId = uniqueId(testInfo, "unread-mobile");
    const messages = [
      ...Array.from({ length: 30 }, (_, i) =>
        makeMessage({
          id: `${targetId}-read-${i}`,
          sender_id: OTHER_USER_ID,
          sender_display_name: OTHER_USER_NAME,
          body_text: `Mensagem lida ${i}`,
          created_at: new Date(Date.UTC(2026, 6, 15, 9, 0, 0) + i * 60_000).toISOString(),
        }),
      ),
      ...Array.from({ length: 20 }, (_, i) =>
        makeMessage({
          id: `${targetId}-unread-${i}`,
          sender_id: OTHER_USER_ID,
          sender_display_name: OTHER_USER_NAME,
          body_text: `Mensagem não lida ${i}`,
          created_at: new Date(Date.UTC(2026, 6, 15, 11, 0, 0) + i * 60_000).toISOString(),
        }),
      ),
    ];
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal móvel",
      messages,
    });
    scenario.sidebarChannels.find((channel) => channel.id === targetId)!.unread_count = 20;
    await installMessagingMocks(page, scenario);
    await disableNativeScrollAnchoring(page);

    await page.goto(`/chat/channel/${targetId}`);

    const separator = page.getByRole("separator", { name: "Novas mensagens" });
    await expect(separator).toBeInViewport();
    // Context above the boundary, and not half the screen of it: on a short
    // viewport the proportional offset is clamped, never a fixed desktop value.
    const offset = await page.evaluate(() => {
      const list = document.querySelector(".chat-msg-area__list") as HTMLElement;
      const row = document.querySelector('[role="separator"]') as HTMLElement;
      return Math.round(row.getBoundingClientRect().top - list.getBoundingClientRect().top);
    });
    const viewportPx = await page.locator(".chat-msg-area__list").evaluate((el) => el.clientHeight);
    expect(offset).toBeGreaterThan(0);
    expect(offset).toBeLessThan(viewportPx / 2);

    const list = page.locator(".chat-msg-area__list");
    await list.evaluate((el) => {
      el.scrollTop = 0;
    });
    await settledTimeline(page);
    const control = page.getByRole("button", { name: "Começar pelas 20 novas mensagens" });
    await expect(control).toBeVisible();

    // Above the composer, and a target a thumb can hit.
    const controlBox = (await control.boundingBox())!;
    const composerBox = (await page.getByTestId("chat-composer-input").boundingBox())!;
    expect(controlBox.y + controlBox.height).toBeLessThanOrEqual(composerBox.y);
    expect(Math.min(controlBox.width, controlBox.height)).toBeGreaterThanOrEqual(36);

    // Operable from the keyboard, and it never steals focus by appearing.
    expect(await page.evaluate(() => document.activeElement?.tagName)).not.toBe("BUTTON");
    await control.focus();
    await page.keyboard.press("Enter");
    await expect(separator).toBeInViewport();

    await page.getByRole("button", { name: /^Ir para o final da conversa/ }).press("Space");
    await expect
      .poll(() =>
        list.evaluate((el) => Math.round(el.scrollHeight - el.scrollTop - el.clientHeight)),
      )
      .toBeLessThanOrEqual(2);
    await expect(page.getByTestId("chat-bottom-sentinel")).toBeInViewport();
  });
});

/**
 * #880 — a scrollbar drag in a real browser.
 *
 * Which event a drag produces depends on how the browser draws its scrollbars:
 * a classic one is part of the page and reports a pointer past the content
 * box; an overlay one (what this Chromium draws, and what the assertion below
 * records) is browser chrome, and the page sees only the scroll it caused. The
 * viewport recognises both, and this test is here to keep the observable half
 * honest — the reader keeps the position they dragged to, whatever the engine
 * chose to tell the page.
 */
test.describe("a scrollbar drag during a programmatic navigation (#880)", () => {
  // Both sides of the distance rule (#675): the long trip is instant by that
  // rule, and the short one is instant because this browser's overlay
  // scrollbar would leave an animation impossible to interrupt.
  for (const trip of [
    { label: "de longe", startAtTopPx: 0, draggedTo: [9_000, 5_500, 4_000] },
    { label: "de perto", startAtTopPx: -1_200, draggedTo: [1_400, 900, 600] },
  ]) {
    test(`ends the trip to the end and leaves the reader where they dragged to — ${trip.label}`, async ({
      page,
    }, testInfo) => {
      const targetId = uniqueId(testInfo, `scrollbar-drag-${trip.label.replace(" ", "-")}`);
      const scenario = createScenario({
        kind: "channel",
        targetId,
        targetName: "Canal com scrollbar",
        messages: Array.from({ length: 200 }, (_, i) =>
          makeMessage({
            id: `${targetId}-msg-${i}`,
            sender_id: i % 3 === 0 ? OTHER_USER_ID : CURRENT_USER_ID,
            sender_display_name: i % 3 === 0 ? OTHER_USER_NAME : undefined,
            body_text: `Mensagem ${i} ` + "texto ".repeat((i * 37) % 90),
            created_at: new Date(Date.UTC(2026, 6, 15, 8, 0, 0) + i * 61_000).toISOString(),
          }),
        ),
      });
      await installMessagingMocks(page, scenario);
      await disableNativeScrollAnchoring(page);

      await page.goto(`/chat/channel/${targetId}`);
      await expect(page.getByTestId("chat-bottom-sentinel")).toBeInViewport();

      const list = page.locator(".chat-msg-area__list");
      await list.evaluate((el, startAtTopPx) => {
        el.scrollTop =
          startAtTopPx >= 0 ? startAtTopPx : el.scrollHeight - el.clientHeight + startAtTopPx;
      }, trip.startAtTopPx);
      await settledTimeline(page);

      // A trip to the end, and the reader taking the scrollbar mid-way: the
      // pointer goes down on the bar (classic scrollbars) and the drag scrolls
      // (every kind), which is what an overlay scrollbar leaves behind.
      const box = (await list.boundingBox())!;
      await page.getByRole("button", { name: /Ir para o final da conversa/ }).click();
      await page.mouse.move(box.x + box.width - 3, box.y + box.height / 2);
      await page.mouse.down();
      // A drag is a stream of scroll events, not one: the viewport reads a
      // reader's position from an event whose layout did not move under it
      // (#788), and a single event could land on a measurement.
      await list.evaluate(async (el, tops) => {
        const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));
        for (const top of tops) {
          el.scrollTop = top;
          await frame();
        }
      }, trip.draggedTo);
      await page.mouse.move(box.x + box.width - 3, box.y + box.height / 4, { steps: 6 });
      await page.mouse.up();
      await settledTimeline(page);

      // Far from the end, which is the invariant — not a pixel, since a row
      // above the fold being measured legitimately moves the offset to keep
      // the content still (#675).
      const remainingAfterDrag = await list.evaluate((el) =>
        Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
      );
      expect(remainingAfterDrag).toBeGreaterThan(1_000);
      await expect(page.getByRole("button", { name: /Ir para o final da conversa/ })).toBeVisible();

      // A row above the fold grows afterwards: the navigation is over, so
      // nothing retargets the end, and what the reader is looking at stays put.
      await mountedMessage(page, "first").evaluate((el) => {
        const filler = document.createElement("div");
        filler.style.height = "300px";
        filler.setAttribute("data-testid", "post-drag-reflow");
        el.appendChild(filler);
      });
      await expect(page.getByTestId("post-drag-reflow")).toHaveCount(1);
      await settledTimeline(page);

      await expect
        .poll(() =>
          list.evaluate((el) => Math.round(el.scrollHeight - el.scrollTop - el.clientHeight)),
        )
        .toBeGreaterThan(2);
      await expect(page.getByRole("button", { name: /Ir para o final da conversa/ })).toBeVisible();
    });
  }
});
