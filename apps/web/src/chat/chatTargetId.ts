const HYPHENATED_UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const COMPACT_UUID_PATTERN = /^[0-9a-f]{32}$/i;

/**
 * Returns the canonical textual form used by the chat backend for UUID
 * targets. Synthetic/non-UUID IDs remain supported for local fixtures and
 * backwards-compatible client contracts.
 */
export function normalizeChatTargetId(value: string): string {
  const trimmed = value.trim();
  const unwrapped =
    trimmed.length === 38 && trimmed.startsWith("{") && trimmed.endsWith("}")
      ? trimmed.slice(1, -1)
      : trimmed;

  if (HYPHENATED_UUID_PATTERN.test(unwrapped)) return unwrapped.toLowerCase();
  if (!COMPACT_UUID_PATTERN.test(unwrapped)) return trimmed;

  return [
    unwrapped.slice(0, 8),
    unwrapped.slice(8, 12),
    unwrapped.slice(12, 16),
    unwrapped.slice(16, 20),
    unwrapped.slice(20),
  ]
    .join("-")
    .toLowerCase();
}
