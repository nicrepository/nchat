import { expect, test, type Page } from "@playwright/test";

/**
 * End-to-end coverage of the management screens (issue #579).
 *
 * The Admin API is stubbed at the network boundary, exactly as the console
 * shell's specs stub theirs: what is under test here is the console's own
 * behaviour — the search, the filters, the paging, the confirmations and the
 * feedback — not the server's authorization, which has its own suite in Go.
 *
 * The rule the shell's specs set holds here too: no case below may assert that
 * the frontend is what keeps an unauthorized operator out. Every refusal ends
 * in a refusal because the API refused.
 */

const SUPERUSER = {
  identity: {
    user_id: "11111111-1111-1111-1111-111111111111",
    email: "admin@example.test",
    display_name: "Admin E2E",
    avatar_url: "",
  },
  capabilities: ["admin.superuser"],
  environment: "STAGING",
  build: { service: "admin-service", version: "0.0.0", commit: "e2e" },
  session: {
    idle_expires_at: "2099-01-01T00:00:00Z",
    absolute_expires_at: "2099-01-01T08:00:00Z",
  },
  csrf_token: "csrf-e2e",
};

const MIB = 1024 * 1024;

function user(overrides: Record<string, unknown> = {}) {
  return {
    // A real identifier, not a readable placeholder: the console validates the
    // audit filter as a UUID, so a fixture that is not one would silently drop
    // it and this suite would prove nothing.
    id: "11111111-2222-4333-8444-555555555555",
    email: "ana@example.test",
    display_name: "Ana",
    full_name: "Ana Lima",
    avatar_url: "",
    status: "active",
    auth_source: "manual",
    external_provider: "",
    identity_managed_externally: false,
    last_login_at: "2026-08-01T10:00:00Z",
    created_at: "2026-01-01T00:00:00Z",
    platform_admin: false,
    admin_roles: [],
    workspace_roles: [],
    active_sessions: 2,
    ...overrides,
  };
}

function channel(overrides: Record<string, unknown> = {}) {
  return {
    id: "c-eng",
    workspace_id: "w1",
    workspace_name: "NChat",
    slug: "engenharia",
    display_name: "Engenharia",
    type: "private",
    status: "active",
    is_general: false,
    member_count: 12,
    moderator_count: 1,
    created_by_name: "Root",
    created_by_email: "root@example.test",
    created_at: "2026-01-01T00:00:00Z",
    last_activity_at: "2026-08-01T10:00:00Z",
    ...overrides,
  };
}

async function json(page: Page, pattern: string, body: unknown, status = 200) {
  await page.route(pattern, (route) =>
    route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) }),
  );
}

async function signedIn(page: Page, capabilities: string[] = ["admin.superuser"]) {
  await json(page, "**/api/admin/bootstrap", { data: { ...SUPERUSER, capabilities } });
}

test("busca um usuário e envia o termo ao servidor", async ({ page }) => {
  await signedIn(page);
  const requests: string[] = [];
  await page.route("**/api/admin/users?**", (route) => {
    requests.push(route.request().url());
    const matched = route.request().url().includes("q=ana");
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          users: matched ? [user()] : [user(), user({ id: "u-bruno", full_name: "Bruno Dias" })],
          pagination: { next_cursor: null, has_more: false },
        },
      }),
    });
  });

  await page.goto("/users");
  await expect(page.getByRole("rowheader", { name: /Bruno Dias/ })).toBeVisible();

  await page.getByLabel("Buscar por nome, e-mail ou login").fill("ana");
  await expect(page.getByRole("rowheader", { name: /Bruno Dias/ })).toHaveCount(0);
  await expect(page.getByRole("rowheader", { name: /Ana Lima/ })).toBeVisible();
  // The filtering happened on the server, not in the browser.
  expect(requests.some((url) => url.includes("q=ana"))).toBe(true);
});

