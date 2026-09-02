import { expect, test, type Page } from "@playwright/test";

import {
  OTHER_USER_NAME,
  createScenario,
  installMessagingMocks,
  uniqueId,
  type MessagingScenario,
} from "../helpers/messagingApi";

/**
 * Issue #516: Ctrl+V / Cmd+V attaches what the clipboard carries.
 *
 * The component tests already cover the rules — limits, names, duplicates. What
 * only a real browser can show is that a genuine ClipboardEvent carrying real
 * bytes reaches the composer at all, past the editor's own paste handling, and
 * that a paste of plain text still lands in the message body as it always did.
 *
 * The clipboard is built in the page rather than taken from the operating
 * system: a DataTransfer the test owns is deterministic and needs no window
 * focus, which the real OS clipboard would on a CI machine.
 */

/** One opaque pixel, PNG. Small enough to inline, real enough to upload. */
const PNG_BASE64 =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";

const composerInput = (page: Page) => page.getByTestId("chat-composer-input");

/**
 * A paste on the composer's editor, exactly as the browser delivers one.
 *
 * `png` adds a file the browser would expose for a screenshot — under the
 * placeholder name a screenshot really arrives with — and `text` adds the plain
 * text of an ordinary copy.
 */
async function pasteIntoComposer(page: Page, clipboard: { png?: string; text?: string }) {
  await page.evaluate(
    ({ png, text }) => {
      const data = new DataTransfer();
      if (text !== undefined) data.setData("text/plain", text);
      if (png !== undefined) {
        const binary = atob(png);
        const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
        data.items.add(new File([bytes], "image.png", { type: "image/png" }));
      }
      const editor = document.querySelector('[data-testid="chat-composer-input"]');
      editor?.dispatchEvent(
        new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }),
      );
    },
    { png: clipboard.png, text: clipboard.text },
  );
}

async function openConversation(page: Page, scenario: MessagingScenario, path: string) {
  await installMessagingMocks(page, scenario);
  await page.goto(path);
  await expect(composerInput(page)).toBeVisible();
  await composerInput(page).click();
}

/** The name a screenshot is given for display and upload, e.g. Screenshot-2026-08-31-145410.png. */
const SCREENSHOT_NAME = /Screenshot-\d{4}-\d{2}-\d{2}-\d{6}\.png/;

test.describe("colar da área de transferência no composer", () => {
  test("anexa um screenshot colado em um canal e envia com legenda", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal E2E",
      messages: [],
    });
    await openConversation(page, scenario, `/chat/channel/${targetId}`);

    await pasteIntoComposer(page, { png: PNG_BASE64 });

    const pending = page.getByTestId("chat-composer-pending-attachment");
    await expect(pending).toBeVisible();
    await expect(pending).toContainText(SCREENSHOT_NAME);
    // The image is an attachment, never text the editor had to absorb.
    await expect(composerInput(page)).toHaveText("");
    expect(scenario.requests.attachmentUploads).toHaveLength(1);
    expect(scenario.requests.attachmentUploads[0]).toMatchObject({
      targetId,
      purpose: "message_draft",
    });
    expect(scenario.requests.attachmentUploads[0].filename).toMatch(
      new RegExp(`^${SCREENSHOT_NAME.source}$`),
    );

    // Typing continues where the caret already was: no second click.
    await page.keyboard.type("olha isto");
    await expect(composerInput(page)).toHaveText("olha isto");

    const posted = page.waitForResponse(
      (response) =>
        response.url().includes(`/api/chat/channels/${targetId}/messages`) &&
        response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Enviar mensagem" }).click();
    await posted;

    expect(scenario.requests.channelPosts).toEqual([
      expect.objectContaining({ body_text: "olha isto", attachment_ids: ["upload-1"] }),
    ]);
    // Sent means gone: the composer is empty again, attachment included.
    await expect(page.getByTestId("chat-composer-pending-attachment")).toHaveCount(0);
  });

  test("anexa um screenshot colado em uma conversa direta", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [],
    });
    await openConversation(page, scenario, `/chat/dm/${targetId}`);

    await pasteIntoComposer(page, { png: PNG_BASE64 });

    await expect(page.getByTestId("chat-composer-pending-attachment")).toBeVisible();
    expect(scenario.requests.attachmentUploads).toHaveLength(1);
    expect(scenario.requests.attachmentUploads[0].targetId).toBe(targetId);
  });

  test("anexa um screenshot colado em um grupo", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "group");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId,
      targetName: "Time de Infra E2E",
      messages: [],
    });
    await openConversation(page, scenario, `/chat/dm/${targetId}`);

    await pasteIntoComposer(page, { png: PNG_BASE64 });

    const pending = page.getByTestId("chat-composer-pending-attachment");
    await expect(pending).toBeVisible();
    await expect(pending).toContainText(SCREENSHOT_NAME);
    expect(scenario.requests.attachmentUploads).toHaveLength(1);
    expect(scenario.requests.attachmentUploads[0]).toMatchObject({
      targetId,
      purpose: "message_draft",
    });

    // The pasted screenshot composes the message like any other attachment.
    await page.keyboard.type("no grupo");
    const posted = page.waitForResponse(
      (response) =>
        response.url().includes(`/api/chat/dm/${targetId}/messages`) &&
        response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Enviar mensagem" }).click();
    await posted;

    expect(scenario.requests.dmPosts).toEqual([
      expect.objectContaining({ body_text: "no grupo", attachment_ids: ["upload-1"] }),
    ]);
  });

  test("mantém o paste de texto puro inteiramente nativo", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal E2E",
      messages: [],
    });
    await openConversation(page, scenario, `/chat/channel/${targetId}`);

    await pasteIntoComposer(page, { text: "texto colado" });

    await expect(composerInput(page)).toHaveText("texto colado");
    await expect(page.getByTestId("chat-composer-upload-status")).toHaveCount(0);
    expect(scenario.requests.attachmentUploads).toEqual([]);
  });
});
