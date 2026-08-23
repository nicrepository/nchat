/**
 * The configuration surface of the Admin API (issue #580).
 *
 * Two things this module never does, and both are the point:
 *
 *  - it never invents a key. Every setting the console renders came from
 *    `GET /config`, and the server refuses a key its registry does not
 *    declare, so a client that made one up gets a 400 rather than a write;
 *  - it never asks for a credential's value, because no endpoint returns one.
 *    A sensitive setting arrives with `configured` and no `value` at all, and
 *    the type below is what keeps that shape from being read as an empty
 *    string somewhere down the tree.
 */

import { adminFetch } from "./client";
import {
  bool,
  contractError,
  isRecord,
  num,
  nullableStr,
  requireArray,
  requireRecord,
  str,
  strList,
} from "./parse";

/** How a change reaches the running platform. */
export type ConfigApply = "runtime" | "rollout" | "external";

/** Where the authoritative value lives. */
export type ConfigSource = "database" | "gitops" | "sealed_secret";

export type ConfigValueType = "int" | "bool" | "string";

/**
 * A configuration value as the API sends it: a JSON scalar or null.
 *
 * `undefined` is a fourth state the API expresses by omitting the field
 * entirely — the setting is not observable from this deployment — and it is
 * deliberately not collapsed into null, which means "configured as nothing".
 */
export type ConfigValue = number | boolean | string | null;

export interface ConfigSetting {
  key: string;
  label: string;
  description: string;
  category: string;
  ownerService: string;
  /** A, B, C or D. Declared by the server, never derived here. */
  configClass: string;
  source: ConfigSource;
  apply: ConfigApply;
  type: ConfigValueType;
  unit: string;
  min: number | null;
  max: number | null;
  nullable: boolean;
  default: ConfigValue | undefined;
  editable: boolean;
  readOnlyReason: string;
  sensitive: boolean;
  document: string;
  manageCapability: string;
  dangerNote: string;
  rollbackable: boolean;
  envVar: string;
  /** Whether this deployment can see the value at all. */
  observable: boolean;
  value: ConfigValue | undefined;
  /** Only ever present for a credential, and never alongside a value. */
  configured: boolean | undefined;
}

export interface ConfigDocument {
  key: string;
  revision: number;
}

export interface ConfigCatalog {
  documents: ConfigDocument[];
  settings: ConfigSetting[];
}

export interface ConfigDiffEntry {
  key: string;
  label: string;
  category: string;
  ownerService: string;
  apply: ConfigApply;
  unit: string;
  dangerous: boolean;
  dangerNote: string;
  from: ConfigValue;
  to: ConfigValue;
}

export interface ConfigValidationError {
  key: string;
  message: string;
}

export interface ConfigPlan {
  document: string;
  revision: number;
  /** The document moved since the client read it. */
  stale: boolean;
  /**
   * Only meaningful for a rollback preview: at least one field of the version
   * being reverted no longer holds the value that version set, so undoing it
   * would discard a later change.
   *
   * A different fact from `stale` — the console may have loaded *after* the
   * change that superseded the version, so the revision agrees while the
   * rollback is still impossible. Always false for an ordinary edit, which
   * carries no preconditions.
   */
  superseded: boolean;
  changes: ConfigDiffEntry[];
  dangerous: boolean;
  requiredCapability: string;
  authorized: boolean;
  reasonRequired: boolean;
  warnings: string[];
  errors: ConfigValidationError[];
  affectedServices: string[];
  apply: ConfigApply;
}

export interface ConfigVersion {
  id: string;
  document: string;
  revision: number;
  appliedAt: string;
  actorUserId: string;
  actorEmail: string;
  correlationId: string;
  reason: string;
  revertsRevision: number;
  rollbackable: boolean;
  changes: ConfigDiffEntry[];
}

export interface ConfigApplyResult {
  applied: boolean;
  document: string;
  revision: number;
  values: Record<string, ConfigValue>;
  plan: ConfigPlan;
  version: ConfigVersion | null;
}

const APPLY_MODES: ConfigApply[] = ["runtime", "rollout", "external"];
const SOURCES: ConfigSource[] = ["database", "gitops", "sealed_secret"];
const VALUE_TYPES: ConfigValueType[] = ["int", "bool", "string"];

function oneOf<T extends string>(
  raw: Record<string, unknown>,
  key: string,
  field: string,
  allowed: T[],
): T {
  const value = str(raw, key, field);
  if (!allowed.includes(value as T)) {
    throw contractError(`${field}.${key} desconhecido: ${value}`);
  }
  return value as T;
}

/**
 * Reads a scalar the API may legitimately omit.
 *
 * Absent and null are kept apart on purpose: absent means the deployment
 * cannot observe the setting, null means it is observably unset. Collapsing
 * them would tell an operator that a credential scoped to another workload is
 * missing.
 */