test("executa uma administração autorizada e confirma o resultado", async ({ page }) => {
  await signedIn(page);
  let suspended = false;
  await page.route("**/api/admin/users?**", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          users: [user(suspended ? { status: "suspended", active_sessions: 0 } : {})],
          pagination: { next_cursor: null, has_more: false },
        },
      }),
    }),
  );
  await page.route("**/api/admin/users/11111111-2222-4333-8444-555555555555/status", (route) => {
    suspended = true;
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          user_id: "11111111-2222-4333-8444-555555555555",
          from_status: "active",
          to_status: "suspended",
          revoked_sessions: 2,
        },
      }),
    });
  });

  await page.goto("/users");
  await page.getByRole("button", { name: "Desativar" }).click();

  // A sensitive action states its impact before it is confirmed.
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Todas as sessões ativas são encerradas");
  await dialog.getByRole("button", { name: "Desativar" }).click();

  await expect(page.getByTestId("admin-feedback")).toContainText("2 sessão(ões) encerrada(s)");
});

test("uma mutação sem capability é recusada pela API e a tela diz isso", async ({ page }) => {
  // The console draws no action for a capability the session lacks; the point
  // of this case is the other half — the API refuses even so.
  await signedIn(page, ["admin.users.read"]);
  await json(page, "**/api/admin/users?**", {
    data: { users: [user()], pagination: { next_cursor: null, has_more: false } },
  });
  await json(
    page,
    "**/api/admin/users/11111111-2222-4333-8444-555555555555",
    { error: { code: "forbidden", message: "" } },
    403,
  );

  await page.goto("/users");
  await expect(page.getByRole("button", { name: "Desativar" })).toHaveCount(0);

  await page.getByRole("button", { name: "Detalhes" }).click();
  await expect(page.getByRole("alert")).toContainText("não tem permissão");
});

test("filtra canais e grupos pelo servidor", async ({ page }) => {
  await signedIn(page);
  const requests: string[] = [];
  await page.route("**/api/admin/channels?**", (route) => {
    requests.push(route.request().url());
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      }),
    });
  });
  await json(page, "**/api/admin/conversations?**", {
    data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
  });

  await page.goto("/channels");
  await expect(page.getByRole("rowheader", { name: /Engenharia/ })).toBeVisible();

  await page.getByLabel("Visibilidade").selectOption("private");
  await expect.poll(() => requests.some((url) => url.includes("type=private"))).toBe(true);
});

test("arquivar um canal pede confirmação e explica o impacto", async ({ page }) => {
  await signedIn(page);
  let archived = false;
  await page.route("**/api/admin/channels?**", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          channels: [channel(archived ? { status: "archived" } : {})],
          pagination: { next_cursor: null, has_more: false },
        },
      }),
    }),
  );
  await page.route("**/api/admin/channels/c-eng/status", (route) => {
    archived = true;
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: channel({ status: "archived" }) }),
    });
  });
  await json(page, "**/api/admin/conversations?**", {
    data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
  });

  await page.goto("/channels");
  await page.getByRole("button", { name: "Arquivar" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("histórico e as pessoas do canal são preservados");
  await dialog.getByRole("button", { name: "Arquivar" }).click();

  await expect(page.getByTestId("admin-feedback")).toContainText("O histórico permanece");
});

test("o console não expõe conteúdo de conversas privadas", async ({ page }) => {
  await signedIn(page);
  await json(page, "**/api/admin/channels?**", {
    data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
  });
  await json(page, "**/api/admin/conversations?**", {
    data: {
      conversations: [
        {
          id: "d-1",
          workspace_id: "w1",
          workspace_name: "NChat",
          type: "group",
          status: "active",
          participant_count: 4,
          message_count: 120,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-08-01T10:00:00Z",
          last_activity_at: "2026-08-01T10:00:00Z",
        },
      ],
      pagination: { next_cursor: null, has_more: false },
    },
  });

  await page.goto("/channels");
  const section = page.getByRole("region", { name: "Conversas privadas" });
  await expect(section).toContainText("120");
  // There is no column that could carry content, and no route that returns it.
  await expect(section.getByRole("columnheader", { name: "Participantes" })).toBeVisible();
  await expect(section.getByRole("columnheader", { name: /Mensagem|Título|Prévia/ })).toHaveCount(
    0,
  );
});

