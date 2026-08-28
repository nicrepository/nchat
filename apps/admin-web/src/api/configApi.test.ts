import { afterEach, describe, expect, it, vi } from "vitest";

import { AdminApiError, setCSRFToken, _resetCSRFToken } from "./client";
import {
  applyConfiguration,
  listConfigVersions,
  loadConfiguration,
  previewConfigRollback,
  previewConfiguration,
  rollbackConfigVersion,
} from "./configApi";
import { ERR_INVALID_RESPONSE } from "./parse";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function stub(body: unknown, status = 200) {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse(body, status));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const EDITABLE = {
  key: "auth.password.min_length",
  label: "Tamanho mínimo da senha",
  description: "Número mínimo de caracteres.",
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
  danger_note: "Abaixo de 12 fica mais fraca.",
  rollbackable: true,
  observable: true,
  value: 14,
};

const CREDENTIAL = {
  key: "secret.smtp_password",
  label: "SMTP — senha",
  description: "Credencial de envio.",
  category: "credentials",
  owner_service: "notification-service",
  class: "D",
  source: "sealed_secret",
  apply: "external",
  type: "string",
  nullable: false,
  editable: false,
  read_only_reason: "Credencial em Sealed Secret.",
  sensitive: true,
  rollbackable: false,
  env_var: "SMTP_PASSWORD",
  observable: true,
  configured: true,
};

const NULLABLE = {
  ...EDITABLE,
  key: "auth.password.expiration_days",
  label: "Expiração de senha",
  unit: "dias",
  nullable: true,
  default: null,
  value: null,
};

function catalog(settings: unknown[]) {
  return { data: { documents: [{ key: "auth.policy", revision: 3 }], settings } };
}

const PLAN = {
  document: "auth.policy",
  revision: 3,
  stale: false,
  superseded: false,
  changes: [
    {
      key: "auth.password.min_length",
      label: "Tamanho mínimo da senha",
      category: "authentication",
      owner_service: "auth-service",
      apply: "runtime",
      unit: "caracteres",
      dangerous: false,
      from: 12,
      to: 14,
    },
  ],
  dangerous: false,
  required_capability: "admin.config.manage",
  authorized: true,
  reason_required: false,
  warnings: [],
  errors: [],
  affected_services: ["auth-service"],
  apply: "runtime",
};

const VERSION = {
  id: "7",
  document: "auth.policy",
  revision: 4,
  applied_at: "2026-08-20T12:00:00Z",
  actor_user_id: "11111111-1111-1111-1111-111111111111",
  actor_email: "admin@example.test",
  correlation_id: "req-1",
  reason: "",
  reverts_revision: 0,
  rollbackable: true,
  changes: PLAN.changes,
};

afterEach(() => {
  vi.unstubAllGlobals();
  _resetCSRFToken();
});

describe("loadConfiguration", () => {
  it("reads a setting with its class, source and range", async () => {
    stub(catalog([EDITABLE]));

    const result = await loadConfiguration();

    expect(result.documents).toEqual([{ key: "auth.policy", revision: 3 }]);
    expect(result.settings[0]).toMatchObject({
      key: "auth.password.min_length",
      configClass: "A",
      source: "database",
      apply: "runtime",
      min: 8,
      max: 128,
      value: 14,
      editable: true,
      sensitive: false,
    });
  });

  // The read-path invariant, checked at the boundary rather than assumed: a
  // credential arrives as a status and never as a value.
  it("reads a credential as configured, with no value at all", async () => {
    stub(catalog([CREDENTIAL]));

    const [setting] = (await loadConfiguration()).settings;

    expect(setting.sensitive).toBe(true);
    expect(setting.configured).toBe(true);
    expect(setting.value).toBeUndefined();
    expect(setting.editable).toBe(false);
  });

  // A server that started returning credential values is a contract change the
  // console must fail on, not render.
  it("refuses a credential that arrives carrying a value", async () => {
    stub(catalog([{ ...CREDENTIAL, value: "hunter2" }]));

    await expect(loadConfiguration()).rejects.toMatchObject({ code: ERR_INVALID_RESPONSE });
  });

  it("refuses a credential the server marks editable", async () => {
    stub(catalog([{ ...CREDENTIAL, editable: true }]));

    await expect(loadConfiguration()).rejects.toMatchObject({ code: ERR_INVALID_RESPONSE });
  });

  // Absent and null are different facts: one is a setting this deployment
  // cannot observe, the other is one that is observably unset.
  it("keeps an absent value apart from an explicit null", async () => {
    stub(catalog([NULLABLE, { ...EDITABLE, key: "auth.device.max_per_user", observable: false }]));

    const [nullable, unobservable] = (await loadConfiguration()).settings;

    expect(nullable.value).toBeNull();
    expect(nullable.nullable).toBe(true);
    expect(unobservable.value).toBe(14);
    expect(unobservable.observable).toBe(false);
  });

  it("refuses an unknown source, apply mode or type", async () => {
    for (const override of [{ source: "kubernetes" }, { apply: "magic" }, { type: "float" }]) {
      stub(catalog([{ ...EDITABLE, ...override }]));
      await expect(loadConfiguration()).rejects.toMatchObject({ code: ERR_INVALID_RESPONSE });
    }
  });

  it("refuses a payload that is not the promised shape", async () => {
    stub({ data: { documents: [], settings: {} } });
    await expect(loadConfiguration()).rejects.toBeInstanceOf(AdminApiError);
  });
});

