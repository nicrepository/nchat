import { expect, test, type Page } from "@playwright/test";

/**
 * End-to-end coverage of the configuration screen (issue #580).
 *
 * The Admin API is stubbed at the network boundary, exactly as the other
 * console suites stub theirs: what is under test is the console's own
 * behaviour — the edit, the review, the conflict, the read-only rendering and
 * the rollback — not the server's authorization, which has its own suite in Go.
 *
 * The rule the earlier suites set holds here too: no case below may assert that
 * the frontend is what keeps an unauthorized operator out, and no case may
 * assert that a credential is hidden by the screen. The credential is absent
 * because the API never sent it.
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

const MIN_LENGTH = {
  key: "auth.password.min_length",
  label: "Tamanho mínimo da senha",
  description: "Número mínimo de caracteres exigido ao definir uma senha local.",
  category: "authentication",
  owner_service: "auth-service",
  class: "A",
  source: "database",
  apply: "runtime",
  type: "int",
  unit: "caracteres",
  min: 8,
  max: 128,
  nullable: false,
  default: 12,
  editable: true,
  sensitive: false,
  document: "auth.policy",
  manage_capability: "admin.config.manage",
  danger_note: "Abaixo de 12 caracteres a política fica mais fraca.",
  rollbackable: true,
  observable: true,
  value: 12,
};

const CREDENTIAL = {
  key: "secret.smtp_password",
  label: "SMTP — senha",
  description: "Credencial de envio do relay de e-mail.",
  category: "credentials",
  owner_service: "notification-service",
  class: "D",
  source: "sealed_secret",
  apply: "external",
  type: "string",
  nullable: false,
  editable: false,
  read_only_reason:
    "Credencial em Sealed Secret; a rotação segue docs/runbooks/sealed-secrets-rotation.md.",
  sensitive: true,
  rollbackable: false,
  env_var: "SMTP_PASSWORD",
  observable: true,
  configured: true,
};

const DEPLOYMENT = {
  key: "oidc.enabled",
  label: "Single sign-on habilitado",
  description: "Com false, todos os endpoints OIDC respondem 404.",
  category: "integrations",
  owner_service: "auth-service",
  class: "C",
  source: "gitops",
  apply: "rollout",
  type: "string",
  nullable: false,
  editable: false,
  read_only_reason: "Definido no ConfigMap versionado em Git; alterar exige commit e rollout.",
  sensitive: false,
  rollbackable: false,
  env_var: "OIDC_ENABLED",
  observable: true,
  value: "true",
};

function diffEntry(from: unknown, to: unknown) {
  return {
    key: MIN_LENGTH.key,
    label: MIN_LENGTH.label,
    category: "authentication",
    owner_service: "auth-service",
    apply: "runtime",
    unit: "caracteres",
    dangerous: false,
    from,
    to,
  };
}

function plan(overrides: Record<string, unknown> = {}) {
  return {
    document: "auth.policy",
    revision: 3,
    stale: false,
    superseded: false,
    changes: [diffEntry(12, 16)],
    dangerous: false,
    required_capability: "admin.config.manage",
    authorized: true,
    reason_required: false,
    warnings: [],
    errors: [],
    affected_services: ["auth-service"],
    apply: "runtime",
    ...overrides,
  };
}

const VERSION = {
  id: "7",
  document: "auth.policy",
  revision: 3,
  applied_at: "2026-08-20T12:00:00Z",
  actor_user_id: SUPERUSER.identity.user_id,
  actor_email: "admin@example.test",
  correlation_id: "req-e2e",
  reason: "endurecimento inicial",
  reverts_revision: 0,
  rollbackable: true,
  changes: [diffEntry(8, 12)],
};

async function json(page: Page, pattern: string, body: unknown, status = 200) {
  await page.route(pattern, (route) =>
    route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) }),
  );
}

async function signedIn(page: Page, capabilities: string[] = ["admin.superuser"]) {
  await json(page, "**/api/admin/bootstrap", { data: { ...SUPERUSER, capabilities } });
}