test("altera uma política anti-spam com unidade e faixa visíveis", async ({ page }) => {
  await signedIn(page);
  await json(page, "**/api/admin/policies/anti-spam?**", {
    data: {
      policies: [
        {
          workspace: { id: "w1", slug: "default", name: "NChat", status: "active" },
          message_rate_limit_per_minute: 60,
        },
      ],
      bounds: { min: 1, max: 600, default: 60, unit: "messages_per_minute" },
      pagination: { next_cursor: null, has_more: false },
    },
  });
  await json(page, "**/api/admin/policies/anti-spam/w1", {
    data: {
      policy: {
        workspace: { id: "w1", slug: "default", name: "NChat", status: "active" },
        message_rate_limit_per_minute: 30,
      },
      bounds: { min: 1, max: 600, default: 60, unit: "messages_per_minute" },
    },
  });

  await page.goto("/security");
  await expect(page.getByText("msg/min")).toBeVisible();
  await expect(page.getByText(/Entre 1 e 600/)).toBeVisible();

  const input = page.getByLabel("Mensagens por minuto, por usuário");
  await input.fill("0");
  await expect(page.getByRole("alert")).toContainText("entre 1 e 600");
  await expect(page.getByRole("button", { name: "Salvar" })).toBeDisabled();

  await input.fill("30");
  await page.getByRole("button", { name: "Salvar" }).click();
  await expect(page.getByTestId("admin-feedback")).toContainText("Limite salvo: 30");
});

test("o limite de upload recusa um valor fracionário e mostra o que não é editável", async ({
  page,
}) => {
  await signedIn(page);
  await json(page, "**/api/admin/policies/upload?**", {
    data: {
      policies: [
        {
          workspace: { id: "w1", slug: "default", name: "NChat", status: "active" },
          max_upload_bytes: 250 * MIB,
        },
      ],
      bounds: { min: MIB, max: 512 * MIB, default: 250 * MIB, unit: "bytes", step: MIB },
      gateway_hard_cap_bytes: 512 * MIB + 8192,
      deployment_managed: ["malware_scanning", "upload_concurrency"],
      pagination: { next_cursor: null, has_more: false },
    },
  });

  await page.goto("/files");
  await expect(page.getByText("MiB").first()).toBeVisible();
  await expect(page.getByText(/Teto do gateway/)).toBeVisible();
  await expect(page.getByText(/Verificação de malware/)).toBeVisible();

  await page.getByLabel("Tamanho máximo por arquivo").fill("1.5");
  await expect(page.getByRole("alert")).toContainText("inteiros de MiB");
  await expect(page.getByRole("button", { name: "Salvar" })).toBeDisabled();
});

test("uma seção sem capability não é navegável e a API recusa pela URL", async ({ page }) => {
  await signedIn(page, ["admin.config.read"]);
  await json(page, "**/api/admin/users?**", { error: { code: "forbidden", message: "" } }, 403);

  await page.goto("/");
  const nav = page.getByRole("navigation", { name: "Seções administrativas" });
  await expect(nav.getByRole("link", { name: "Usuários" })).toHaveCount(0);

  await page.goto("/users");
  await expect(page.getByRole("alert")).toContainText("não tem permissão");
});

test("conceder papel administrativo exige confirmação explícita", async ({ page }) => {
  await signedIn(page);
  const roleRequests: string[] = [];
  await json(page, "**/api/admin/users?**", {
    data: { users: [user()], pagination: { next_cursor: null, has_more: false } },
  });
  await json(page, "**/api/admin/users/11111111-2222-4333-8444-555555555555", {
    data: {
      ...user(),
      memberships: [],
      channel_count: 0,
      role_grants: [],
      available_roles: [
        {
          slug: "platform-auditor",
          description: "Somente leitura.",
          capabilities: ["admin.audit.read"],
        },
      ],
    },
  });
  await page.route(
    "**/api/admin/users/11111111-2222-4333-8444-555555555555/admin-roles",
    (route) => {
      roleRequests.push(route.request().method());
      return route.fulfill({ status: 204, body: "" });
    },
  );

  await page.goto("/users");
  await page.getByRole("button", { name: "Detalhes" }).click();
  await page.getByRole("button", { name: "Conceder" }).click();

  const dialog = page.getByRole("dialog", { name: "Conceder este papel administrativo?" });
  await expect(dialog).toContainText("admin.audit.read");
  // Nothing has been granted yet.
  expect(roleRequests).toEqual([]);

  await dialog.getByRole("button", { name: "Cancelar" }).click();
  expect(roleRequests).toEqual([]);

  await page.getByRole("button", { name: "Conceder" }).click();
  await page
    .getByRole("dialog", { name: "Conceder este papel administrativo?" })
    .getByRole("button", { name: "Conceder" })
    .click();
  await expect.poll(() => roleRequests).toEqual(["POST"]);
});

