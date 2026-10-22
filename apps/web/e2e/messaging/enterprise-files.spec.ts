import {
  legacyContainerCase,
  nativeDocumentCases,
} from "../../src/chat/attachmentDocumentFixtures";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

import {
  createScenario,
  installMessagingMocks,
  makeMessage,
  uniqueId,
  sidebarResponseBody,
} from "../helpers/messagingApi";

test("enterprise originals without preview download intact and pending files stay unavailable", async ({
  page,
}, info) => {
  const id = uniqueId(info, "enterprise-original");
  const original = Buffer.from('{"cost":12,"synthetic":true}\n');
  const filename = "Relatório.Custos.JSON";
  const attachment = {
    id: "enterprise-clean",
    filename,
    content_type: "application/json",
    size: original.length,
    status: "clean",
    preview_status: "unsupported",
  };
  const scenario = createScenario({
    kind: "channel",
    targetId: id,
    targetName: "Enterprise files",
    messages: [
      makeMessage({
        id: `${id}-message`,
        attachments: [
          attachment,
          { ...attachment, id: "enterprise-pending", status: "pending_scan" },
        ],
      }),
    ],
  });
  await installMessagingMocks(page, scenario);
  await page.route("**/api/files/attachments/enterprise-clean/content", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: original }),
  );
  await page.goto(`/chat/channel/${id}`);
  await expect(page.getByTestId("chat-message-attachment-download-enterprise-pending")).toHaveCount(
    0,
  );
  const saved = page.waitForEvent("download");
  await page.getByTestId("chat-message-attachment-download-enterprise-clean").click();
  const download = await saved;
  expect(download.suggestedFilename()).toBe(filename);
  const downloaded = await readFile((await download.path())!);
  expect(downloaded).toEqual(original);
  expect(createHash("sha256").update(downloaded).digest("hex")).toBe(
    createHash("sha256").update(original).digest("hex"),
  );
});

test("text typed before multiple native files survives uploads and is sent with their IDs", async ({
  page,
}, info) => {
  const id = uniqueId(info, "enterprise-composer");
  const scenario = createScenario({
    kind: "channel",
    targetId: id,
    targetName: "Enterprise files",
    messages: [],
  });
  await installMessagingMocks(page, scenario);
  const sidebar = JSON.parse(sidebarResponseBody(scenario));
  sidebar.data.workspace.max_message_attachments = 2;
  sidebar.data.workspace.max_message_attachment_bytes = 1024 * 1024;
  await page.route("**/api/chat/sidebar", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(sidebar) }),
  );
  let uploads = 0;
  await page.route(`**/api/files/channels/${id}/attachments`, async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    uploads += 1;
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          id: `enterprise-upload-${uploads}`,
          filename: `Custos-${uploads}.json`,
          contentType: "application/json",
          size: 12,
          status: "pending_scan",
          previewStatus: "unsupported",
        },
      }),
    });
  });
  await page.goto(`/chat/channel/${id}`);
  const editor = page.getByTestId("chat-composer-input");
  await editor.fill("Texto antes de selecionar os anexos.");
  await page.getByTestId("chat-composer-file-input").setInputFiles([
    { name: "Custos-1.json", mimeType: "application/json", buffer: Buffer.from('{"cost":12}') },
    {
      name: "Custos-2.json",
      mimeType: "application/octet-stream",
      buffer: Buffer.from('{"cost":15}'),
    },
  ]);
  await expect(page.getByRole("button", { name: "Enviar mensagem", exact: true })).toBeEnabled();
  await expect(editor).toHaveText("Texto antes de selecionar os anexos.");
  await page.getByRole("button", { name: "Enviar mensagem", exact: true }).click();
  await expect.poll(() => scenario.requests.channelPosts.length).toBe(1);
  expect(scenario.requests.channelPosts[0]).toMatchObject({
    body_text: "Texto antes de selecionar os anexos.",
    attachment_ids: ["enterprise-upload-1", "enterprise-upload-2"],
  });
  await expect(editor).toHaveText("");
});