async function catalogue(page: Page, revision = 3, value: number = 12) {
  await json(page, "**/api/admin/config", {
    data: {
      documents: [{ key: "auth.policy", revision }],
      settings: [{ ...MIN_LENGTH, value }, CREDENTIAL, DEPLOYMENT],
    },
  });
}

async function history(page: Page, versions: unknown[] = [VERSION]) {
  await json(page, "**/api/admin/config/versions?**", { data: { versions } });
}

test("edita uma configuração, revisa o diff do servidor e aplica", async ({ page }) => {
  await signedIn(page);
  await history(page);
  await catalogue(page);
  await json(page, "**/api/admin/config/preview", { data: { plan: plan() } });

  const applied: string[] = [];
  await page.route("**/api/admin/config/apply", (route) => {
    applied.push(route.request().postData() ?? "");
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          applied: true,
          document: "auth.policy",
          revision: 4,
          values: { "auth.password.min_length": 16 },
          plan: plan(),
          version: { ...VERSION, id: "8", revision: 4 },
        },
      }),
    });
  });

  await page.goto("/configuration");
  const field = page.getByLabel("Tamanho mínimo da senha");
  await expect(field).toHaveValue("12");

  await field.fill("16");
  await expect(page.getByTestId("config-dirty")).toBeVisible();

  await page.getByRole("button", { name: "Revisar alterações" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("config-diff")).toContainText("− 12 caracteres");
  await expect(dialog.getByTestId("config-diff")).toContainText("+ 16 caracteres");
  // Reviewing is not applying.
  expect(applied).toHaveLength(0);

  await dialog.getByRole("button", { name: "Aplicar" }).click();

  await expect(page.getByTestId("config-feedback")).toContainText("Revisão 4");
  expect(JSON.parse(applied[0])).toMatchObject({
    document: "auth.policy",
    expected_revision: 3,
    changes: { "auth.password.min_length": 16 },
  });
});