test("filtra usuários por papel de workspace, pelo servidor", async ({ page }) => {
  await signedIn(page);
  const requests: string[] = [];
  await page.route("**/api/admin/users?**", (route) => {
    requests.push(route.request().url());
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: { users: [user()], pagination: { next_cursor: null, has_more: false } },
      }),
    });
  });

  await page.goto("/users");
  await expect(page.getByRole("rowheader", { name: /Ana Lima/ })).toBeVisible();

  await page.getByLabel("Papel de workspace").selectOption("owner");
  await expect.poll(() => requests.some((url) => url.includes("workspace_role=owner"))).toBe(true);
});

test("adiciona membro buscando por nome, sem que o operador conheça o identificador", async ({
  page,
}) => {
  await signedIn(page);
  const mutations: { method: string; body: string }[] = [];
  const searches: string[] = [];

  await json(page, "**/api/admin/conversations?**", {
    data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
  });
  await json(page, "**/api/admin/channels?**", {
    data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
  });
  await page.route("**/api/admin/channels/c-eng/member-candidates**", (route) => {
    searches.push(route.request().url());
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          candidates: [
            {
              user_id: "11111111-2222-4333-8444-555555555555",
              display_name: "Ana",
              full_name: "Ana Lima",
              email: "ana@example.test",
              avatar_url: "",
              workspace_role: "member",
            },
          ],
        },
      }),
    });
  });
  await page.route("**/api/admin/channels/c-eng/members**", (route) => {
    mutations.push({ method: route.request().method(), body: route.request().postData() ?? "" });
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          channel_id: "c-eng",
          workspace_id: "w1",
          added: route.request().method() === "POST" ? 1 : 0,
          already_members: 0,
          removed: route.request().method() === "DELETE",
          member_count: 13,
        },
      }),
    });
  });
  await page.route("**/api/admin/channels/c-eng", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          ...channel(),
          category_name: "Times",
          moderators: [],
          workspace_admins: [],
          members: [
            { user_id: "u-zoe", display_name: "Zoe", email: "zoe@example.test", role: "member" },
          ],
          message_count: 0,
        },
      }),
    }),
  );

  await page.goto("/channels");
  await page.getByRole("button", { name: "Detalhes" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("rowheader", { name: "Zoe" })).toBeVisible();

  // The operator types a name. Nothing is sent as a membership yet.
  await dialog.getByLabel("Adicionar membro").fill("Ana");
  await expect.poll(() => searches.some((url) => url.includes("q=Ana"))).toBe(true);
  expect(mutations).toEqual([]);
  await expect(dialog.getByRole("button", { name: "Adicionar" })).toBeDisabled();

  // A person is chosen by their name, never by an identifier.
  await page.getByRole("option", { name: /Ana Lima/ }).click();
  await expect(dialog.getByRole("button", { name: "Adicionar" })).toBeEnabled();
  await dialog.getByRole("button", { name: "Adicionar" }).click();

  await expect(page.getByTestId("admin-membership-feedback")).toContainText("13 membro(s)");
  // human search -> selected user -> identifier in the request.
  expect(mutations).toHaveLength(1);
  expect(mutations[0].method).toBe("POST");
  expect(JSON.parse(mutations[0].body)).toEqual({
    user_ids: ["11111111-2222-4333-8444-555555555555"],
  });

  // Removal still confirms first.
  await dialog.getByRole("button", { name: "Remover" }).click();
  const confirmation = page.getByRole("dialog", { name: "Remover esta pessoa do canal?" });
  await expect(confirmation).toContainText("histórico do canal não são alterados");
  expect(mutations).toHaveLength(1);

  await confirmation.getByRole("button", { name: "Remover" }).click();
  await expect.poll(() => mutations.map((m) => m.method)).toEqual(["POST", "DELETE"]);
});

