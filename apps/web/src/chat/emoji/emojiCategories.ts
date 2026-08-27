/**
 * The picker's category bar (issue #496, CQ round 3).
 *
 * The text pills this replaces — "Rostos e emoções", "Pessoas e corpo" — cost
 * most of the picker's width, wrapped or clipped on a narrow viewport, and
 * pushed the grid below the fold. A row of ten glyphs fits any width the picker
 * is offered and leaves the emoji themselves as the page's subject.
 *
 * The glyphs are emoji, not icon assets: the picker already renders the reader's
 * own emoji font, so the bar needs no new font, sprite or licence.
 */

export interface EmojiCategory {
  /** Unicode group index, or "recent" for the reader's own history. */
  key: number | "recent";
  icon: string;
  /** The accessible name, and the tooltip. Never rendered as a visible label. */
  label: string;
}

/** Indexed by Unicode group; index 2 is the Component group, which has no emoji. */
const groupCategories: readonly (EmojiCategory | null)[] = [
  { key: 0, icon: "😀", label: "Rostos e emoções" },
  { key: 1, icon: "🧑", label: "Pessoas e corpo" },
  null,
  { key: 3, icon: "🐻", label: "Animais e natureza" },
  { key: 4, icon: "🍕", label: "Comidas e bebidas" },
  { key: 5, icon: "🚗", label: "Viagens e lugares" },
  { key: 6, icon: "⚽", label: "Atividades" },
  { key: 7, icon: "💡", label: "Objetos" },
  { key: 8, icon: "🔣", label: "Símbolos" },
  { key: 9, icon: "🚩", label: "Bandeiras" },
];

export const recentCategory: EmojiCategory = { key: "recent", icon: "🕘", label: "Recentes" };

/** The bar's tabs, in catalog order, with the history tab first when there is one. */
export function emojiCategories(groups: readonly number[], hasHistory: boolean): EmojiCategory[] {
  const tabs = groups
    .map((group) => groupCategories[group])
    .filter((category): category is EmojiCategory => category !== null);
  return hasHistory ? [recentCategory, ...tabs] : tabs;
}

/** Unicode's five modifiers, plus the unmodified default the palette opens with. */
export const skinToneLabels: readonly string[] = [
  "Padrão",
  "Clara",
  "Morena clara",
  "Morena",
  "Morena escura",
  "Escura",
];