function optionalValue(
  raw: Record<string, unknown>,
  key: string,
  field: string,
): ConfigValue | undefined {
  if (!(key in raw)) return undefined;
  const value = raw[key];
  if (value === null) return null;
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "boolean" || typeof value === "string") return value;
  throw contractError(`${field}.${key} deve ser um escalar`);
}

function requiredValue(raw: Record<string, unknown>, key: string, field: string): ConfigValue {
  if (!(key in raw)) throw contractError(`${field}.${key} ausente`);
  return optionalValue(raw, key, field) ?? null;
}

function optionalNumber(raw: Record<string, unknown>, key: string, field: string): number | null {
  return key in raw ? num(raw, key, field) : null;
}

function optionalText(raw: Record<string, unknown>, key: string): string {
  const value = raw[key];
  return typeof value === "string" ? value : "";
}

function parseSetting(raw: Record<string, unknown>, index: number): ConfigSetting {
  const field = `settings[${index}]`;
  const sensitive = bool(raw, "sensitive", field);
  const setting: ConfigSetting = {
    key: str(raw, "key", field),
    label: str(raw, "label", field),
    description: str(raw, "description", field),
    category: str(raw, "category", field),
    ownerService: str(raw, "owner_service", field),
    configClass: str(raw, "class", field),
    source: oneOf(raw, "source", field, SOURCES),
    apply: oneOf(raw, "apply", field, APPLY_MODES),
    type: oneOf(raw, "type", field, VALUE_TYPES),
    unit: optionalText(raw, "unit"),
    min: optionalNumber(raw, "min", field),
    max: optionalNumber(raw, "max", field),
    nullable: bool(raw, "nullable", field),
    default: optionalValue(raw, "default", field),
    editable: bool(raw, "editable", field),
    readOnlyReason: optionalText(raw, "read_only_reason"),
    sensitive,
    document: optionalText(raw, "document"),
    manageCapability: optionalText(raw, "manage_capability"),
    dangerNote: optionalText(raw, "danger_note"),
    rollbackable: bool(raw, "rollbackable", field),
    envVar: optionalText(raw, "env_var"),
    observable: bool(raw, "observable", field),
    value: optionalValue(raw, "value", field),
    configured: "configured" in raw ? bool(raw, "configured", field) : undefined,
  };
  // The security invariant, checked at the boundary rather than trusted: a
  // credential that arrived carrying a value is a server that changed in a way
  // this console must not paper over by rendering it.
  if (sensitive && setting.value !== undefined) {
    throw contractError(`${field}.value presente em uma credencial`);
  }
  if (sensitive && setting.editable) {
    throw contractError(`${field} é uma credencial editável`);
  }
  return setting;
}

function parseDiffEntry(raw: Record<string, unknown>, field: string): ConfigDiffEntry {
  return {
    key: str(raw, "key", field),
    label: str(raw, "label", field),
    category: optionalText(raw, "category"),
    ownerService: optionalText(raw, "owner_service"),
    apply: (optionalText(raw, "apply") || "runtime") as ConfigApply,
    unit: optionalText(raw, "unit"),
    dangerous: bool(raw, "dangerous", field),
    dangerNote: optionalText(raw, "danger_note"),
    from: requiredValue(raw, "from", field),
    to: requiredValue(raw, "to", field),
  };
}

function parseDiff(raw: Record<string, unknown>, key: string, field: string): ConfigDiffEntry[] {
  return requireArray(raw[key], `${field}.${key}`).map((entry, index) =>
    parseDiffEntry(requireRecord(entry, `${field}.${key}[${index}]`), `${field}.${key}[${index}]`),
  );
}

function parsePlan(value: unknown): ConfigPlan {
  const raw = requireRecord(value, "plan");
  return {
    document: str(raw, "document", "plan"),
    revision: num(raw, "revision", "plan"),
    stale: bool(raw, "stale", "plan"),
    superseded: bool(raw, "superseded", "plan"),
    changes: parseDiff(raw, "changes", "plan"),
    dangerous: bool(raw, "dangerous", "plan"),
    requiredCapability: optionalText(raw, "required_capability"),
    authorized: bool(raw, "authorized", "plan"),
    reasonRequired: bool(raw, "reason_required", "plan"),
    warnings: strList(raw, "warnings", "plan"),
    errors: requireArray(raw.errors, "plan.errors").map((entry, index) => {
      const failure = requireRecord(entry, `plan.errors[${index}]`);
      return {
        key: str(failure, "key", `plan.errors[${index}]`),
        message: str(failure, "message", `plan.errors[${index}]`),
      };
    }),
    affectedServices: strList(raw, "affected_services", "plan"),
    apply: (optionalText(raw, "apply") || "runtime") as ConfigApply,
  };
}