describe("previewConfiguration", () => {
  it("sends the document, the revision and the raw values", async () => {
    const fetchMock = stub({ data: { plan: PLAN } });
    setCSRFToken("csrf-1");

    const plan = await previewConfiguration({
      document: "auth.policy",
      expectedRevision: 3,
      changes: { "auth.password.min_length": 14, "auth.password.require_symbol": false },
    });

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/admin/config/preview");
    expect(init.method).toBe("POST");
    expect(init.headers.get("X-NChat-Admin-CSRF")).toBe("csrf-1");
    expect(JSON.parse(init.body)).toEqual({
      document: "auth.policy",
      expected_revision: 3,
      reason: "",
      changes: { "auth.password.min_length": 14, "auth.password.require_symbol": false },
    });
    expect(plan.changes[0]).toMatchObject({ from: 12, to: 14, dangerous: false });
    expect(plan.affectedServices).toEqual(["auth-service"]);
  });

  // The Go/TypeScript divergence that let the whole problem through: the
  // backend published `superseded` and the parser dropped it, so the console
  // could not have rendered it even if it had wanted to.
  it("carries superseded through the parser, both ways", async () => {
    for (const superseded of [true, false]) {
      stub({ data: { plan: { ...PLAN, superseded } } });

      const plan = await previewConfiguration({
        document: "auth.policy",
        expectedRevision: 3,
        changes: { "auth.password.min_length": 14 },
      });

      expect(plan.superseded).toBe(superseded);
    }
  });

  // Read with the same rigor as every other field of the plan: a payload that
  // omits it, or sends it as something else, is a contract mismatch and must
  // surface here rather than as a rollback that silently looks applicable.
  it("refuses a plan whose superseded field is missing or not a boolean", async () => {
    const withoutSuperseded: Record<string, unknown> = { ...PLAN };
    delete withoutSuperseded.superseded;
    for (const plan of [withoutSuperseded, { ...PLAN, superseded: "true" }]) {
      stub({ data: { plan } });
      await expect(
        previewConfiguration({
          document: "auth.policy",
          expectedRevision: 3,
          changes: { "auth.password.min_length": 14 },
        }),
      ).rejects.toMatchObject({ code: ERR_INVALID_RESPONSE });
    }
  });

  it("reads the server's validation failures and the capability it demands", async () => {
    stub({
      data: {
        plan: {
          ...PLAN,
          changes: [],
          dangerous: true,
          authorized: false,
          reason_required: true,
          required_capability: "admin.superuser",
          warnings: ["Enfraquece a autenticação."],
          errors: [{ key: "auth.password.min_length", message: "mínimo 8" }],
        },
      },
    });

    const plan = await previewConfiguration({
      document: "auth.policy",
      expectedRevision: 3,
      changes: { "auth.password.min_length": 2 },
    });

    expect(plan.authorized).toBe(false);
    expect(plan.requiredCapability).toBe("admin.superuser");
    expect(plan.reasonRequired).toBe(true);
    expect(plan.errors).toEqual([{ key: "auth.password.min_length", message: "mínimo 8" }]);
    expect(plan.warnings).toHaveLength(1);
  });
});