for (const [code, message] of [
  ["file_policy_blocked", "Arquivo bloqueado pela política de segurança."],
  ["file_content_unrecognized", "O conteúdo não corresponde a um formato válido."],
  ["file_inspection_limit", "O arquivo excede os limites de inspeção segura."],
] as const) {
  test(`refused upload ${code} keeps the text draft and reports the decision`, async ({
    page,
  }, info) => {
    const id = uniqueId(info, code);
    const scenario = createScenario({
      kind: "channel",
      targetId: id,
      targetName: "Enterprise files",
      messages: [],
    });
    await installMessagingMocks(page, scenario);
    await page.route(`**/api/files/channels/${id}/attachments`, (route) =>
      route.fulfill({
        status: 415,
        contentType: "application/json",
        body: JSON.stringify({ error: { code, message: "redacted" } }),
      }),
    );
    await page.goto(`/chat/channel/${id}`);
    const editor = page.getByTestId("chat-composer-input");
    await editor.fill("Rascunho preservado.");
    await page.getByTestId("chat-composer-file-input").setInputFiles({
      name: "Relatório.docx",
      mimeType: "application/octet-stream",
      buffer: Buffer.from("invalid container"),
    });
    await expect(page.getByText(message, { exact: true })).toBeVisible();
    await expect(editor).toHaveText("Rascunho preservado.");
    expect(scenario.requests.channelPosts).toHaveLength(0);
  });
}

for (const { extension, contentType, kind } of [...nativeDocumentCases, legacyContainerCase]) {
  test(`${extension} document MIME opens the document viewer and preserves original download`, async ({
    page,
  }, info) => {
    const id = uniqueId(info, "canonical-viewer");
    const attachmentId = "canonical-document";
    const filename = `Custos.${extension}`;
    const original = Buffer.from("synthetic original bytes");
    const scenario = createScenario({
      kind: "channel",
      targetId: id,
      targetName: "Enterprise documents",
      messages: [
        makeMessage({
          id: `${id}-message`,
          attachments: [
            {
              id: attachmentId,
              filename,
              content_type: contentType,
              size: original.length,
              status: "clean",
              preview_status: "ready",
            },
          ],
        }),
      ],
    });
    await installMessagingMocks(page, scenario);
    await page.route(`**/api/files/attachments/${attachmentId}/document-preview`, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: { attachmentId, kind, pageCount: 1, labels: ["Custos"] } }),
      }),
    );
    const sheet = {
      columns: ["Cost"],
      rows: [["12"]],
      totalRowsRead: 1,
      truncatedRows: false,
      truncatedColumns: false,
    };
    const pageBody =
      kind === "pages"
        ? await readFile(
            new URL(
              "../../../../services/file-service/internal/filepolicy/testdata/image.jpg",
              import.meta.url,
            ),
          )
        : JSON.stringify(sheet);
    await page.route(`**/api/files/attachments/${attachmentId}/document-preview/pages/1`, (route) =>
      route.fulfill({
        status: 200,
        contentType: kind === "pages" ? "image/jpeg" : "application/vnd.nchat.preview-sheet+json",
        body: pageBody,
      }),
    );
    await page.route(`**/api/files/attachments/${attachmentId}/content`, (route) =>
      route.fulfill({ status: 200, contentType, body: original }),
    );
    await page.goto(`/chat/channel/${id}`);
    await page
      .getByRole("button", { name: `Visualizar ${filename}`, exact: true })
      .first()
      .click();
    const dialog = page.getByRole("dialog", { name: filename });
    await expect(dialog).toBeVisible();
    if (kind === "sheets") {
      await expect(dialog.getByRole("cell", { name: "12", exact: true })).toBeVisible();
      await expect(dialog.locator("img")).toHaveCount(0);
    }
    const saving = page.waitForEvent("download");
    await dialog.getByRole("button", { name: /Baixar/ }).click();
    const download = await saving;
    expect(download.suggestedFilename()).toBe(filename);
    expect(await readFile((await download.path())!)).toEqual(original);
  });
}