function parseVersion(value: unknown, field: string): ConfigVersion {
  const raw = requireRecord(value, field);
  return {
    id: str(raw, "id", field),
    document: str(raw, "document", field),
    revision: num(raw, "revision", field),
    appliedAt: str(raw, "applied_at", field),
    actorUserId: nullableStr(raw, "actor_user_id", field) ?? "",
    actorEmail: nullableStr(raw, "actor_email", field) ?? "",
    correlationId: nullableStr(raw, "correlation_id", field) ?? "",
    reason: optionalText(raw, "reason"),
    revertsRevision: num(raw, "reverts_revision", field),
    rollbackable: bool(raw, "rollbackable", field),
    changes: parseDiff(raw, "changes", field),
  };
}

function parseApplyResult(value: unknown): ConfigApplyResult {
  const raw = requireRecord(value, "data");
  const values: Record<string, ConfigValue> = {};
  const rawValues = requireRecord(raw.values, "data.values");
  for (const [key, entry] of Object.entries(rawValues)) {
    if (entry === null || typeof entry === "number" || typeof entry === "boolean") {
      values[key] = entry;
      continue;
    }
    throw contractError(`data.values.${key} deve ser um escalar`);
  }
  return {
    applied: bool(raw, "applied", "data"),
    document: str(raw, "document", "data"),
    revision: num(raw, "revision", "data"),
    values,
    plan: parsePlan(raw.plan),
    version: isRecord(raw.version) ? parseVersion(raw.version, "data.version") : null,
  };
}

export async function loadConfiguration(signal?: AbortSignal): Promise<ConfigCatalog> {
  const body = await adminFetch<unknown>("/config", { signal });
  const raw = requireRecord(body, "data");
  return {
    documents: requireArray(raw.documents, "documents").map((entry, index) => {
      const document = requireRecord(entry, `documents[${index}]`);
      return {
        key: str(document, "key", `documents[${index}]`),
        revision: num(document, "revision", `documents[${index}]`),
      };
    }),
    settings: requireArray(raw.settings, "settings").map((entry, index) =>
      parseSetting(requireRecord(entry, `settings[${index}]`), index),
    ),
  };
}

export interface ConfigChangeRequest {
  document: string;
  expectedRevision: number;
  reason?: string;
  changes: Record<string, ConfigValue>;
}

function changeBody(request: ConfigChangeRequest): string {
  return JSON.stringify({
    document: request.document,
    expected_revision: request.expectedRevision,
    reason: request.reason ?? "",
    changes: request.changes,
  });
}

/** Asks the server what a change would do. Writes nothing. */
export async function previewConfiguration(request: ConfigChangeRequest): Promise<ConfigPlan> {
  const body = await adminFetch<unknown>("/config/preview", {
    method: "POST",
    body: changeBody(request),
  });
  return parsePlan(requireRecord(body, "data").plan);
}

export async function applyConfiguration(request: ConfigChangeRequest): Promise<ConfigApplyResult> {
  const body = await adminFetch<unknown>("/config/apply", {
    method: "POST",
    body: changeBody(request),
  });
  return parseApplyResult(body);
}

export async function listConfigVersions(
  document: string,
  limit: number,
  signal?: AbortSignal,
): Promise<ConfigVersion[]> {
  const params = new URLSearchParams({ document, limit: String(limit) });
  const body = await adminFetch<unknown>(`/config/versions?${params.toString()}`, { signal });
  const raw = requireRecord(body, "data");
  return requireArray(raw.versions, "versions").map((entry, index) =>
    parseVersion(entry, `versions[${index}]`),
  );
}

/**
 * Asks the server what reverting one version would do.
 *
 * The request names the version and the revision this client last read, and
 * nothing else: which values a rollback restores, and whether it is still
 * possible, are facts about the recorded version and the current state. A
 * console that rebuilt the change set from the history it renders would be
 * deriving an administrative mutation from presentation data — and would show a
 * diff the confirmed rollback then refuses.
 */
export async function previewConfigRollback(
  versionId: string,
  expectedRevision: number,
): Promise<ConfigPlan> {
  const body = await adminFetch<unknown>(
    `/config/versions/${encodeURIComponent(versionId)}/rollback/preview`,
    { method: "POST", body: JSON.stringify({ expected_revision: expectedRevision }) },
  );
  return parsePlan(requireRecord(body, "data").plan);
}

export async function rollbackConfigVersion(
  versionId: string,
  expectedRevision: number,
  reason: string,
): Promise<ConfigApplyResult> {
  const body = await adminFetch<unknown>(
    `/config/versions/${encodeURIComponent(versionId)}/rollback`,
    {
      method: "POST",
      body: JSON.stringify({ expected_revision: expectedRevision, reason }),
    },
  );
  return parseApplyResult(body);
}