test("filtra canais por quem administra, buscando a pessoa pelo nome", async ({ page }) => {
  await signedIn(page);
  const channelRequests: string[] = [];
  const peopleSearches: string[] = [];

  await json(page, "**/api/admin/conversations?**", {
    data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
  });
  await page.route("**/api/admin/users?**", (route) => {
    peopleSearches.push(route.request().url());
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: { users: [user()], pagination: { next_cursor: null, has_more: false } },
      }),
    });
  });
  await page.route("**/api/admin/channels?**", (route) => {
    channelRequests.push(route.request().url());
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
      }),
    });
  });

  await page.goto("/channels");
  await expect(page.getByRole("rowheader", { name: /Engenharia/ })).toBeVisible();

  await page.getByLabel("Administrado por").fill("Ana");
  await expect.poll(() => peopleSearches.some((url) => url.includes("q=Ana"))).toBe(true);
  // Partial text is a search, never a filter value — the old field sent it and
  // earned a 400.
  expect(channelRequests.every((url) => !url.includes("administered_by"))).toBe(true);

  await page.getByRole("option", { name: /Ana Lima/ }).click();
  await expect
    .poll(() =>
      channelRequests.some((url) =>
        url.includes("administered_by=11111111-2222-4333-8444-555555555555"),
      ),
    )
    .toBe(true);

  await page.getByRole("button", { name: "Trocar" }).click();
  await expect.poll(() => channelRequests.at(-1)?.includes("administered_by")).toBe(false);
});

test("conta com convite pendente não recebe ação de ativação", async ({ page }) => {
  await signedIn(page);
  await json(page, "**/api/admin/users?**", {
    data: {
      users: [
        user({ status: "invited", full_name: "Ivo Invited" }),
        user({ id: "u-sus", status: "suspended", full_name: "Sara Suspensa" }),
      ],
      pagination: { next_cursor: null, has_more: false },
    },
  });

  await page.goto("/users");
  await expect(page.getByRole("rowheader", { name: /Ivo Invited/ })).toBeVisible();

  // Exactly one "Ativar": the suspended account. The invited one says why it
  // has none instead of offering an operation the API would refuse.
  await expect(page.getByRole("button", { name: "Ativar" })).toHaveCount(1);
  await expect(page.getByText("convite pendente")).toBeVisible();
});

test("sem capability de gestão o console não oferece controle de membership", async ({ page }) => {
  await signedIn(page, ["admin.channels.read", "admin.config.read"]);
  await json(page, "**/api/admin/conversations?**", {
    data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
  });
  await json(page, "**/api/admin/channels?**", {
    data: { channels: [channel()], pagination: { next_cursor: null, has_more: false } },
  });
  await json(page, "**/api/admin/channels/c-eng", {
    data: {
      ...channel(),
      category_name: "Times",
      moderators: [],
      workspace_admins: [],
      members: [
        { user_id: "u-zoe", display_name: "Zoe", email: "zoe@example.test", role: "member" },
      ],
      message_count: 0,
    },
  });

  await page.goto("/channels");
  await page.getByRole("button", { name: "Detalhes" }).click();
  const dialog = page.getByRole("dialog");

  await expect(dialog.getByRole("rowheader", { name: "Zoe" })).toBeVisible();
  await expect(dialog.getByLabel(/Adicionar membro/)).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "Remover" })).toHaveCount(0);
});

