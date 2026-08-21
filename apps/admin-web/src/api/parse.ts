/**
 * Runtime shape checks for the Admin API's payloads.
 *
 * TypeScript types are erased, so a response that does not match one produces
 * `undefined` several screens later rather than an error here. These helpers
 * exist so a contract mismatch fails at the boundary, loudly, with the field
 * that is wrong — the same reason the client already refuses a 2xx that carries
 * no `data`.
 *
 * They are deliberately small and shared rather than one hand-written parser
 * per endpoint: the payloads differ in their fields, not in what "a string" or
 * "a number" means.
 */

import { AdminApiError } from "./client";

export const ERR_INVALID_RESPONSE = "invalid_response";

export function contractError(detail: string): AdminApiError {
  return new AdminApiError(200, ERR_INVALID_RESPONSE, `Resposta inválida da API: ${detail}`);
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function requireRecord(value: unknown, field: string): Record<string, unknown> {
  if (!isRecord(value)) throw contractError(`${field} deve ser um objeto`);
  return value;
}

export function requireArray(value: unknown, field: string): unknown[] {
  if (!Array.isArray(value)) throw contractError(`${field} deve ser uma lista`);
  return value;
}

export function str(raw: Record<string, unknown>, key: string, field: string): string {
  const value = raw[key];
  if (typeof value !== "string") throw contractError(`${field}.${key} deve ser texto`);
  return value;
}

export function num(raw: Record<string, unknown>, key: string, field: string): number {
  const value = raw[key];
  // Number.isFinite rather than typeof: NaN and Infinity are numbers to
  // JavaScript and are not counts, sizes or limits to anybody.
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw contractError(`${field}.${key} deve ser um número`);
  }
  return value;
}

export function bool(raw: Record<string, unknown>, key: string, field: string): boolean {
  const value = raw[key];
  if (typeof value !== "boolean") throw contractError(`${field}.${key} deve ser booleano`);
  return value;
}

/** A field the API sends as a string or as null, never absent. */
export function nullableStr(
  raw: Record<string, unknown>,
  key: string,
  field: string,
): string | null {
  const value = raw[key];
  if (value === null || value === undefined) return null;
  if (typeof value !== "string") throw contractError(`${field}.${key} deve ser texto ou nulo`);
  return value;
}

export function strList(raw: Record<string, unknown>, key: string, field: string): string[] {
  return requireArray(raw[key], `${field}.${key}`).map((entry, index) => {
    if (typeof entry !== "string") throw contractError(`${field}.${key}[${index}] deve ser texto`);
    return entry;
  });
}

/** One page of a keyset-paginated listing. */
export interface Page<T> {
  items: T[];
  nextCursor: string | null;
  hasMore: boolean;
}

/**
 * Reads the `pagination` object every listing carries.
 *
 * `has_more` and `next_cursor` must agree. When they do not, one of them is
 * wrong and there is no way to tell which: paging on a cursor we do not trust
 * risks an endless loop, so this is an error rather than a guess.
 */
export function parsePagination(body: Record<string, unknown>): {
  nextCursor: string | null;
  hasMore: boolean;
} {
  const pagination = requireRecord(body.pagination, "pagination");
  const hasMore = bool(pagination, "has_more", "pagination");
  const nextCursor = nullableStr(pagination, "next_cursor", "pagination");
  if (hasMore && nextCursor === null) {
    throw contractError("pagination.has_more é verdadeiro sem next_cursor");
  }
  // The contract says the last page carries null. An empty string is not a
  // second spelling of it: accepting both is how the two ends drift, because
  // whichever one the server stops sending, this keeps working. Rejecting it
  // means the mismatch surfaces here instead of as a listing that pages
  // forever on a cursor made of nothing.
  if (nextCursor === "") {
    throw contractError("pagination.next_cursor vazio deve ser nulo");
  }
  return { nextCursor, hasMore };
}

export function parsePage<T>(
  body: unknown,
  key: string,
  mapItem: (raw: Record<string, unknown>, index: number) => T,
): Page<T> {
  const record = requireRecord(body, "data");
  const items = requireArray(record[key], key).map((entry, index) =>
    mapItem(requireRecord(entry, `${key}[${index}]`), index),
  );
  return { items, ...parsePagination(record) };
}
