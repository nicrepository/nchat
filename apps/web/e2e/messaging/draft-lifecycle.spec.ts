import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  GROUP_DM_NAME,
  OTHER_CHANNEL_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  expectComposerConsumedTheSend,
  fillComposer,
  installMessagingMocks,
  makeMessage,
  messageBubble,
  messagesFor,
  revealActions,
  uniqueId,
  type MessagingScenario,
} from "../helpers/messagingApi";

/**
 * Issue #929 — NAVIGATION PRESERVES DRAFT. CONFIRMED SEND CONSUMES ITS SNAPSHOT.
 *
 * One flow, three surfaces: a DM, a channel and a group all run the same
 * steps through the same helpers, because the draft store, the composer and
 * the acknowledgement are the same code for all three — a surface-specific
 * branch anywhere would show up here as a surface-specific failure.
 *
 * Navigation is always the sidebar, never `page.goto`: a full page load is
 * an F5, which deliberately restores only text and reply. The point of these
 * specs is the in-app switch, where the whole draft must come back.
 */

type Surface = {
  kind: "dm" | "channel";
  conversationType?: "group";
  /** The sidebar row of the conversation under test. */
  row: (page: Page, targetName: string) => Locator;
  /** The row of the conversation the reader switches to and back from. */
  otherRow: (page: Page) => Locator;
  postUrl: (targetId: string) => string;
  posts: (scenario: MessagingScenario) => Array<{ body_text?: string; parent_message_id?: string }>;
};

const surfaces: Record<"dm" | "canal" | "grupo", Surface> = {
  dm: {
    kind: "dm",
    row: (page, name) => page.getByRole("option", { name: `Mensagem direta com ${name}` }),
    otherRow: (page) => page.getByRole("option", { name: new RegExp(OTHER_CHANNEL_NAME) }),
    postUrl: (id) => `/api/chat/dm/${id}/messages`,
    posts: (scenario) => scenario.requests.dmPosts,
  },
  canal: {
    kind: "channel",
    row: (page, name) => page.getByRole("option", { name: `Canal ${name}` }),
    otherRow: (page) => page.getByRole("option", { name: new RegExp(GROUP_DM_NAME) }),
    postUrl: (id) => `/api/chat/channels/${id}/messages`,
    posts: (scenario) => scenario.requests.channelPosts,
  },
  grupo: {
    kind: "dm",
    conversationType: "group",
    row: (page, name) => page.getByRole("option", { name: `Grupo ${name}` }),
    otherRow: (page) => page.getByRole("option", { name: new RegExp(OTHER_CHANNEL_NAME) }),
    postUrl: (id) => `/api/chat/dm/${id}/messages`,
    posts: (scenario) => scenario.requests.dmPosts,
  },
};

/**
 * A response held open until the test releases it. `release` is idempotent
 * and carries no assertion of its own: it is cleanup, and must never
 * replace the failure that sent the test into its `finally`.
 */
function heldResponse() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => (release = resolve));
  return { promise, release };
}

const composerInput = (page: Page) => page.getByTestId("chat-composer-input");
const composerQuote = (page: Page) => page.getByTestId("chat-composer-quote");
const pendingAttachment = (page: Page) => page.getByTestId("chat-composer-pending-attachment");
const draftBadge = (row: Locator) => row.getByTestId("chat-sidebar-draft-badge");

async function openScenario(
  page: Page,
  surface: Surface,
  targetName: string,
  testInfo: Parameters<Parameters<typeof test>[1]>[1],
) {
  const targetId = uniqueId(testInfo, surface.conversationType ?? surface.kind);
  const originalText = `${uniqueId(testInfo, "original")} pergunta original`;
  const original = makeMessage({
    id: `${targetId}-original`,
    sender_id: OTHER_USER_ID,
    sender_display_name: OTHER_USER_NAME,
    body_text: originalText,
    body_format: "v2",
  });
  const scenario = createScenario({
    kind: surface.kind,
    conversationType: surface.conversationType,
    targetId,
    targetName,
    messages: [original],
  });
  await installMessagingMocks(page, scenario);
  await page.goto(`/chat/${surface.kind}/${targetId}`);
  await expect(messageBubble(page, original.id)).toContainText(originalText);
  return { scenario, targetId, original, originalText };
}

async function replyToOriginal(page: Page, originalId: string, originalText: string) {
  const bubble = await revealActions(page, originalId);
  await bubble.getByRole("button", { name: "Responder" }).click();
  await expect(composerQuote(page)).toContainText(OTHER_USER_NAME);
  await expect(composerQuote(page)).toContainText(originalText);
}

