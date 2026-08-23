import { describe, expect, it } from "vitest";

import type { ConfigSetting } from "../api/configApi";
import {
  applyLabel,
  categoryLabel,
  changedValues,
  classLabel,
  draftFrom,
  draftsFrom,
  formatConfigValue,
  groupByCategory,
  hasInvalidDraft,
  isDirty,
  sourceLabel,
  toConfigValue,
  validateDraft,
} from "./configFields";

function setting(overrides: Partial<ConfigSetting> = {}): ConfigSetting {
  return {
    key: "auth.password.min_length",
    label: "Tamanho mínimo da senha",
    description: "",
    category: "authentication",
    ownerService: "auth-service",
    configClass: "A",
    source: "database",
    apply: "runtime",
    type: "int",
    unit: "caracteres",
    min: 8,
    max: 128,
    nullable: false,
    default: 12,
    editable: true,
    readOnlyReason: "",
    sensitive: false,
    document: "auth.policy",
    manageCapability: "admin.config.manage",
    dangerNote: "",
    rollbackable: true,
    envVar: "",
    observable: true,
    value: 12,
    configured: undefined,
    ...overrides,
  };
}

describe("toConfigValue", () => {
  it("reads a plain decimal integer and nothing else", () => {
    expect(toConfigValue(setting(), "14")).toEqual({ value: 14 });
    expect(toConfigValue(setting(), " 14 ")).toEqual({ value: 14 });
    for (const draft of ["14.0", "1e2", "14px", "-", "", "0x10"]) {
      expect(toConfigValue(setting(), draft)).toBeNull();
    }
  });

  // A blank nullable field is an explicit absence; a blank required one is not
  // a value, and must never be submitted as zero.
  it("keeps a blank nullable field apart from a blank required one", () => {
    expect(toConfigValue(setting({ nullable: true }), "")).toEqual({ value: null });
    expect(toConfigValue(setting({ nullable: false }), "")).toBeNull();
  });

  it("reads a boolean only from its two spellings", () => {
    const flag = setting({ type: "bool", value: true });
    expect(toConfigValue(flag, "true")).toEqual({ value: true });
    expect(toConfigValue(flag, "false")).toEqual({ value: false });
    expect(toConfigValue(flag, "1")).toBeNull();
    expect(toConfigValue(flag, "")).toBeNull();
  });
});

describe("validateDraft", () => {
  it("reports the bound that was crossed", () => {
    expect(validateDraft(setting(), "7")).toBe("O mínimo aceito é 8.");
    expect(validateDraft(setting(), "129")).toBe("O máximo aceito é 128.");
    expect(validateDraft(setting(), "12")).toBeNull();
  });

  it("reports an unreadable draft as one", () => {
    expect(validateDraft(setting(), "doze")).toBe("Informe um número inteiro.");
  });

  it("accepts a blank nullable field", () => {
    expect(validateDraft(setting({ nullable: true }), "")).toBeNull();
  });
});

describe("changedValues", () => {
  const settings = [
    setting(),
    setting({ key: "auth.password.require_symbol", type: "bool", value: true, unit: "" }),
    setting({ key: "oidc.enabled", editable: false, value: "true" }),
  ];

  it("sends only the fields that actually differ", () => {
    const drafts = draftsFrom(settings);
    expect(changedValues(settings, drafts)).toEqual({});

    drafts["auth.password.min_length"] = "16";
    drafts["auth.password.require_symbol"] = "true";
    expect(changedValues(settings, drafts)).toEqual({ "auth.password.min_length": 16 });
  });

  it("never sends a field the server declared read-only", () => {
    const drafts = { ...draftsFrom(settings), "oidc.enabled": "false" };
    expect(changedValues(settings, drafts)).toEqual({});
  });

  it("drops a draft that is not a value at all", () => {
    const drafts = { ...draftsFrom(settings), "auth.password.min_length": "doze" };
    expect(changedValues(settings, drafts)).toEqual({});
    expect(hasInvalidDraft(settings, drafts)).toBe(true);
    expect(isDirty(settings, drafts)).toBe(true);
  });
});

describe("draftFrom", () => {
  it("renders an unobservable or absent value as an empty field", () => {
    expect(draftFrom(setting({ value: undefined }))).toBe("");
    expect(draftFrom(setting({ value: null, nullable: true }))).toBe("");
    expect(draftFrom(setting({ type: "bool", value: false }))).toBe("false");
  });
});

describe("formatConfigValue", () => {
  // Three different sentences for three different facts. Collapsing any pair
  // would send an operator to fix something that is not broken.
  it("distinguishes unobservable, unset and empty", () => {
    expect(formatConfigValue(undefined)).toBe("Não observável neste serviço");
    expect(formatConfigValue(null)).toBe("Não definido");
    expect(formatConfigValue("")).toBe("Vazio");
  });

  it("renders booleans and units for reading", () => {
    expect(formatConfigValue(true)).toBe("Sim");
    expect(formatConfigValue(false)).toBe("Não");
    expect(formatConfigValue(12, "caracteres")).toBe("12 caracteres");
    expect(formatConfigValue(12)).toBe("12");
  });
});

describe("labels", () => {
  it("translates the values the server sends and passes unknown ones through", () => {
    expect(applyLabel("rollout")).toBe("Exige rollout");
    expect(sourceLabel("sealed_secret")).toBe("Sealed Secret");
    expect(classLabel("A")).toBe("Classe A · runtime");
    expect(categoryLabel("credentials")).toBe("Credenciais");
    expect(applyLabel("something-new")).toBe("something-new");
    expect(classLabel("Z")).toBe("Classe Z");
    expect(categoryLabel("novo")).toBe("novo");
    expect(sourceLabel("novo")).toBe("novo");
  });
});

describe("groupByCategory", () => {
  it("keeps the order the server sent", () => {
    const grouped = groupByCategory([
      setting({ key: "a", category: "platform" }),
      setting({ key: "b", category: "credentials" }),
      setting({ key: "c", category: "platform" }),
    ]);
    expect(grouped.map(([category, entries]) => [category, entries.length])).toEqual([
      ["platform", 2],
      ["credentials", 1],
    ]);
  });
});
