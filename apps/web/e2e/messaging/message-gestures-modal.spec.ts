/**
 * Message gestures against native modality, in a real browser (issue #1152).
 *
 * Which modal is the active one is the browser's business: several elements
 * can match :modal at once (a fullscreen element, nested or sibling dialogs),
 * the top layer orders them by opening, not by the DOM, and only the topmost
 * modal dialog leaves anything outside it interactive. jsdom has none of
 * that, so these cases run the real recognizer — imported from the Vite dev
 * server into a bare page, under React StrictMode — with real dialogs,
 * showModal(), fullscreen and touches through the browser's touch pipeline.
 */

import { expect, test, type CDPSession, type Page } from "@playwright/test";

import { LONG_PRESS_MS, SWIPE_REPLY_PX } from "../../src/chat/useMessageTouchGestures";

const HARNESS = `<!doctype html>
<html lang="pt-BR">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <style>
      body { margin: 0; font: 16px sans-serif; }
      .row { box-sizing: border-box; height: 64px; padding: 20px 40px; background: #eee;
        touch-action: pan-y; user-select: none; }
      dialog { inset: auto; left: 0; margin: 0; padding: 0; width: 100%; border: 0; }
    </style>
  </head>
  <body>
    <script type="module">
      import RefreshRuntime from "/@react-refresh";
      RefreshRuntime.injectIntoGlobalHook(window);
      window.$RefreshReg$ = () => {};
      window.$RefreshSig$ = () => (type) => type;
      window.__vite_plugin_react_preamble_installed__ = true;

      const source = await (await fetch("/src/chat/useMessageTouchGestures.ts")).text();
      const reactUrl = source.match(/"(\\/node_modules\\/\\.vite\\/deps\\/react\\.js\\?v=\\w+)"/)[1];
      const version = new URL(reactUrl, location.href).searchParams.get("v");
      const React = (await import(reactUrl)).default;
      const client = await import("/node_modules/.vite/deps/react-dom_client.js?v=" + version);
      const { createRoot } = client.default ?? client;
      const { useMessageTouchGestures } = await import("/src/chat/useMessageTouchGestures.ts");

      window.events = [];
      function Row({ id }) {
        const handlers = useMessageTouchGestures({
          messageId: id,
          disabled: false,
          swipeBlocked: false,
          onLongPress: (messageId) => window.events.push("long:" + messageId),
          onSwipeReply: (messageId) => window.events.push("reply:" + messageId),
        });
        return React.createElement("div", { className: "row", "data-row": id, ...handlers }, "mensagem " + id);
      }
      window.mountRow = (id, parent) => {
        const host = document.createElement("div");
        parent.append(host);
        createRoot(host).render(
          React.createElement(React.StrictMode, null, React.createElement(Row, { id })),
        );
      };
      window.ready = true;
    </script>
  </body>
</html>`;

/** A finger, through the browser's touch pipeline. */
class Finger {
  private x = 0;
  private y = 0;

  private constructor(private readonly cdp: CDPSession) {}

  static async on(page: Page) {
    return new Finger(await page.context().newCDPSession(page));
  }

  async downOn(page: Page, rowId: string, atX = 100) {
    const box = await page.locator(`[data-row="${rowId}"]`).boundingBox();
    if (!box) throw new Error(`row ${rowId} has no box`);
    this.x = box.x + atX;
    this.y = box.y + box.height / 2;
    await this.send("touchStart", [{ x: this.x, y: this.y }]);
  }

  async swipe(dx: number) {
    for (let step = 1; step <= 6; step += 1) {
      await this.send("touchMove", [{ x: this.x + (dx * step) / 6, y: this.y }]);
    }
    this.x += dx;
  }

  up() {
    return this.send("touchEnd", []);
  }

  private send(type: string, touchPoints: { x: number; y: number }[]) {
    return this.cdp.send("Input.dispatchTouchEvent", { type, touchPoints });
  }
}