async function attachFile(page: Page, name: string) {
  await page
    .getByTestId("chat-composer-file-input")
    .setInputFiles({ name, mimeType: "application/pdf", buffer: Buffer.from("%PDF-1.4 e2e") });
  await expect(pendingAttachment(page)).toContainText(name);
  await expect(pendingAttachment(page)).toContainText("Pronto para enviar");
}

/** Leaves for the other conversation and comes back through the sidebar. */
async function leaveAndReturn(page: Page, surface: Surface, targetName: string) {
  await surface.otherRow(page).click();
  await expect(surface.row(page, targetName)).toHaveAttribute("aria-selected", "false");
  // The row of the conversation left behind advertises its draft — and only
  // that: the badge is presence, never content.
  await expect(draftBadge(surface.row(page, targetName))).toHaveText("Rascunho");
  await expect(composerInput(page)).toHaveText("");
  await expect(composerQuote(page)).toHaveCount(0);
  await expect(pendingAttachment(page)).toHaveCount(0);
  await surface.row(page, targetName).click();
  await expect(surface.row(page, targetName)).toHaveAttribute("aria-selected", "true");
}

async function sendAndAwaitAck(page: Page, surface: Surface, targetId: string) {
  const posted = page.waitForResponse(
    (response) =>
      response.url().includes(surface.postUrl(targetId)) && response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Enviar mensagem" }).click();
  await posted;
}

/** Sent means gone — from the composer, from the row's badge, and after a reload. */
async function expectNothingLeftOf(
  page: Page,
  surface: Surface,
  targetName: string,
  sentText: string,
) {
  await expectComposerConsumedTheSend(page);
  await expect(pendingAttachment(page)).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Enviar mensagem" })).toBeDisabled();

  await surface.otherRow(page).click();
  await expect(surface.row(page, targetName)).toHaveAttribute("aria-selected", "false");
  await expect(draftBadge(surface.row(page, targetName))).toHaveCount(0);
  await surface.row(page, targetName).click();
  await expect(page.getByText(sentText)).toBeVisible();
  await expectComposerConsumedTheSend(page);
  await expect(pendingAttachment(page)).toHaveCount(0);

  await page.reload();
  await expect(page.getByText(sentText)).toBeVisible();
  await expectComposerConsumedTheSend(page);
  await expect(pendingAttachment(page)).toHaveCount(0);
}

for (const [label, surface] of Object.entries(surfaces)) {
  test.describe(`rascunho completo — ${label}`, () => {
    const targetName = label === "dm" ? OTHER_USER_NAME : `Conversa ${label} E2E`;

    test("reply + texto sobrevivem à navegação e são consumidos pelo envio confirmado", async ({
      page,
    }, testInfo) => {
      const { scenario, targetId, original, originalText } = await openScenario(
        page,
        surface,
        targetName,
        testInfo,
      );
      const draftText = "Vou verificar";

      await replyToOriginal(page, original.id, originalText);
      await fillComposer(page, draftText);

      await leaveAndReturn(page, surface, targetName);

      await expect(composerQuote(page)).toContainText(originalText);
      await expect(composerInput(page)).toHaveText(draftText);

      await sendAndAwaitAck(page, surface, targetId);

      expect(surface.posts(scenario)).toEqual([
        expect.objectContaining({ body_text: draftText, parent_message_id: original.id }),
      ]);
      const reply = messagesFor(scenario).find((message) => message.body_text === draftText);
      expect(reply).toBeDefined();
      await expect(messageBubble(page, reply!.id).getByTestId("chat-message-quote")).toContainText(
        originalText,
      );

      await expectNothingLeftOf(page, surface, targetName, draftText);
    });
  });
}

test.describe("rascunho com anexo", () => {
  const surface = surfaces.canal;
  const targetName = "Conversa anexo E2E";

  test("um anexo sobrevive à navegação e é consumido pelo envio confirmado", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId } = await openScenario(page, surface, targetName, testInfo);

    await attachFile(page, "relatorio.pdf");
    expect(scenario.requests.attachmentUploads).toHaveLength(1);

    await leaveAndReturn(page, surface, targetName);

    // Back exactly as it was: still uploaded, still ready, not re-uploaded.
    await expect(pendingAttachment(page)).toContainText("relatorio.pdf");
    await expect(pendingAttachment(page)).toContainText("Pronto para enviar");
    expect(scenario.requests.attachmentUploads).toHaveLength(1);

    await fillComposer(page, "segue o relatório");
    await sendAndAwaitAck(page, surface, targetId);

    expect(surface.posts(scenario)).toEqual([
      expect.objectContaining({ body_text: "segue o relatório", attachment_ids: ["upload-1"] }),
    ]);
    await expectNothingLeftOf(page, surface, targetName, "segue o relatório");
  });

  test("reply + texto + anexo voltam juntos e saem juntos", async ({ page }, testInfo) => {
    const { scenario, targetId, original, originalText } = await openScenario(
      page,
      surface,
      targetName,
      testInfo,
    );
    const draftText = "resposta com anexo";

    await replyToOriginal(page, original.id, originalText);
    await fillComposer(page, draftText);
    await attachFile(page, "evidencia.pdf");

    await leaveAndReturn(page, surface, targetName);

    await expect(composerQuote(page)).toContainText(originalText);
    await expect(composerInput(page)).toHaveText(draftText);
    await expect(pendingAttachment(page)).toContainText("evidencia.pdf");

    await sendAndAwaitAck(page, surface, targetId);

    expect(surface.posts(scenario)).toEqual([
      expect.objectContaining({
        body_text: draftText,
        parent_message_id: original.id,
        attachment_ids: ["upload-1"],
      }),
    ]);
    const reply = messagesFor(scenario).find((message) => message.body_text === draftText);
    expect(reply).toBeDefined();
    await expect(messageBubble(page, reply!.id).getByTestId("chat-message-quote")).toContainText(
      originalText,
    );
    await expectNothingLeftOf(page, surface, targetName, draftText);
  });
});

