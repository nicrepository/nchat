import type { ConfigPlan, ConfigSetting, ConfigValue } from "../api/configApi";

/**
 * Editing helpers for the configuration screen.
 *
 * Every draft is held as a string, whatever the setting's type, because that is
 * what an input actually contains: "1" on the way to "12" is not the number 1,
 * and coercing it while the operator is still typing is how a field fights
 * back. Conversion to a typed value happens once, when the change is reviewed.
 *
 * The validation here is for immediate feedback and nothing else. The server
 * runs the same rules from its own registry and its answer is the one that
 * decides; this copy exists so an operator does not have to round-trip to learn
 * that 2 is below the minimum.
 */

/**
 * The one configuration document this console can write.
 *
 * Named here rather than typed at each call site so the screen, the review flow
 * and the history all address the same document; the server refuses any other
 * anyway, and a second spelling would only turn that refusal into a mystery.
 */
export const AUTH_POLICY_DOCUMENT = "auth.policy";

/** The value of an unset nullable field. Empty, not zero. */
const UNSET = "";

export function draftFrom(setting: ConfigSetting): string {
  const value = setting.value;
  if (value === undefined || value === null) return UNSET;
  if (typeof value === "boolean") return value ? "true" : "false";
  return String(value);
}

export function draftsFrom(settings: ConfigSetting[]): Record<string, string> {
  const drafts: Record<string, string> = {};
  for (const setting of settings) {
    if (setting.editable) drafts[setting.key] = draftFrom(setting);
  }
  return drafts;
}

/**
 * Converts a draft into the value the API would receive, or null when the draft
 * is not a value at all.
 *
 * A blank nullable field is `{ value: null }` — an explicit absence — while a
 * blank non-nullable one is not a value and returns null here, which is what
 * stops an empty input from being submitted as zero.
 */
export function toConfigValue(
  setting: ConfigSetting,
  draft: string,
): { value: ConfigValue } | null {
  const trimmed = draft.trim();
  if (setting.type === "bool") {
    return readBoolean(trimmed);
  }
  if (trimmed === UNSET) {
    return setting.nullable ? { value: null } : null;
  }
  if (setting.type === "int") {
    return readInteger(trimmed);
  }
  return { value: trimmed };
}

/** A checkbox is on or off. There is no third spelling and no default. */
function readBoolean(trimmed: string): { value: ConfigValue } | null {
  if (trimmed === "true") return { value: true };
  if (trimmed === "false") return { value: false };
  return null;
}

/**
 * A plain decimal integer and nothing else.
 *
 * "12.0", "1e2", "12px" and "-" are all things a text input can hold, and none
 * of them is a limit. Beyond the safe-integer range a number stops meaning what
 * it reads as, so it is refused rather than rounded on the way to the server.
 */
function readInteger(trimmed: string): { value: ConfigValue } | null {
  if (!/^-?\d+$/.test(trimmed)) return null;
  const parsed = Number(trimmed);
  return Number.isSafeInteger(parsed) ? { value: parsed } : null;
}

/** The message to show under a field, or null when the draft is acceptable. */
export function validateDraft(setting: ConfigSetting, draft: string): string | null {
  const converted = toConfigValue(setting, draft);
  if (converted === null) {
    return setting.type === "int" ? "Informe um número inteiro." : "Valor inválido.";
  }
  // A blank nullable field and a text value both carry no range to check. The
  // typed absence is a value, not a failure.
  if (typeof converted.value !== "number") return null;
  return rangeMessage(setting, converted.value);
}

/** The administrative range, as the server published it with the setting. */
function rangeMessage(setting: ConfigSetting, value: number): string | null {
  if (setting.min !== null && value < setting.min) {
    return `O mínimo aceito é ${setting.min}.`;
  }
  if (setting.max !== null && value > setting.max) {
    return `O máximo aceito é ${setting.max}.`;
  }
  return null;
}

/**
 * The drafts that differ from what is stored, as typed values.
 *
 * Unchanged fields are dropped here as well as on the server: a form that
 * submitted every field it rendered would ask for thirteen changes when the
 * operator made one, and the confirmation would say so.
 */
