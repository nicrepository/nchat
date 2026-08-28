/**
 * Presentation rules for the integrations screen (issue #582).
 *
 * Pure functions, deliberately out of the components, because they are the part
 * that has to be *correct* rather than merely rendered: a stage that did not run
 * must never read as one that passed, a diagnostic must be describable without
 * colour, and the order of the stages an operator reads must not depend on the
 * order they happened to finish in.
 */

import type { DiagnosticStatus, Integration, IntegrationSetting } from "../api/integrationsApi";

/**
 * How each diagnostic stage is named for an operator.
 *
 * Functional words rather than protocol jargon where the two differ: "DNS" is
 * what an operator greps for, "credencial" is what they can act on. An
 * unrecognised stage is shown as it arrived — the server only sends values from
 * a closed set, so seeing a raw one means the two ends disagree, and hiding
 * that would leave a blank row.
 */
const STAGE_LABELS: Record<string, string> = {
  resolve: "DNS",
  connect: "Conexão TCP",
  tls: "TLS",
  discovery: "Discovery OIDC",
  issuer: "Issuer",
  jwks: "Conjunto de chaves (JWKS)",
  credential: "Autenticação",
  ready: "Serviço pronto",
  delivery: "Entrega da mensagem",
};

export function stageLabel(stage: string): string {
  return STAGE_LABELS[stage] ?? stage;
}

/**
 * How each stage result is described.
 *
 * `mark` is the non-colour carrier of the same information, exactly as the
 * Health Center's badges do it: an operator on a monochrome screen, with a
 * colour vision deficiency, or reading a screenshot pasted into a ticket gets
 * the same answer as everyone else.
 */
export interface DiagnosticPresentation {
  label: string;
  mark: string;
  tone: "ok" | "warn" | "danger" | "neutral";
}

const DIAGNOSTIC_PRESENTATION: Record<DiagnosticStatus, DiagnosticPresentation> = {
  passed: { label: "OK", mark: "✓", tone: "ok" },
  warning: { label: "Atenção", mark: "▲", tone: "warn" },
  failed: { label: "Falha", mark: "✕", tone: "danger" },
  skipped: { label: "Não executada", mark: "–", tone: "neutral" },
};

export function presentDiagnostic(status: DiagnosticStatus): DiagnosticPresentation {
  return DIAGNOSTIC_PRESENTATION[status] ?? DIAGNOSTIC_PRESENTATION.skipped;
}

/**
 * Splits an integration's settings into what is shown and what is folded away.
 *
 * The server decides which is which; this only preserves the order it sent, so
 * two operators looking at the same integration see the same form.
 */
export function splitSettings(settings: IntegrationSetting[]): {
  common: IntegrationSetting[];
  advanced: IntegrationSetting[];
} {
  return {
    common: settings.filter((setting) => !setting.advanced),
    advanced: settings.filter((setting) => setting.advanced),
  };
}

/**
 * The sentence under an integration's name when no diagnostic has been run.
 *
 * Switched off, invisible-from-here and simply not yet checked are three
 * different facts leading to three different actions, so they get three
 * different sentences rather than a shared "sem informação".
 */
export function passiveSummary(integration: Integration): string {
  if (!integration.enabled) {
    return "Integração desligada na configuração deste ambiente. Não é uma falha.";
  }
  if (!integration.observable) {
    return (
      "O serviço administrativo não recebe a configuração que nomeia o endpoint desta " +
      "integração. Este é o estado desconhecido — não é saudável."
    );
  }
  return integration.detail;
}

/**
 * Whether the console should offer the diagnostic button.
 *
 * Purely a user-experience rule. The API refuses the same request whether or
 * not the button rendered, and an operator who edits the DOM gets a 403 or a
 * 409 — so this hides a control that would fail, and protects nothing.
 */
export function canDiagnose(
  integration: Integration,
  can: (capability: string) => boolean,
): boolean {
  return integration.diagnosable && can("admin.integrations.manage");
}
