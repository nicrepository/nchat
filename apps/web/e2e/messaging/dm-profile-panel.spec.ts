import { expect, test } from "@playwright/test";

import {
  CURRENT_USER_ID,
  CURRENT_USER_NAME,
  GROUP_DM_ID,
  GROUP_DM_NAME,
  OTHER_CHANNEL_ID,
  OTHER_CHANNEL_NAME,
  channelDetailsFixture,
  createScenario,
  directProfileFixture,
  groupDetailsFixture,
  installMessagingMocks,
  makeMessage,
  uniqueId,
} from "../helpers/messagingApi";

/**
 * Painel de perfil em DM 1:1 (issue #443).
 *
 * O ponto destes testes é que uma DM 1:1 não é um grupo pequeno: o painel mostra
 * o perfil da outra pessoa ("Perfil"), com o vocabulário de perfil e nenhuma
 * seção de conversa — sem participantes, sem arquivos, sem visibilidade. E o
 * perfil exibido é sempre o do outro participante, resolvido pelo servidor.
 */
test.describe("painel de perfil em DM 1:1", () => {
  test("abre pelo cabeçalho, mostra o perfil do outro participante, preserva a conversa e devolve o foco", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-juliane");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "direct",
      targetId,
      targetName: "Juliane Lino",
      messages: [makeMessage({ id: `${targetId}-m1`, body_text: "Mensagem na DM" })],
    });
    scenario.directProfiles.set(
      targetId,
      directProfileFixture(targetId, {
        user_id: "e2e-juliane",
        display_name: "Juliane Lino",
        email: "juliane.lino@nic-labs.test",
        presence: "online",
        job_title: "Infraestrutura & Suporte",
        department: "TI",
        timezone: "America/Sao_Paulo",
      }),
    );
    // Anexos existem para esta conversa e mesmo assim não devem aparecer: o
    // painel de perfil não tem seção de arquivos.
    scenario.conversationAttachments.set(targetId, [
      {
        id: `${targetId}-file`,
        filename: "nao-deve-aparecer.pdf",
        contentType: "application/pdf",
        size: 1024,
        status: "clean",
        createdAt: "2026-07-15T12:24:00Z",
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
    const toggle = page.getByRole("button", { name: "Abrir perfil de Juliane Lino", exact: true });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");

    // ── 2. abrir o painel ────────────────────────────────────────────────
    await toggle.click();

    const panel = page.getByRole("complementary", { name: "Perfil" });
    await expect(panel).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");

    // ── 3. o perfil, com os dados reais do outro participante ────────────
    await expect(panel.getByRole("heading", { name: "Perfil" })).toBeVisible();
    await expect(panel.getByTestId("chat-details-profile-name")).toHaveText("Juliane Lino");
    // Presença é palavra, não só cor.
    await expect(panel.getByTestId("chat-details-profile-status")).toHaveText(/Online/);

    const meta = panel.getByTestId("chat-details-profile-meta");
    await expect(meta).toContainText("Infraestrutura & Suporte");
    await expect(meta).toContainText("TI");
    await expect(meta).toContainText("America/Sao_Paulo");
    await expect(meta).toContainText("juliane.lino@nic-labs.test");
    // Horário local derivado do fuso do perfil: hora e minuto, nunca vazio.
    await expect(meta).toContainText(/\d{2}:\d{2}/);
    // O e-mail é texto: nada aqui vira link.
    await expect(panel.getByRole("link")).toHaveCount(0);

    // ── 4. nada de vocabulário de grupo nem de canal ─────────────────────
    await expect(panel.getByRole("heading", { name: /Participantes/ })).toHaveCount(0);
    await expect(panel.getByRole("heading", { name: /Membros online/ })).toHaveCount(0);
    await expect(panel.getByRole("heading", { name: "Arquivos recentes" })).toHaveCount(0);
    await expect(panel.getByRole("heading", { name: "Mensagem fixada" })).toHaveCount(0);
    await expect(panel.getByText("nao-deve-aparecer.pdf")).toHaveCount(0);
    await expect(panel.getByText(/Canal público/)).toHaveCount(0);
    await expect(panel.getByTestId("chat-details-group-name")).toHaveCount(0);
    // O próprio usuário nunca é o perfil da DM.
    await expect(panel.getByText(CURRENT_USER_NAME)).toHaveCount(0);

    // ── 5. ação de perfil completo, visivelmente indisponível ────────────
    const fullProfile = panel.getByRole("button", { name: /Ver perfil completo/ });
    // Indisponível pela semântica, e não pelo atributo HTML: `disabled` tiraria
    // o botão da ordem de tabulação e levaria junto o motivo anunciado.
    await expect(fullProfile).toHaveAttribute("aria-disabled", "true");
    expect(await fullProfile.evaluate((el) => (el as HTMLButtonElement).disabled)).toBe(false);

    // ── 6. rota inalterada e compositor preservado ───────────────────────
    expect(new URL(page.url()).pathname).toBe(routeBeforeOpening);
    await expect(composer).toContainText("rascunho que precisa sobreviver");
    await composer.click();
    await page.keyboard.insertText(" e continua editável");
    await expect(composer).toContainText("rascunho que precisa sobreviver e continua editável");

    // ── 7. fechar pelo botão do painel e validar o retorno do foco ───────
    await panel.getByRole("button", { name: "Fechar perfil" }).click();
    await expect(panel).toHaveCount(0);

    const toggleAfterClose = page.getByRole("button", {
      name: "Abrir perfil de Juliane Lino",
      exact: true,
    });
    await expect(toggleAfterClose).toHaveAttribute("aria-expanded", "false");
    await expect(toggleAfterClose).toBeFocused();
    expect(new URL(page.url()).pathname).toBe(routeBeforeOpening);
  });

  test("troca entre DMs sem carregar dados da conversa anterior", async ({ page }, testInfo) => {
    // The discriminator is appended *after* uniqueId, which truncates: a long
    // test title would otherwise collapse "dm-a" and "dm-b" into one id and the
    // two fixtures would silently become one.
    const base = uniqueId(testInfo, "dm");
    const firstId = `${base}-a`;
    const secondId = `${base}-b`;
    const scenario = createScenario({
      kind: "dm",
      conversationType: "direct",
      targetId: firstId,
      targetName: "Juliane Lino",
      messages: [makeMessage({ id: `${firstId}-m1` })],
    });
    scenario.sidebarDMs.push({
      id: secondId,
      type: "direct",
      name: "Marcos Prado",
      unread_count: 0,
      counterpart: { user_id: "e2e-marcos", display_name: "Marcos Prado" },
    });
    scenario.directProfiles.set(
      firstId,
      directProfileFixture(firstId, {
        user_id: "e2e-juliane",
        display_name: "Juliane Lino",
        email: "juliane.lino@nic-labs.test",
        department: "TI",
      }),
    );
    scenario.directProfiles.set(
      secondId,
      directProfileFixture(secondId, {
        user_id: "e2e-marcos",
        display_name: "Marcos Prado",
        email: "marcos.prado@nic-labs.test",
        department: "Financeiro",
      }),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${firstId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    await page.getByRole("button", { name: "Abrir perfil de Juliane Lino", exact: true }).click();
    const panel = page.getByRole("complementary", { name: "Perfil" });
    await expect(panel.getByTestId("chat-details-profile-name")).toHaveText("Juliane Lino");
    await expect(panel.getByTestId("chat-details-profile-meta")).toContainText(
      "juliane.lino@nic-labs.test",
    );

    // Trocar de DM com o painel aberto.
    await page.getByRole("option", { name: /Marcos Prado/ }).click();

    await expect(panel.getByTestId("chat-details-profile-name")).toHaveText("Marcos Prado");
    const meta = panel.getByTestId("chat-details-profile-meta");
    await expect(meta).toContainText("marcos.prado@nic-labs.test");
    await expect(meta).toContainText("Financeiro");
    // Nenhum dado da conversa anterior pode sobreviver à troca: mostrar o e-mail
    // de uma pessoa sob o nome de outra é a falha que este passo existe para pegar.
    await expect(meta).not.toContainText("juliane.lino@nic-labs.test");
    await expect(meta).not.toContainText("TI");
    await expect(panel.getByText("Juliane Lino")).toHaveCount(0);
  });

  test("troca entre tipos usa o vocabulário de cada conversa", async ({ page }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-direta");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "direct",
      targetId,
      targetName: "Juliane Lino",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    scenario.directProfiles.set(
      targetId,
      directProfileFixture(targetId, {
        user_id: "e2e-juliane",
        display_name: "Juliane Lino",
        email: "juliane.lino@nic-labs.test",
      }),
    );
    scenario.groupDetails.set(
      GROUP_DM_ID,
      groupDetailsFixture({ id: GROUP_DM_ID, name: GROUP_DM_NAME }, [
        { user_id: CURRENT_USER_ID, display_name: CURRENT_USER_NAME, presence: "online" },
      ]),
    );
    scenario.channelDetails.set(
      OTHER_CHANNEL_ID,
      channelDetailsFixture(
        {
          id: OTHER_CHANNEL_ID,
          slug: "e2e-canal",
          display_name: OTHER_CHANNEL_NAME,
          type: "public",
        },
        [
          {
            user_id: CURRENT_USER_ID,
            display_name: CURRENT_USER_NAME,
            role: "member",
            presence: "online",
          },
        ],
      ),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    // ── direct ───────────────────────────────────────────────────────────
    await page.getByRole("button", { name: "Abrir perfil de Juliane Lino", exact: true }).click();
    await expect(page.getByRole("complementary", { name: "Perfil" })).toBeVisible();

    // ── direct → group ───────────────────────────────────────────────────
    await page.getByRole("option", { name: new RegExp(GROUP_DM_NAME) }).click();
    const groupPanel = page.getByRole("complementary", { name: "Detalhes do grupo" });
    await expect(groupPanel).toBeVisible();
    await expect(groupPanel.getByTestId("chat-details-group-name")).toHaveText(GROUP_DM_NAME);
    await expect(page.getByRole("complementary", { name: "Perfil" })).toHaveCount(0);
    await expect(page.getByTestId("chat-details-profile-name")).toHaveCount(0);

    // ── group → channel ──────────────────────────────────────────────────
    await page.getByRole("option", { name: new RegExp(OTHER_CHANNEL_NAME) }).click();
    const channelPanel = page.getByRole("complementary", { name: "Detalhes do canal" });
    await expect(channelPanel).toBeVisible();
    await expect(channelPanel.getByText(/Canal público/)).toBeVisible();
    await expect(page.getByRole("complementary", { name: "Detalhes do grupo" })).toHaveCount(0);

    // ── channel → direct ─────────────────────────────────────────────────
    await page.getByRole("option", { name: /Juliane Lino/ }).click();
    const profilePanel = page.getByRole("complementary", { name: "Perfil" });
    await expect(profilePanel).toBeVisible();
    await expect(profilePanel.getByTestId("chat-details-profile-name")).toHaveText("Juliane Lino");
    // Nada do canal sobreviveu à volta.
    await expect(profilePanel.getByText(/Canal público/)).toHaveCount(0);
    await expect(page.getByRole("complementary", { name: "Detalhes do canal" })).toHaveCount(0);
  });

  test("alcança 'Ver perfil completo' pelo teclado sem que a ação faça nada", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-teclado");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "direct",
      targetId,
      targetName: "Juliane Lino",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    scenario.directProfiles.set(
      targetId,
      directProfileFixture(targetId, {
        user_id: "e2e-juliane",
        display_name: "Juliane Lino",
        email: "juliane.lino@nic-labs.test",
      }),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    const routeBeforeOpening = new URL(page.url()).pathname;
    await page.getByRole("button", { name: "Abrir perfil de Juliane Lino", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Perfil" });
    await expect(panel).toBeVisible();
    // O painel entrega o foco ao botão de fechar; a partir daí a ordem natural
    // de tabulação precisa alcançar a ação indisponível.
    const close = panel.getByRole("button", { name: "Fechar perfil" });
    await expect(close).toBeFocused();

    const action = panel.getByRole("button", { name: "Ver perfil completo", exact: true });
    // Percorre a ordem real em vez de assumir um número fixo de paradas, e sem
    // nunca chamar focus() — o que este passo prova é que o Tab chega lá.
    for (let stop = 0; stop < 20; stop += 1) {
      if (await action.evaluate((el) => el === document.activeElement)) break;
      await page.keyboard.press("Tab");
    }
    await expect(action).toBeFocused();

    // Estado exposto semanticamente, nunca pelo atributo HTML disabled — que
    // tiraria o elemento da ordem de tabulação junto com o motivo.
    await expect(action).toHaveAttribute("aria-disabled", "true");
    expect(await action.evaluate((el) => (el as HTMLButtonElement).disabled)).toBe(false);

    const reasonId = await action.getAttribute("aria-describedby");
    expect(reasonId).toBeTruthy();
    const reason = panel.locator(`#${reasonId}`);
    await expect(reason).toHaveCount(1);
    await expect(reason).toHaveText(
      "O perfil completo de outros usuários ainda não está disponível nesta versão.",
    );

    // ── ativar não faz nada ──────────────────────────────────────────────
    await page.keyboard.press("Enter");
    await page.keyboard.press("Space");
    // Um clique real é recusado pelo próprio Playwright, que lê aria-disabled
    // como "not enabled" — evidência a mais de que o estado está exposto. O
    // evento é despachado direto para cobrir também a ativação programática,
    // sem recorrer a force.
    await action.dispatchEvent("click");

    expect(new URL(page.url()).pathname).toBe(routeBeforeOpening);
    await expect(panel).toBeVisible();
    await expect(panel.getByTestId("chat-details-profile-name")).toHaveText("Juliane Lino");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(panel.getByRole("link")).toHaveCount(0);

    // O painel continua fechando normalmente e devolvendo o foco.
    await close.click();
    await expect(panel).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Abrir perfil de Juliane Lino", exact: true }),
    ).toBeFocused();
  });

  test("mantém o painel utilizável quando o perfil não tem dados profissionais", async ({
    page,
  }, testInfo) => {
    const targetId = uniqueId(testInfo, "dm-minima");
    const scenario = createScenario({
      kind: "dm",
      conversationType: "direct",
      targetId,
      targetName: "Juliane Lino",
      messages: [makeMessage({ id: `${targetId}-m1` })],
    });
    // A resposta real de hoje: identidade e nada mais. Nenhum campo ausente
    // pode quebrar o painel nem virar erro.
    scenario.directProfiles.set(
      targetId,
      directProfileFixture(targetId, {
        user_id: "e2e-juliane",
        display_name: "Juliane Lino",
      }),
    );

    await installMessagingMocks(page, scenario);
    await page.goto(`/chat/dm/${targetId}`);
    await expect(page.getByTestId("chat-composer-input")).toBeVisible();

    await page.getByRole("button", { name: "Abrir perfil de Juliane Lino", exact: true }).click();

    const panel = page.getByRole("complementary", { name: "Perfil" });
    await expect(panel.getByTestId("chat-details-profile-name")).toHaveText("Juliane Lino");
    // Linhas presentes e honestas, nunca linhas vazias.
    await expect(panel.getByTestId("chat-details-profile-meta")).toContainText("Não informado");
    // RF-58: presença agora é afirmada pelo servidor em tempo real, e este
    // perfil não tem sessão alguma — "Offline" é o que o servidor respondeu,
    // não um palpite do cliente. O caso em que nada foi respondido ainda
    // continua sem indicador: está coberto em presence-avatars.spec.ts.
    await expect(panel.getByTestId("chat-details-profile-status")).toHaveText("Offline");
    // Ausência de dados não é erro.
    await expect(panel.getByText("Não foi possível carregar o perfil.")).toHaveCount(0);
  });
});
