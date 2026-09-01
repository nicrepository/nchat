/**
 * Baixar uma mensagem de voz (issue #740).
 *
 * O que só um navegador real pode provar está aqui, e nada mais: que renderizar
 * a timeline não pede byte nenhum, que o clique pede exatamente a mídia daquela
 * mensagem pela rota autenticada de conteúdo, e que o arquivo chega ao disco com
 * um nome legível. As decisões de autorização — workspace, participação em DM,
 * membership de canal privado, convidado fora do canal, gate de malware — são do
 * file-service e estão cobertas de forma determinística nos testes Go da rota
 * `/attachments/{id}/content`; repeti-las contra um mock que sempre autoriza não
 * provaria nada.
 */

import { expect, test, type Page } from "@playwright/test";

import {
  CURRENT_USER_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  installMessagingMocks,
  makeMessage,
  uniqueId,
  type RawMessageAttachment,
} from "../helpers/messagingApi";

const SENT_AT = "2026-07-15T12:00:00.000Z";

/**
 * WAV rather than the WebM a real composer recording is: the mock content route
 * serves decodable PCM so a spec can assert that playback survives a download,
 * and the container is not what this feature turns on — `audio_kind` is.
 */
function voiceAttachment(id: string): RawMessageAttachment {
  return {
    id,
    filename: "voice-message.wav",
    content_type: "audio/wav",
    size: 8192,
    status: "clean",
    preview_status: "unsupported",
    audio_kind: "voice",
    duration_ms: 5_000,
  };
}

const downloadButton = (page: Page, attachmentId: string) =>
  page.getByTestId(`chat-audio-${attachmentId}-download`);

test.describe("mensagem de voz — download", () => {
  test("baixa a gravação sem interromper a reprodução, em uma DM", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm");
    const attachmentId = `${targetId}-voice`;
    const message = makeMessage({
      id: `${targetId}-msg`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "",
      body_format: "v2",
      created_at: SENT_AT,
      updated_at: SENT_AT,
      attachments: [voiceAttachment(attachmentId)],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    const player = page.getByTestId(`chat-audio-${attachmentId}-player`);
    await expect(player).toBeVisible();
    const download = downloadButton(page, attachmentId);
    await expect(download).toHaveAccessibleName("Baixar mensagem de voz");
    await expect(download).toHaveAttribute("title", "Baixar áudio");

    // Lazy loading (#675): o botão existe a partir de metadados que a mensagem
    // já trouxe. Renderizar não pede áudio nenhum.
    expect(scenario.requests.attachmentContentFetches).toEqual([]);

    // Tocando de verdade: o download roda ao lado da reprodução, não no lugar
    // dela.
    await page.getByTestId(`chat-audio-${attachmentId}-playpause`).click();
    const audio = page.getByTestId(`chat-audio-${attachmentId}-audio-el`);
    await expect(audio).toBeAttached();
    await expect
      .poll(() => audio.evaluate((element: HTMLAudioElement) => element.paused))
      .toBe(false);
    const playedUntil = await audio.evaluate((element: HTMLAudioElement) => element.currentTime);
    const beforeDownload = scenario.requests.attachmentContentFetches.length;

    const saved = page.waitForEvent("download");
    await download.click();
    const file = await saved;

    expect(file.suggestedFilename()).toMatch(/^mensagem-de-voz-2026-07-1[45]-\d{4}\.wav$/);
    expect(scenario.requests.attachmentContentFetches.length).toBe(beforeDownload + 1);
    // Só a mídia daquela mensagem foi pedida, sempre pela rota autenticada.
    expect(new Set(scenario.requests.attachmentContentFetches)).toEqual(new Set([attachmentId]));

    // Ainda tocando, do mesmo ponto, e o foco continua onde o usuário o deixou.
    expect(await audio.evaluate((element: HTMLAudioElement) => element.paused)).toBe(false);
    expect(
      await audio.evaluate((element: HTMLAudioElement) => element.currentTime),
    ).toBeGreaterThanOrEqual(playedUntil);
    expect(await audio.evaluate((element: HTMLAudioElement) => element.playbackRate)).toBe(1);
    await expect(download).toBeFocused();
    await expect(page.getByTestId(`chat-audio-${attachmentId}-rate`)).toHaveText("1x");

    // E a velocidade continua ciclando depois do download.
    await page.getByTestId(`chat-audio-${attachmentId}-rate`).click();
    await expect(page.getByTestId(`chat-audio-${attachmentId}-rate`)).toHaveText("1.5x");
  });

  test.describe("em um telefone com toque", () => {
    test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });

    test("mantém o download alcançável em um canal", async ({ page }, testInfo) => {
      const targetId = uniqueId(testInfo, "canal");
      const attachmentId = `${targetId}-voice`;
      const message = makeMessage({
        id: `${targetId}-msg`,
        sender_id: CURRENT_USER_ID,
        body_text: "",
        created_at: SENT_AT,
        updated_at: SENT_AT,
        attachments: [voiceAttachment(attachmentId)],
      });
      const scenario = createScenario({
        kind: "channel",
        targetId,
        targetName: "Canal E2E",
        messages: [message],
      });

      await installMessagingMocks(page, scenario);
      await page.goto(`/chat/channel/${targetId}`);

      const download = downloadButton(page, attachmentId);
      await expect(download).toBeVisible();
      // Alvo de toque confortável sem crescer o player: a área clicável passa da
      // caixa visual do botão.
      const box = await download.boundingBox();
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(30);

      const saved = page.waitForEvent("download");
      await download.tap();
      await saved;
      expect(scenario.requests.attachmentContentFetches).toEqual([attachmentId]);
    });
  });

  test("avisa sem derrubar o player quando o acesso é perdido antes do clique", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-403");
    const attachmentId = `${targetId}-voice`;
    const message = makeMessage({
      id: `${targetId}-msg`,
      sender_id: OTHER_USER_ID,
      sender_display_name: OTHER_USER_NAME,
      body_text: "",
      body_format: "v2",
      created_at: SENT_AT,
      updated_at: SENT_AT,
      attachments: [voiceAttachment(attachmentId)],
    });
    const scenario = createScenario({
      kind: "dm",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [message],
    });

    await installMessagingMocks(page, scenario);
    // Registrada depois, então tem precedência: a conversa deixou de ser
    // acessível entre o render e o clique.
    await page.route("**/api/files/attachments/*/content", (route) =>
      route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "forbidden", message: "forbidden" } }),
      }),
    );
    await page.goto(`/chat/dm/${targetId}`);

    await downloadButton(page, attachmentId).click();

    await expect(page.getByTestId(`chat-audio-${attachmentId}-download-error`)).toHaveText(
      "Não foi possível baixar o áudio.",
    );
    // O player continua inteiro e o botão continua pronto para outra tentativa.
    await expect(page.getByTestId(`chat-audio-${attachmentId}-player`)).toBeVisible();
    await expect(downloadButton(page, attachmentId)).toBeEnabled();
  });
});