/**
 * Code Quality Review of #929, finding 1: the reader leaves and comes back
 * while the send is still open. The composer mounted on return is a new
 * instance; it must neither post the same message again nor keep showing it
 * once the server has acknowledged the first post.
 */
test.describe("envio em voo sobrevive ao remount do composer", () => {
  const surface = surfaces.dm;
  const targetName = OTHER_USER_NAME;

  test("A → B → A durante o POST: não duplica e converge quando o servidor confirma", async ({
    page,
  }, testInfo) => {
    const { scenario, targetId, original, originalText } = await openScenario(
      page,
      surface,
      targetName,
      testInfo,
    );
    const draftText = "Vou verificar";

    // The POST is intercepted ahead of the messaging mock and held until
    // released; everything else about it (recording, the message it
    // creates) is still the mock's, via fallback.
    const held = heldResponse();
    let posts = 0;
    await page.route(`**${surface.postUrl(targetId)}`, async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      posts += 1;
      await held.promise;
      await route.fallback();
    });

    await replyToOriginal(page, original.id, originalText);
    await fillComposer(page, draftText);
    const firstPost = page.waitForRequest(
      (request) => request.url().includes(surface.postUrl(targetId)) && request.method() === "POST",
    );
    await page.getByRole("button", { name: "Enviar mensagem" }).click();
    await firstPost;

    // Everything asserted while the response is held runs inside the block,
    // so a failure still releases the request: a route handler left waiting
    // would hang this worker long after the assertion that matters failed.
    try {
      await surface.otherRow(page).click();
      await expect(surface.row(page, targetName)).toHaveAttribute("aria-selected", "false");
      await surface.row(page, targetName).click();
      await expect(surface.row(page, targetName)).toHaveAttribute("aria-selected", "true");

      // Back on A with S1 still open: the draft is shown as going out, not
      // as sendable again — by the button, and by Enter.
      await expect(composerInput(page)).toHaveText(draftText);
      await expect(composerInput(page)).toHaveAttribute("aria-disabled", "true");
      await expect(page.getByRole("button", { name: "Enviar mensagem" })).toBeDisabled();
      await composerInput(page).focus();
      await page.keyboard.press("Enter");
      expect(posts).toBe(1);
    } finally {
      held.release();
    }

    const acknowledged = page.waitForResponse(
      (response) =>
        response.url().includes(surface.postUrl(targetId)) &&
        response.request().method() === "POST",
    );
    await acknowledged;

    expect(surface.posts(scenario)).toEqual([
      expect.objectContaining({ body_text: draftText, parent_message_id: original.id }),
    ]);
    const reply = messagesFor(scenario).find((message) => message.body_text === draftText);
    expect(reply).toBeDefined();
    await expect(messageBubble(page, reply!.id)).toContainText(draftText);
    await expectNothingLeftOf(page, surface, targetName, draftText);
    expect(posts).toBe(1);
  });
});