/**
 * Builds the scene from plain DOM: `rows` maps each row id to the id of the
 * element it goes in ("body" or an element created first by `elements`).
 */
async function scene(
  page: Page,
  elements: { id: string; tag: "dialog" | "div"; parent: string; top?: number }[],
  rows: Record<string, string>,
) {
  await page.clock.install({ time: new Date("2026-11-04T10:00:00Z") });
  await page.route("**/__gesture-harness", (route) =>
    route.fulfill({ contentType: "text/html", body: HARNESS }),
  );
  await page.goto("/__gesture-harness");
  await page.waitForFunction(() => (window as unknown as { ready?: boolean }).ready === true);
  await page.clock.pauseAt(new Date("2026-11-04T10:01:00Z"));
  await page.evaluate(
    ({ elements, rows }) => {
      const byId = (id: string) => (id === "body" ? document.body : document.getElementById(id)!);
      for (const { id, tag, parent, top } of elements) {
        const element = document.createElement(tag);
        element.id = id;
        if (top !== undefined) element.style.top = `${top}px`;
        byId(parent).append(element);
      }
      const mount = (window as unknown as { mountRow: (id: string, el: Element) => void }).mountRow;
      for (const [row, parent] of Object.entries(rows)) mount(row, byId(parent));
    },
    { elements, rows },
  );
  for (const row of Object.keys(rows))
    await page.locator(`[data-row="${row}"]`).waitFor({ state: "attached" });
  return Finger.on(page);
}

const events = (page: Page) =>
  page.evaluate(() => (window as unknown as { events: string[] }).events.slice());

function act(page: Page, script: string) {
  // Run as page script with a user gesture, as Playwright evaluates in Chromium.
  return page.evaluate(script);
}

const showModal = (page: Page, id: string) =>
  act(page, `document.getElementById(${JSON.stringify(id)}).showModal()`);
const close = (page: Page, id: string) =>
  act(page, `document.getElementById(${JSON.stringify(id)}).close()`);

/** Past the long-press time, on the page's own clock. */
const pastLongPress = (page: Page) => page.clock.runFor(LONG_PRESS_MS + 100);

test.use({ viewport: { width: 390, height: 740 }, hasTouch: true, isMobile: true });

