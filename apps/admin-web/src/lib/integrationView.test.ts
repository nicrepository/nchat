import { describe, expect, it } from "vitest";

import type { Integration, IntegrationSetting } from "../api/integrationsApi";
import {
  canDiagnose,
  passiveSummary,
  presentDiagnostic,
  splitSettings,
  stageLabel,
} from "./integrationView";

function integration(overrides: Partial<Integration> = {}): Integration {
  return {
    id: "oidc",
    displayName: "Keycloak / OIDC",
    summary: "Single sign-on da plataforma.",
    category: "identity",
    runbookPath: "docs/runbooks/task-auth-oidc-keycloak.md",
    healthServiceId: "oidc",
    state: "healthy",
    enabled: true,
    observable: true,
    latencyMS: 120,
    checkedAt: "2026-08-23T11:00:00.000Z",
    errorCategory: "",
    detail: "Tudo certo.",
    version: "",
    diagnosable: true,
    diagnosticUnsupported: "",
    stages: ["resolve"],
    settingsVisible: true,
    settings: [],
    actions: [],
    ...overrides,
  };
}

describe("integrationView", () => {
  // Every status carries a word and a shape as well as a colour, so the answer
  // survives a monochrome screen and a screenshot in a ticket.
  it("describes every diagnostic status without relying on colour", () => {
    for (const status of ["passed", "warning", "failed", "skipped"] as const) {
      const presentation = presentDiagnostic(status);
      expect(presentation.label).not.toBe("");
      expect(presentation.mark).not.toBe("");
    }
    const marks = (["passed", "warning", "failed", "skipped"] as const).map(
      (status) => presentDiagnostic(status).mark,
    );
    expect(new Set(marks).size).toBe(marks.length);
  });

  // A status this build does not know must degrade into honesty, never into a
  // pass.
  it("never reads an unknown status as a pass", () => {
    const presentation = presentDiagnostic("what" as never);
    expect(presentation.tone).toBe("neutral");
    expect(presentation.label).toBe("Não executada");
  });

  it("names every declared stage and shows an unknown one as it arrived", () => {
    expect(stageLabel("jwks")).toBe("Conjunto de chaves (JWKS)");
    expect(stageLabel("delivery")).toBe("Entrega da mensagem");
    expect(stageLabel("brand-new")).toBe("brand-new");
  });

  it("keeps advanced settings apart, in the order the server sent", () => {
    const base = { advanced: false } as IntegrationSetting;
    const settings = [
      { ...base, key: "a" },
      { ...base, key: "b", advanced: true },
      { ...base, key: "c" },
    ] as IntegrationSetting[];
    const { common, advanced } = splitSettings(settings);
    expect(common.map((entry) => entry.key)).toEqual(["a", "c"]);
    expect(advanced.map((entry) => entry.key)).toEqual(["b"]);
  });

  // Switched off, invisible-from-here and simply healthy lead to three
  // different actions, so they get three different sentences.
  it("distinguishes disabled from unobservable from a real diagnosis", () => {
    expect(passiveSummary(integration({ enabled: false }))).toContain("desligada");
    expect(passiveSummary(integration({ observable: false }))).toContain("desconhecido");
    expect(passiveSummary(integration())).toBe("Tudo certo.");
  });

  it("offers the diagnostic only with the manage capability and an adapter", () => {
    const manage = (capability: string) => capability === "admin.integrations.manage";
    const readOnly = (capability: string) => capability === "admin.integrations.read";
    expect(canDiagnose(integration(), manage)).toBe(true);
    expect(canDiagnose(integration(), readOnly)).toBe(false);
    expect(canDiagnose(integration({ diagnosable: false }), manage)).toBe(false);
  });
});
