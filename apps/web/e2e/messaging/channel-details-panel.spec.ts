import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  OTHER_CHANNEL_ID,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  channelDetailsFixture,
  createScenario,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

test.describe("painel de detalhes do canal", () => {
  test("abre pelo cabeçalho, mostra as seções, preserva a conversa e devolve o foco", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-details");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Infraestrutura E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no canal" })],
    });
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(
          channel,
          [
            {
              user_id: CURRENT_USER_ID,
              display_name: CURRENT_USER_NAME,
              role: "moderator",
              presence: "online",
            },
            {
              user_id: OTHER_USER_ID,
              display_name: OTHER_USER_NAME,
              role: "member",
              presence: "online",
            },
          ],
          // The channel has more members than are connected: the panel must
          // report both figures without confusing them.
          12,
        ),
      );
    }
    scenario.channelAttachments.set(targetId, [
      {
        id: `${targetId}-file`,
        filename: "relatorio-backup.pdf",
        contentType: "application/pdf",
        size: 2_516_582,
        status: "clean",
        createdAt: "2026-07-15T12:24:00Z",
      },
    ]);
    scenario.channelAttachments.set(OTHER_CHANNEL_ID, [
      {
        id: `${OTHER_CHANNEL_ID}-file`,
        filename: "topologia-rede.png",
        contentType: "image/png",
        size: 911_360,
        status: "pending_scan",
        createdAt: "2026-07-14T09:00:00Z",
      },
    ]);

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const composer = page.getByTestId("chat-composer-input");
    await expect(composer).toBeVisible();
    await composer.click();
    await page.keyboard.insertText("rascunho que precisa sobreviver");
    await expect(composer).toContainText("rascunho que precisa sobreviver");

    const routeBeforeOpening = new URL(page.url()).pathname;
    const toggle = page.getByRole("button", { name: "Detalhes do canal", exact: true });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");

    // ── 1. abrir o painel ────────────────────────────────────────────────
    await toggle.click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(panel).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");

    // ── 2. seções principais com dados reais do canal ────────────────────
    await expect(panel.getByRole("heading", { name: "Detalhes do canal" })).toBeVisible();
    await expect(panel.getByText(/Criado em 12 de janeiro de 2024/)).toBeVisible();
    await expect(panel.getByText(/Canal público · 12 membros/)).toBeVisible();
    await expect(panel.getByRole("heading", { name: "Membros online (2)" })).toBeVisible();
    const members = panel.getByRole("list", { name: "Membros online do canal" });
    await expect(members.getByText(CURRENT_USER_NAME)).toBeVisible();
    await expect(members.getByText("Você")).toBeVisible();
    await expect(members.getByText(OTHER_USER_NAME)).toBeVisible();
    await expect(panel.getByRole("heading", { name: "Mensagem fixada" })).toBeVisible();
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("relatorio-backup.pdf"),
    ).toBeVisible();

    // Ações ainda sem fluxo: visíveis, desabilitadas, sem simular sucesso.
    await expect(panel.getByRole("button", { name: /Adicionar membros/ })).toBeDisabled();

    // ── 3. a rota não mudou e o compositor segue utilizável ──────────────
    expect(new URL(page.url()).pathname).toBe(routeBeforeOpening);
    await expect(composer).toBeVisible();
    await expect(composer).toContainText("rascunho que precisa sobreviver");
    await composer.click();
    await page.keyboard.insertText(" e continua editável");
    await expect(composer).toContainText("rascunho que precisa sobreviver e continua editável");

    // ── 4. trocar de canal com o painel aberto ───────────────────────────
    await page.getByRole("option", { name: /Canal E2E/ }).click();
    await expect(panel).toBeVisible();
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("topologia-rede.png"),
    ).toBeVisible();
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("relatorio-backup.pdf"),
    ).toHaveCount(0);
    // Arquivo ainda em análise aparece marcado e nunca como link.
    await expect(panel.getByText("Em análise")).toBeVisible();
    await expect(panel.getByRole("link")).toHaveCount(0);
    await expect(panel.locator("a")).toHaveCount(0);

    // ── 5. fechar pelo botão do painel e validar o retorno do foco ───────
    await panel.getByRole("button", { name: "Fechar detalhes do canal" }).click();
    await expect(panel).toHaveCount(0);

    const toggleAfterClose = page.getByRole("button", { name: "Detalhes do canal", exact: true });
    await expect(toggleAfterClose).toHaveAttribute("aria-expanded", "false");
    await expect(toggleAfterClose).toBeFocused();
    // Fechar também não trocou de rota: continua no canal aberto no passo 4.
    expect(new URL(page.url()).pathname).toContain(OTHER_CHANNEL_ID);
  });

  test("abre e fecha pelo mesmo controle do cabeçalho, por teclado", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-details-keyboard");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Teclado",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(channel, [
          {
            user_id: CURRENT_USER_ID,
            display_name: CURRENT_USER_NAME,
            role: "member",
            presence: "online",
          },
        ]),
      );
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    const toggle = page.getByRole("button", { name: "Detalhes do canal", exact: true });
    await expect(toggle).toBeVisible();
    await toggle.focus();
    await page.keyboard.press("Enter");

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(panel).toBeVisible();
    // O foco entra no painel, no seu botão de fechar.
    await expect(panel.getByRole("button", { name: "Fechar detalhes do canal" })).toBeFocused();

    await toggle.click();
    await expect(panel).toHaveCount(0);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
  });

  test("distingue 'ninguém online' de 'canal sem membros'", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-details-empty");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Vazio",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    for (const channel of scenario.sidebarChannels) {
      // 31 membros no canal, nenhum conectado — exatamente o cenário em que o
      // painel não pode dizer que o canal está vazio.
      scenario.channelDetails.set(channel.id, channelDetailsFixture(channel, [], 31));
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByRole("button", { name: "Detalhes do canal", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(panel.getByText("Nenhum membro online no momento.")).toBeVisible();
    // O tamanho do canal continua reportado e não vira zero.
    await expect(panel.getByText(/Canal público · 31 membros/)).toBeVisible();
    await expect(panel.getByRole("heading", { name: "Membros online (0)" })).toBeVisible();
    await expect(panel.getByText("Nenhuma mensagem fixada neste canal.")).toBeVisible();
    await expect(panel.getByText("Nenhum arquivo enviado neste canal.")).toBeVisible();
    await expect(panel.getByText("Este canal ainda não tem descrição.")).toBeVisible();
  });

  test("mostra o membro online que fica fora dos primeiros 30 nomes", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "channel-details-online-cut");
    const scenario = createScenario({
      kind: "channel",
      targetId,
      targetName: "Canal Grande",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    // O servidor filtra por presença antes do limite, então o único membro
    // conectado — o 31º em ordem alfabética — é o que chega ao cliente.
    for (const channel of scenario.sidebarChannels) {
      scenario.channelDetails.set(
        channel.id,
        channelDetailsFixture(
          channel,
          [
            {
              user_id: "e2e-ultimo-alfabetico",
              display_name: "Zulmira Última",
              role: "member",
              presence: "online",
            },
          ],
          31,
        ),
      );
    }

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/channel/${targetId}`);

    await page.getByRole("button", { name: "Detalhes do canal", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do canal" });
    const members = panel.getByRole("list", { name: "Membros online do canal" });
    await expect(members.getByText("Zulmira Última")).toBeVisible();
    await expect(members.getByRole("listitem")).toHaveCount(1);
    await expect(panel.getByRole("heading", { name: "Membros online (1)" })).toBeVisible();
    await expect(panel.getByText(/Canal público · 31 membros/)).toBeVisible();
  });
});