test.describe("message gestures under native modality #1152", () => {
  test.beforeEach(({ browserName }) => {
    test.skip(browserName !== "chromium", "Touch goes through Chromium's CDP");
  });

  test("A — fullscreen then a modal dialog: the press behind the dialog never lands", async ({
    page,
  }) => {
    const finger = await scene(
      page,
      [
        { id: "fullscreen", tag: "div", parent: "body" },
        { id: "dialog", tag: "dialog", parent: "body", top: 400 },
      ],
      { behind: "fullscreen" },
    );
    await act(page, `document.getElementById("fullscreen").requestFullscreen()`);
    await expect.poll(() => act(page, "document.fullscreenElement?.id")).toBe("fullscreen");
    expect(await act(page, `document.querySelectorAll(":modal").length`)).toBe(1);

    await finger.downOn(page, "behind");
    await showModal(page, "dialog");
    // Both match :modal now; the dialog, opened last, is the active one.
    expect(await act(page, `document.querySelectorAll(":modal").length`)).toBe(2);
    await pastLongPress(page);
    await finger.up();

    await finger.downOn(page, "behind");
    await finger.up();
    await close(page, "dialog");
    await finger.downOn(page, "behind");
    await finger.swipe(SWIPE_REPLY_PX + 20);
    await showModal(page, "dialog");
    await finger.up();
    expect(await events(page)).toEqual([]);
  });

  test("A — inside the fullscreen element alone, gestures work", async ({ page }) => {
    const finger = await scene(page, [{ id: "fullscreen", tag: "div", parent: "body" }], {
      inside: "fullscreen",
    });
    await act(page, `document.getElementById("fullscreen").requestFullscreen()`);
    await expect.poll(() => act(page, "document.fullscreenElement?.id")).toBe("fullscreen");
    await finger.downOn(page, "inside");
    await pastLongPress(page);
    await finger.up();
    await finger.downOn(page, "inside");
    await finger.swipe(SWIPE_REPLY_PX + 20);
    await finger.up();
    expect(await events(page)).toEqual(["long:inside", "reply:inside"]);
  });

  for (const [first, second] of [
    ["a", "b"],
    ["b", "a"],
  ] as const) {
    test(`B — sibling dialogs opened ${first} then ${second}: only ${second}, on top, answers`, async ({
      page,
    }) => {
      const finger = await scene(
        page,
        [
          { id: "a", tag: "dialog", parent: "body", top: 40 },
          { id: "b", tag: "dialog", parent: "body", top: 400 },
        ],
        { "row-a": "a", "row-b": "b" },
      );
      await showModal(page, first);
      await finger.downOn(page, `row-${first}`);
      await showModal(page, second);
      await pastLongPress(page);
      await finger.up();
      expect(await events(page)).toEqual([]);

      await finger.downOn(page, `row-${second}`);
      await pastLongPress(page);
      await finger.up();
      expect(await events(page)).toEqual([`long:row-${second}`]);
    });
  }

  test("C/D/E — nested dialogs: the outer is blocked under the inner, revives nothing on close", async ({
    page,
  }) => {
    const finger = await scene(
      page,
      [
        { id: "outer", tag: "dialog", parent: "body", top: 40 },
        { id: "inner", tag: "dialog", parent: "outer", top: 400 },
      ],
      { "row-outer": "outer", "row-inner": "inner" },
    );
    await showModal(page, "outer");
    await finger.downOn(page, "row-outer");
    await showModal(page, "inner");
    await pastLongPress(page);
    await finger.up();
    expect(await events(page)).toEqual([]);

    // D: a row inside the active dialog is not blocked.
    await finger.downOn(page, "row-inner");
    await pastLongPress(page);
    await finger.up();
    expect(await events(page)).toEqual(["long:row-inner"]);

    // E: a swipe on the outer row started before the inner opened does not
    // come back when it closes; the outer, active again, answers anew.
    await close(page, "inner");
    await finger.downOn(page, "row-outer");
    await finger.swipe(SWIPE_REPLY_PX + 20);
    await showModal(page, "inner");
    await close(page, "inner");
    await finger.up();
    await pastLongPress(page);
    expect(await events(page)).toEqual(["long:row-inner"]);
    await finger.downOn(page, "row-outer");
    await pastLongPress(page);
    await finger.up();
    expect(await events(page)).toEqual(["long:row-inner", "long:row-outer"]);
  });

  test("F — a dialog opened with show() is not modal and blocks nothing", async ({ page }) => {
    const finger = await scene(page, [{ id: "plain", tag: "dialog", parent: "body", top: 400 }], {
      row: "body",
    });
    await act(page, `document.getElementById("plain").show()`);
    expect(await act(page, `document.querySelectorAll(":modal").length`)).toBe(0);
    await finger.downOn(page, "row");
    await pastLongPress(page);
    await finger.up();
    await finger.downOn(page, "row");
    await finger.swipe(SWIPE_REPLY_PX + 20);
    await finger.up();
    expect(await events(page)).toEqual(["long:row", "reply:row"]);
  });

  test("G — a swipe in progress when a modal opens loses its feedback and never replies", async ({
    page,
  }) => {
    const finger = await scene(page, [{ id: "dialog", tag: "dialog", parent: "body", top: 400 }], {
      row: "body",
    });
    const row = page.locator('[data-row="row"]');
    await finger.downOn(page, "row");
    await finger.swipe(SWIPE_REPLY_PX + 20);
    await expect(row).toHaveAttribute("data-swipe-armed", "");
    await showModal(page, "dialog");
    await expect(row).not.toHaveAttribute("data-swiping");
    await expect(row).not.toHaveAttribute("data-swipe-armed");
    expect(await row.evaluate((element) => (element as HTMLElement).style.transform)).toBe("");
    await finger.up();
    expect(await events(page)).toEqual([]);
  });

  test("H — a long press pending when a modal opens never fires, then works once it closes", async ({
    page,
  }) => {
    const finger = await scene(page, [{ id: "dialog", tag: "dialog", parent: "body", top: 400 }], {
      row: "body",
    });
    await finger.downOn(page, "row");
    await showModal(page, "dialog");
    await pastLongPress(page);
    await finger.up();
    await close(page, "dialog");
    await pastLongPress(page);
    expect(await events(page)).toEqual([]);

    await finger.downOn(page, "row");
    await pastLongPress(page);
    await finger.up();
    expect(await events(page)).toEqual(["long:row"]);
  });

  test("a backdrop that lets pointers through still blocks: hit-testing skips inert rows", async ({
    page,
  }) => {
    const finger = await scene(page, [{ id: "dialog", tag: "dialog", parent: "body", top: 400 }], {
      row: "body",
    });
    await page.addStyleTag({ content: "dialog::backdrop { pointer-events: none; }" });
    await finger.downOn(page, "row");
    await showModal(page, "dialog");
    await pastLongPress(page);
    await finger.up();
    expect(await events(page)).toEqual([]);
  });

  test("a swipe is revalidated where the finger lifts, not where it last moved", async ({
    page,
  }) => {
    // CDP's touchEnd carries no position (the browser lifts the finger where it
    // last moved), so the release that differs from the last move — what a
    // device reports when its final movement was never dispatched — is the
    // one synthesised event here. Down and moves are native: real Pointer
    // Events with the row's implicit capture.
    const finger = await scene(page, [{ id: "dialog", tag: "dialog", parent: "body", top: 300 }], {
      row: "dialog",
    });
    await page.addStyleTag({ content: ".row { width: 120px; }" });
    await showModal(page, "dialog");
    await act(
      page,
      `document.addEventListener("pointerdown", (e) => { window.pointer = e.pointerId; }, true)`,
    );
    const box = (await page.locator('[data-row="row"]').boundingBox())!;
    // The row follows the finger only so far: past it the finger runs off its
    // right edge onto the dialog, and comes back on it at the release.
    await finger.downOn(page, "row", 110);
    await finger.swipe(110);
    const lastMove = { x: box.x + 220, y: box.y + box.height / 2 };
    const onRow = (point: { x: number; y: number }) =>
      page.evaluate(
        ({ x, y }) =>
          document.querySelector('[data-row="row"]')!.contains(document.elementFromPoint(x, y)),
        point,
      );
    const release = { x: box.x + 212, y: box.y + box.height / 2 };
    // Moves reach the page aligned to frames: wait until the last one is
    // painted — the row as far right as it follows a finger (96 px) — before
    // judging where the release lands.
    const row = page.locator('[data-row="row"]');
    await expect
      .poll(() => row.evaluate((element) => (element as HTMLElement).style.transform))
      .toBe("translateX(96px)");
    expect(await onRow(lastMove)).toBe(false);
    expect(await onRow(release)).toBe(true);
    await page.evaluate(({ x, y }) => {
      const row = document.querySelector('[data-row="row"]')!;
      const pointerId = (window as unknown as { pointer: number }).pointer;
      const init = { bubbles: true, pointerId, pointerType: "touch", isPrimary: true };
      if (!row.contains(document.elementFromPoint(x, y))) throw new Error("release is off the row");
      row.dispatchEvent(new PointerEvent("pointerup", { ...init, clientX: x, clientY: y }));
    }, release);
    await finger.up();
    expect(await events(page)).toEqual(["reply:row"]);
  });
});