describe("applyConfiguration", () => {
  it("returns the applied revision and the stored values", async () => {
    const fetchMock = stub({
      data: {
        applied: true,
        document: "auth.policy",
        revision: 4,
        values: { "auth.password.min_length": 14, "auth.password.expiration_days": null },
        plan: PLAN,
        version: VERSION,
      },
    });

    const result = await applyConfiguration({
      document: "auth.policy",
      expectedRevision: 3,
      reason: "endurecer política",
      changes: { "auth.password.min_length": 14 },
    });

    expect(fetchMock.mock.calls[0][0]).toBe("/api/admin/config/apply");
    expect(JSON.parse(fetchMock.mock.calls[0][1].body).reason).toBe("endurecer política");
    expect(result.applied).toBe(true);
    expect(result.revision).toBe(4);
    expect(result.values["auth.password.expiration_days"]).toBeNull();
    expect(result.version?.id).toBe("7");
  });

  it("reports the no-op case as applied false rather than as a failure", async () => {
    stub({
      data: {
        applied: false,
        document: "auth.policy",
        revision: 3,
        values: {},
        plan: { ...PLAN, changes: [] },
      },
    });

    const result = await applyConfiguration({
      document: "auth.policy",
      expectedRevision: 3,
      changes: { "auth.password.min_length": 12 },
    });

    expect(result.applied).toBe(false);
    expect(result.version).toBeNull();
  });

  it("surfaces a lost race as a conflict", async () => {
    stub({ error: { code: "conflict", message: "conflicting state" } }, 409);

    await expect(
      applyConfiguration({
        document: "auth.policy",
        expectedRevision: 3,
        changes: { "auth.password.min_length": 14 },
      }),
    ).rejects.toMatchObject({ status: 409, code: "conflict" });
  });
});

describe("listConfigVersions", () => {
  it("asks for one document and reads the recorded changes", async () => {
    const fetchMock = stub({ data: { versions: [VERSION] } });

    const versions = await listConfigVersions("auth.policy", 25);

    expect(fetchMock.mock.calls[0][0]).toBe(
      "/api/admin/config/versions?document=auth.policy&limit=25",
    );
    expect(versions[0]).toMatchObject({ id: "7", revision: 4, rollbackable: true });
    expect(versions[0].changes[0]).toMatchObject({ from: 12, to: 14 });
  });

  it("tolerates a version whose actor was deleted", async () => {
    stub({ data: { versions: [{ ...VERSION, actor_user_id: null, actor_email: null }] } });

    const versions = await listConfigVersions("auth.policy", 25);

    expect(versions[0].actorUserId).toBe("");
    expect(versions[0].actorEmail).toBe("");
  });
});

describe("previewConfigRollback", () => {
  // The client names the version and the revision it holds. It does not send
  // the values to restore, the preconditions or a verdict: those are the
  // server's to derive, and sending them would make the console's rendered
  // history the authority for an administrative mutation.
  it("asks the version's own preview route with nothing but the revision", async () => {
    const fetchMock = stub({ data: { plan: { ...PLAN, superseded: true } } });
    setCSRFToken("csrf-1");

    const plan = await previewConfigRollback("7", 4);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/admin/config/versions/7/rollback/preview");
    expect(init.method).toBe("POST");
    expect(init.headers.get("X-NChat-Admin-CSRF")).toBe("csrf-1");
    expect(JSON.parse(init.body)).toEqual({ expected_revision: 4 });
    expect(plan.superseded).toBe(true);
  });

  it("escapes the version identifier in the path", async () => {
    const fetchMock = stub({ data: { plan: PLAN } });

    await previewConfigRollback("7/../9", 4);

    expect(String(fetchMock.mock.calls[0][0])).toBe(
      "/api/admin/config/versions/7%2F..%2F9/rollback/preview",
    );
  });
});

describe("rollbackConfigVersion", () => {
  it("posts to the version's own route with the expected revision", async () => {
    const fetchMock = stub({
      data: {
        applied: true,
        document: "auth.policy",
        revision: 5,
        values: {},
        plan: PLAN,
        version: { ...VERSION, id: "8", revision: 5, reverts_revision: 4 },
      },
    });

    const result = await rollbackConfigVersion("7", 4, "reverter");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/admin/config/versions/7/rollback");
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      expected_revision: 4,
      reason: "reverter",
    });
    expect(result.version?.revertsRevision).toBe(4);
  });
});
