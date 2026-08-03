import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_USER_ID,
  OTHER_USER_NAME,
  createScenario,
  groupDetailsFixture,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Painel de detalhes do grupo (issue #441).
 *
 * O ponto destes testes é que um grupo não é um canal: o painel usa o próprio
 * vocabulário ("Detalhes do grupo", "Participantes"), nunca visibilidade
 * público/privado, e os dados vêm do recurso de conversa.
 */
test.describe("painel de detalhes do grupo", () => {
  test("abre pelo cabeçalho, mostra as seções do grupo, preserva a conversa e devolve o foco", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "grupo");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId,
      targetName: "Time de Infra E2E",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem no grupo" })],
    });
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture(
        { id: targetId, name: "Time de Infra E2E" },
        [
          { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
          // Participante offline: num grupo ele continua na lista.
          { user_id: OTHER_USER_ID, display_name: OTHER_USER_NAME, presence: "offline" },
        ],
        // Mais participantes do que a prévia mostra: os dois números são
        // independentes e o painel não pode confundi-los.
        9,
      ),
    );
    // Quando o alvo do cenário já é um grupo, a sidebar padrão traz só ele; o
    // segundo grupo é adicionado para a troca do passo 7.
    scenario.sidebarDMs.push({
      id: GROUP_DM_ID,
      type: "group",
      name: GROUP_DM_NAME,
      unread_count: 0,
    });
    scenario.groupDetails.set(
      GROUP_DM_ID,
      groupDetailsFixture({ id: GROUP_DM_ID, name: GROUP_DM_NAME }, [
        { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
      ]),
    );
    scenario.conversationAttachments.set(targetId, [
      {
        id: `${targetId}-file`,
        filename: "ata-da-reuniao.pdf",
        contentType: "application/pdf",
        size: 1_258_291,
        status: "clean",
        createdAt: "2026-07-15T12:24:00Z",
      },
    ]);
    scenario.conversationAttachments.set(GROUP_DM_ID, [
      {
        id: `${GROUP_DM_ID}-file`,
        filename: "checklist.png",
        contentType: "image/png",
        size: 204_800,
        status: "pending_scan",
        createdAt: "2026-07-14T09:00:00Z",
      },
    ]);

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    // ── 1. compositor preenchido e não enviado ───────────────────────────
    const composer = page.getByTestId("chat-composer-input");
    await expect(composer).toBeVisible();
    await composer.click();
    await page.keyboard.insertText("rascunho que precisa sobreviver");
    await expect(composer).toContainText("rascunho que precisa sobreviver");

    const routeBeforeOpening = new URL(page.url()).pathname;
    const toggle = page.getByRole("button", { name: "Detalhes do grupo", exact: true });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");

    // ── 2. abrir o painel ────────────────────────────────────────────────
    await toggle.click();

    const panel = page.getByRole("complementary", { name: "Detalhes do grupo" });
    await expect(panel).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");

    // ── 3. seções do grupo, com dados reais ──────────────────────────────
    await expect(panel.getByRole("heading", { name: "Detalhes do grupo" })).toBeVisible();
    await expect(panel.getByTestId("chat-details-group-name")).toHaveText("Time de Infra E2E");
    await expect(panel.getByText(/Criado em 4 de março de 2024/)).toBeVisible();
    await expect(panel.getByText(/9 participantes/)).toBeVisible();
    await expect(panel.getByRole("heading", { name: "Participantes (9)" })).toBeVisible();

    const participants = panel.getByRole("list", { name: "Participantes do grupo" });
    await expect(participants.getByText(CURRENT_USER_NAME)).toBeVisible();
    await expect(participants.getByText("Você")).toBeVisible();
    // O participante offline continua visível — presença é informação, não filtro.
    await expect(participants.getByText(OTHER_USER_NAME)).toBeVisible();

    // ── 4. nada de vocabulário de canal ──────────────────────────────────
    await expect(panel.getByText(/Canal público/)).toHaveCount(0);
    await expect(panel.getByText(/Canal privado/)).toHaveCount(0);
    await expect(panel.getByRole("heading", { name: /Membros online/ })).toHaveCount(0);
    await expect(panel.getByTestId("chat-details-description")).toHaveCount(0);

    // ── 5. arquivos do grupo ─────────────────────────────────────────────
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("ata-da-reuniao.pdf"),
    ).toBeVisible();
    // Metadado apenas: nada aqui é link para download.
    await expect(panel.getByRole("link")).toHaveCount(0);

    // ── 6. rota inalterada e compositor preservado ───────────────────────
    expect(new URL(page.url()).pathname).toBe(routeBeforeOpening);
    await expect(composer).toContainText("rascunho que precisa sobreviver");
    await composer.click();
    await page.keyboard.insertText(" e continua editável");
    await expect(composer).toContainText("rascunho que precisa sobreviver e continua editável");

    // ── 7. trocar para outro grupo com o painel aberto ───────────────────
    await page.getByRole("option", { name: new RegExp(GROUP_DM_NAME) }).click();
    await expect(panel).toBeVisible();
    await expect(panel.getByTestId("chat-details-group-name")).toHaveText(GROUP_DM_NAME);
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("checklist.png"),
    ).toBeVisible();
    await expect(
      panel.getByRole("list", { name: "Arquivos recentes" }).getByText("ata-da-reuniao.pdf"),
    ).toHaveCount(0);
    // Arquivo ainda em análise aparece marcado e nunca como link.
    await expect(panel.getByText("Em análise")).toBeVisible();
    await expect(panel.getByRole("link")).toHaveCount(0);

    // ── 8. fechar pelo botão do painel e validar o retorno do foco ───────
    await panel.getByRole("button", { name: "Fechar detalhes do grupo" }).click();
    await expect(panel).toHaveCount(0);

    const toggleAfterClose = page.getByRole("button", { name: "Detalhes do grupo", exact: true });
    await expect(toggleAfterClose).toHaveAttribute("aria-expanded", "false");
    await expect(toggleAfterClose).toBeFocused();
    expect(new URL(page.url()).pathname).toContain(GROUP_DM_ID);
  });

  test("não empresta o painel de grupo para uma DM 1:1", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-direta");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "direct",
      targetId,
      targetName: OTHER_USER_NAME,
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    // Uma DM 1:1 tem o próprio painel — "Perfil" (issue #443) — e nunca o
    // vocabulário de grupo ou de canal.
    await expect(page.getByRole("button", { name: "Detalhes do grupo", exact: true })).toHaveCount(
      0,
    );
    await expect(page.getByRole("button", { name: "Detalhes do canal", exact: true })).toHaveCount(
      0,
    );
    await expect(
      page.getByRole("button", { name: `Abrir perfil de ${OTHER_USER_NAME}`, exact: true }),
    ).toBeVisible();
  });

  test("mostra estados vazios com o vocabulário do grupo", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "grupo-vazio");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "group",
      targetId,
      targetName: "Grupo Vazio",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    scenario.groupDetails.set(
      targetId,
      groupDetailsFixture({ id: targetId, name: "Grupo Vazio" }, [], 0),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);

    await page.getByRole("button", { name: "Detalhes do grupo", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Detalhes do grupo" });
    await expect(panel.getByText("Nenhuma mensagem fixada neste grupo.")).toBeVisible();
    await expect(panel.getByText("Nenhum arquivo enviado neste grupo.")).toBeVisible();
    await expect(panel.getByText("Nenhum participante para exibir.")).toBeVisible();
    // "Ver todos" continua visível e explicitamente indisponível: o botão segue
    // alcançável por teclado para que o motivo descrito por aria-describedby
    // possa ser anunciado.
    const seeAll = panel.getByRole("button", { name: "Ver todos" }).first();
    await expect(seeAll).toHaveAttribute("aria-disabled", "true");
    expect(await seeAll.evaluate((el) => (el as HTMLButtonElement).disabled)).toBe(false);

    // "Adicionar participantes" deixou de ser um placeholder (issue #398): virou
    // fluxo real, e esta fixture não concede a permissão, então a ação fica
    // ausente — o padrão seguro. O fluxo em si é coberto por add-members.spec.ts.
    await expect(panel.getByTestId("chat-details-add-members")).toHaveCount(0);
  });
});
