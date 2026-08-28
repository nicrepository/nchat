/**
 * Searching the configuration inventory (issue #582).
 *
 * The single rule this module exists to enforce: **no value is indexed**.
 *
 * A search that matched on values would be a search that confirms a guess. Type
 * a suspected token and a hit tells you it is right; type a password and the
 * absence of a hit narrows the space. Credentials never reach this console as
 * values at all, but a masked or derived form would leak the same way, so the
 * index is built from metadata only — the label, the description, the key, the
 * owning service and the section — and `indexOf` below is the whole list.
 *
 * It is also deliberately deterministic: same settings and same term produce
 * the same order every time, so a result can be described in a ticket and found
 * again.
 */

import type { ConfigSetting } from "../api/configApi";

/** What a setting contributes to the index. Values are conspicuously absent. */
export function indexOf(setting: ConfigSetting): string {
  return [
    setting.label,
    setting.description,
    setting.key,
    setting.category,
    setting.ownerService,
    setting.envVar,
  ]
    .join(" ")
    .toLocaleLowerCase("pt-BR");
}

/**
 * Normalizes a term for comparison.
 *
 * Accents are folded so "configuracao" finds "configuração": an operator typing
 * quickly should not have to reach for a dead key to find a field.
 */
export function normalize(term: string): string {
  return term
    .normalize("NFD")
    .replace(/\p{Diacritic}/gu, "")
    .toLocaleLowerCase("pt-BR")
    .trim();
}

/**
 * Filters settings by a free-text term.
 *
 * An empty term returns everything, in the order the server sent — the catalog
 * order, which the whole screen is written against. A term matches when every
 * whitespace-separated word appears somewhere in the setting's metadata, so
 * "smtp senha" finds the SMTP credential and "senha smtp" finds the same one.
 */
export function searchSettings<T extends ConfigSetting>(settings: T[], term: string): T[] {
  const words = normalize(term).split(/\s+/).filter(Boolean);
  if (words.length === 0) return settings;
  return settings.filter((setting) => {
    const haystack = normalize(indexOf(setting));
    return words.every((word) => haystack.includes(word));
  });
}