export function changedValues(
  settings: ConfigSetting[],
  drafts: Record<string, string>,
): Record<string, ConfigValue> {
  const changes: Record<string, ConfigValue> = {};
  for (const setting of settings) {
    if (!setting.editable) continue;
    const draft = drafts[setting.key];
    if (draft === undefined || draft === draftFrom(setting)) continue;
    const converted = toConfigValue(setting, draft);
    if (converted === null) continue;
    changes[setting.key] = converted.value;
  }
  return changes;
}

export function hasInvalidDraft(
  settings: ConfigSetting[],
  drafts: Record<string, string>,
): boolean {
  return settings.some(
    (setting) =>
      setting.editable &&
      drafts[setting.key] !== undefined &&
      drafts[setting.key] !== draftFrom(setting) &&
      validateDraft(setting, drafts[setting.key]) !== null,
  );
}

export function isDirty(settings: ConfigSetting[], drafts: Record<string, string>): boolean {
  return settings.some(
    (setting) =>
      setting.editable &&
      drafts[setting.key] !== undefined &&
      drafts[setting.key] !== draftFrom(setting),
  );
}

/**
 * Renders a value for reading.
 *
 * `undefined` means the deployment cannot observe the setting, which is a
 * different sentence from "não definido": one is a Secret scoped to another
 * workload, the other is a value that is genuinely unset.
 */
export function formatConfigValue(value: ConfigValue | undefined, unit = ""): string {
  if (value === undefined) return "Não observável neste serviço";
  if (value === null) return "Não definido";
  if (typeof value === "boolean") return value ? "Sim" : "Não";
  if (value === "") return "Vazio";
  return unit === "" ? String(value) : `${value} ${unit}`;
}

const APPLY_LABELS: Record<string, string> = {
  runtime: "Aplica em runtime",
  rollout: "Exige rollout",
  external: "Controlado fora da aplicação",
};

const SOURCE_LABELS: Record<string, string> = {
  database: "Banco de dados",
  gitops: "GitOps",
  sealed_secret: "Sealed Secret",
};

const CLASS_LABELS: Record<string, string> = {
  A: "Classe A · runtime",
  B: "Classe B · runtime com credencial",
  C: "Classe C · restart/rollout",
  D: "Classe D · infraestrutura",
};

export function applyLabel(apply: string): string {
  return APPLY_LABELS[apply] ?? apply;
}

export function sourceLabel(source: string): string {
  return SOURCE_LABELS[source] ?? source;
}

export function classLabel(configClass: string): string {
  return CLASS_LABELS[configClass] ?? `Classe ${configClass}`;
}

const CATEGORY_LABELS: Record<string, string> = {
  authentication: "Autenticação",
  platform: "Plataforma",
  integrations: "Integrações",
  infrastructure: "Infraestrutura",
  credentials: "Credenciais",
};

export function categoryLabel(category: string): string {
  return CATEGORY_LABELS[category] ?? category;
}

/** Groups settings by category, preserving the order the server sent. */
export function groupByCategory(settings: ConfigSetting[]): [string, ConfigSetting[]][] {
  const groups = new Map<string, ConfigSetting[]>();
  for (const setting of settings) {
    const bucket = groups.get(setting.category);
    if (bucket === undefined) {
      groups.set(setting.category, [setting]);
      continue;
    }
    bucket.push(setting);
  }
  return [...groups.entries()];
}

/**
 * Whether a reviewed plan may be confirmed.
 *
 * The console's copy of a decision the server makes again on the request. It
 * exists so an operator is not invited to click something that would be
 * refused, and it is deliberately the same list the API enforces: a stale
 * document, a superseded version, a missing capability, a dangerous change with
 * no stated reason, or nothing to change at all.
 */
export function confirmBlocked(plan: ConfigPlan, pending: boolean, reason: string): boolean {
  if (pending || plan.changes.length === 0) return true;
  if (plan.stale || plan.superseded || !plan.authorized) return true;
  return plan.reasonRequired && reason.trim() === "";
}