test("recusa aplicar quando a configuração mudou desde o carregamento", async ({ page }) => {
  await signedIn(page);
  await history(page);
  await catalogue(page);
  await json(page, "**/api/admin/config/preview", {
    data: { plan: plan({ stale: true, revision: 9 }) },
  });

  let applies = 0;
  await page.route("**/api/admin/config/apply", (route) => {
    applies += 1;
    return route.fulfill({ status: 409, contentType: "application/json", body: "{}" });
  });

  await page.goto("/configuration");
  await page.getByLabel("Tamanho mínimo da senha").fill("16");
  await page.getByRole("button", { name: "Revisar alterações" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("config-conflict")).toContainText("revisão atual: 9");
  await expect(dialog.getByRole("button", { name: "Aplicar" })).toBeDisabled();
  expect(applies).toBe(0);
});

test("exige um motivo antes de confirmar uma alteração perigosa", async ({ page }) => {
  await signedIn(page);
  await history(page);
  await catalogue(page);
  await json(page, "**/api/admin/config/preview", {
    data: {
      plan: plan({
        dangerous: true,
        reason_required: true,
        required_capability: "admin.superuser",
        warnings: ["Abaixo de 12 caracteres a política fica mais fraca."],
        changes: [{ ...diffEntry(12, 8), dangerous: true, danger_note: "Política mais fraca." }],
      }),
    },
  });

  await page.goto("/configuration");
  await page.getByLabel("Tamanho mínimo da senha").fill("8");
  await page.getByRole("button", { name: "Revisar alterações" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId(`config-danger-${MIN_LENGTH.key}`)).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Aplicar" })).toBeDisabled();

  await dialog.getByLabel(/Motivo/).fill("aprovado no chamado SEC-77");
  await expect(dialog.getByRole("button", { name: "Aplicar" })).toBeEnabled();
});

test("mostra uma credencial como configurada, sem valor e sem campo", async ({ page }) => {
  await signedIn(page);
  await history(page);
  await catalogue(page);

  await page.goto("/configuration");
  await page.getByText(/^Credenciais \(/).click();

  const field = page.getByTestId(`config-${CREDENTIAL.key}`);
  await expect(field.getByTestId(`config-status-${CREDENTIAL.key}`)).toContainText("Configurado");
  await expect(field.getByRole("textbox")).toHaveCount(0);
  await expect(field).toContainText("sealed-secrets-rotation");
});

test("mostra uma configuração de GitOps como somente leitura e explica o motivo", async ({
  page,
}) => {
  await signedIn(page);
  await history(page);
  await catalogue(page);

  await page.goto("/configuration");
  await page.getByText(/^Integrações \(/).click();

  const field = page.getByTestId(`config-${DEPLOYMENT.key}`);
  await expect(field).toContainText("Exige rollout");
  await expect(field).toContainText("commit e rollout");
  await expect(field.getByRole("textbox")).toHaveCount(0);
});

test("reverte uma versão pela mesma revisão do servidor", async ({ page }) => {
  await signedIn(page);
  await history(page);
  await catalogue(page);
  await json(page, "**/api/admin/config/versions/7/rollback/preview", {
    data: { plan: plan({ changes: [diffEntry(12, 8)] }) },
  });

  const rollbacks: string[] = [];
  await page.route("**/api/admin/config/versions/7/rollback", (route) => {
    rollbacks.push(route.request().postData() ?? "");
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          applied: true,
          document: "auth.policy",
          revision: 4,
          values: { "auth.password.min_length": 8 },
          plan: plan({ changes: [diffEntry(12, 8)] }),
          version: { ...VERSION, id: "9", revision: 4, reverts_revision: 3 },
        },
      }),
    });
  });

  await page.goto("/configuration");
  const versions = page.getByTestId("config-versions");
  await expect(versions).toContainText("Revisão 3");

  await versions.getByRole("button", { name: "Reverter" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("config-diff")).toContainText("+ 8 caracteres");
  await dialog.getByRole("button", { name: "Reverter" }).click();

  await expect(page.getByTestId("config-feedback")).toContainText("Revisão 4");
  expect(JSON.parse(rollbacks[0])).toMatchObject({ expected_revision: 3 });
});

test("sem a permissão de escrita a tela fica somente leitura", async ({ page }) => {
  await signedIn(page, ["admin.config.read"]);
  await history(page);
  await catalogue(page);

  await page.goto("/configuration");

  await expect(page.getByTestId(`config-${MIN_LENGTH.key}`)).toContainText("12 caracteres");
  await expect(page.getByRole("button", { name: "Revisar alterações" })).toHaveCount(0);
  await expect(page.getByText("admin.config.manage").first()).toBeVisible();
});

test("uma versão superada é explicada e não pode ser confirmada", async ({ page }) => {
  await signedIn(page);
  await history(page);
  await catalogue(page);
  // The server derives the verdict from the recorded version and the current
  // state; the console only names which version.
  await json(page, "**/api/admin/config/versions/7/rollback/preview", {
    data: { plan: plan({ superseded: true, changes: [diffEntry(20, 12)] }) },
  });

  const rollbacks: string[] = [];
  await page.route("**/api/admin/config/versions/7/rollback", (route) => {
    rollbacks.push(route.request().url());
    return route.fulfill({ status: 409, contentType: "application/json", body: "{}" });
  });

  await page.goto("/configuration");
  const versions = page.getByTestId("config-versions");
  await versions.getByRole("button", { name: "Reverter" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("config-superseded")).toContainText(
    "não pode mais ser revertida",
  );
  await expect(dialog.getByRole("button", { name: "Reverter" })).toBeDisabled();
  // The operator never reaches the mutation.
  expect(rollbacks).toHaveLength(0);
});
