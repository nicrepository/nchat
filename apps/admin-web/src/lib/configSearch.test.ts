import { describe, expect, it } from "vitest";

import type { ConfigSetting } from "../api/configApi";
import { indexOf, normalize, searchSettings } from "./configSearch";

function setting(overrides: Partial<ConfigSetting> = {}): ConfigSetting {
  return {
    key: "oidc.enabled",
    label: "Single sign-on habilitado",
    description: "Com false, todos os endpoints OIDC respondem 404.",
    category: "integrations",
    ownerService: "auth-service",
    configClass: "C",
    source: "gitops",
    apply: "rollout",
    type: "string",
    unit: "",
    min: null,
    max: null,
    nullable: false,
    default: undefined,
    editable: false,
    readOnlyReason: "ConfigMap",
    sensitive: false,
    document: "",
    manageCapability: "",
    dangerNote: "",
    rollbackable: false,
    envVar: "OIDC_ENABLED",
    observable: true,
    value: "true",
    configured: undefined,
    ...overrides,
  };
}

describe("configSearch", () => {
  // The rule the whole module exists for. A search that matched values would
  // confirm a guess: type a suspected token and a hit tells you it is right.
  it("never indexes a value or a credential status", () => {
    const credential = setting({
      key: "secret.smtp_password",
      label: "SMTP — senha",
      description: "Credencial de envio do relay.",
      sensitive: true,
      value: undefined,
      configured: true,
      envVar: "SMTP_PASSWORD",
    });
    const index = indexOf(credential);

    expect(index).not.toContain("true");
    expect(index).not.toContain("configured");
    expect(indexOf(setting({ value: "super-secret-value" }))).not.toContain("super-secret-value");
  });

  it("matches on the metadata an operator would actually type", () => {
    const settings = [
      setting(),
      setting({
        key: "email.smtp.worker_enabled",
        label: "Envio de e-mail habilitado",
        description: "Com false, a fila de e-mail acumula.",
        ownerService: "notification-service",
        envVar: "SMTP_WORKER_ENABLED",
      }),
    ];
    for (const term of ["single sign", "OIDC_ENABLED", "oidc.enabled", "auth-service"]) {
      expect(searchSettings(settings, term)).toHaveLength(1);
    }
  });

  it("folds accents so a term typed without them still finds the field", () => {
    const settings = [setting({ label: "Configuração de sessão" })];
    expect(searchSettings(settings, "configuracao")).toHaveLength(1);
    expect(searchSettings(settings, "SESSÃO")).toHaveLength(1);
  });

  it("requires every word, in any order", () => {
    const settings = [
      setting({ key: "secret.smtp_password", label: "SMTP — senha", envVar: "SMTP_PASSWORD" }),
      setting({
        key: "email.smtp.worker_enabled",
        label: "Envio de e-mail habilitado",
        description: "Com false, a fila acumula.",
        ownerService: "notification-service",
        envVar: "SMTP_WORKER_ENABLED",
      }),
    ];
    expect(searchSettings(settings, "smtp senha")).toHaveLength(1);
    expect(searchSettings(settings, "senha smtp")).toHaveLength(1);
    expect(searchSettings(settings, "smtp inexistente")).toHaveLength(0);
  });

  it("returns everything for an empty term, in the order it was given", () => {
    const settings = [setting({ key: "b.key" }), setting({ key: "a.key" })];
    for (const term of ["", "   ", "\t"]) {
      expect(searchSettings(settings, term).map((entry) => entry.key)).toEqual(["b.key", "a.key"]);
    }
  });

  // Deterministic: the same term twice is the same list, so a result can be
  // quoted in a ticket and found again.
  it("is deterministic", () => {
    const settings = [setting(), setting({ key: "oidc.scopes", label: "Escopos OIDC" })];
    expect(searchSettings(settings, "oidc")).toEqual(searchSettings(settings, "oidc"));
  });

  it("normalizes for comparison without altering the term shown", () => {
    expect(normalize("  Configuração  ")).toBe("configuracao");
  });
});