test("abre o histórico de auditoria de um usuário a partir do detalhe dele", async ({ page }) => {
  await signedIn(page);
  const auditRequests: string[] = [];

  await json(page, "**/api/admin/users?**", {
    data: { users: [user()], pagination: { next_cursor: null, has_more: false } },
  });
  await json(page, "**/api/admin/users/11111111-2222-4333-8444-555555555555", {
    data: {
      ...user(),
      memberships: [],
      channel_count: 0,
      role_grants: [],
      available_roles: [],
    },
  });
  await page.route("**/api/admin/audit/events**", (route) => {
    const url = route.request().url();
    auditRequests.push(url);
    // The server is what narrows: an unfiltered call answers with somebody
    // else's event, so a page that filtered client-side would show it.
    const filtered = url.includes("user_id=11111111-2222-4333-8444-555555555555");
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          events: filtered
            ? [
                {
                  id: "9",
                  occurred_at: "2026-08-20T10:00:00Z",
                  actor_user_id: "root",
                  actor_email: "root@example.test",
                  action: "admin.user.status.update",
                  resource: "admin.user:11111111-2222-4333-8444-555555555555",
                  result: "success",
                  correlation_id: "req-9",
                },
              ]
            : [
                {
                  id: "1",
                  occurred_at: "2026-08-20T09:00:00Z",
                  actor_user_id: "root",
                  actor_email: "root@example.test",
                  action: "admin.channel.status.update",
                  resource: "admin.channel:outra",
                  result: "success",
                  correlation_id: "req-1",
                },
              ],
        },
      }),
    });
  });

  await page.goto("/users");
  await page.getByRole("button", { name: "Detalhes" }).click();
  await page.getByRole("button", { name: "Ver histórico de auditoria" }).click();

  // Navegou para a auditoria, com o filtro no servidor.
  await expect(page.getByRole("heading", { name: /Auditoria — Ana Lima/ })).toBeVisible();
  await expect
    .poll(() =>
      auditRequests.some((url) => url.includes("user_id=11111111-2222-4333-8444-555555555555")),
    )
    .toBe(true);

  // E mostra o histórico dela, nao o evento global.
  await expect(page.getByText("admin.user.status.update")).toBeVisible();
  await expect(page.getByText("admin.channel.status.update")).toHaveCount(0);

  // Voltar para o global refaz a chamada sem o filtro.
  await page.getByRole("link", { name: "Ver toda a auditoria" }).click();
  await expect(page.getByRole("heading", { name: "Auditoria", exact: true })).toBeVisible();
  await expect(page.getByText("admin.channel.status.update")).toBeVisible();
});

test("canal arquivado bloqueia adicao mas permite remover membro", async ({ page }) => {
  await signedIn(page);
  await json(page, "**/api/admin/conversations?**", {
    data: { conversations: [], pagination: { next_cursor: null, has_more: false } },
  });
  await json(page, "**/api/admin/channels?**", {
    data: {
      channels: [channel({ status: "archived" })],
      pagination: { next_cursor: null, has_more: false },
    },
  });
  await json(page, "**/api/admin/channels/c-eng", {
    data: {
      ...channel({ status: "archived" }),
      category_name: "Times",
      moderators: [],
      workspace_admins: [],
      members: [
        { user_id: "u-zoe", display_name: "Zoe", email: "zoe@example.test", role: "member" },
      ],
      message_count: 0,
    },
  });

  const mutations: string[] = [];
  await page.route("**/api/admin/channels/c-eng/members**", (route) => {
    mutations.push(route.request().method());
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          channel_id: "c-eng",
          workspace_id: "w1",
          added: 0,
          already_members: 0,
          removed: true,
          member_count: 11,
        },
      }),
    });
  });

  await page.goto("/channels");
  await page.getByRole("button", { name: "Detalhes" }).click();
  const dialog = page.getByRole("dialog");

  await expect(dialog.getByRole("rowheader", { name: "Zoe" })).toBeVisible();
  // Adding is refused by the backend on an archived channel, so no control.
  await expect(dialog.getByLabel("Adicionar membro")).toHaveCount(0);
  await expect(dialog.getByText(/Novos membros não podem ser adicionados/)).toBeVisible();

  // Removing is supported, and the console offers it.
  await dialog.getByRole("button", { name: "Remover" }).click();
  const confirmation = page.getByRole("dialog", { name: "Remover esta pessoa do canal?" });
  expect(mutations).toEqual([]);

  await confirmation.getByRole("button", { name: "Remover" }).click();
  await expect.poll(() => mutations).toEqual(["DELETE"]);
  await expect(page.getByTestId("admin-membership-feedback")).toContainText("11 membro(s)");
});
