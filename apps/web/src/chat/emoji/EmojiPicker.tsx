/**
 * The full emoji picker (issue #496).
 *
 * Loaded on demand — this module and the catalog it imports are a separate
 * chunk, so a conversation that never opens the picker never downloads a
 * thousand emoji names. Everything it shows comes from that one catalog and
 * from a local preference; it makes no request of its own, and none per emoji.
 *
 * Layout follows what a mature messenger's picker does, in NChat's own tokens:
 * a search field that owns the header, a single row of category glyphs, and the
 * grid taking every pixel below them. Search and categories stay put; only the
 * grid scrolls, so a reader deep in "Símbolos" has not lost the way back.
 */

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
} from "react";

import { emojiCategories, type EmojiCategory } from "./emojiCategories";
import {
  emojiGroupLabels,
  emojisInGroup,
  entriesFor,
  populatedEmojiGroups,
  searchEmojis,
  withSkinTone,
  type EmojiCatalog,
  type EmojiEntry,
} from "./emojiCatalog";
import { nextEmojiIndex } from "./emojiGridNavigation";
import EmojiTonePalette from "./EmojiTonePalette";
import { frequentEmojis, recentEmojis, type EmojiUsage } from "./emojiUsage";
import { useEmojiCatalog, type EmojiCatalogStatus } from "./useEmojiCatalog";

/** Must match the column count in the stylesheet: the arrows step by a row. */
const gridColumns = 8;
const quickSectionSize = 16;

type EmojiTab = number | "recent";

interface EmojiSection {
  key: string;
  title: string;
  entries: readonly EmojiEntry[];
}

export interface EmojiPickerProps {
  usage: EmojiUsage;
  onToneChange: (tone: number) => void;
  onSelect: (emoji: string) => void;
}

/**
 * "Recentes" and, once there is enough history to mean anything, "Mais usados".
 * An empty section is dropped rather than rendered as a heading over nothing.
 */
function historySections(catalog: EmojiCatalog, usage: EmojiUsage): EmojiSection[] {
  return [
    {
      key: "recent",
      title: "Recentes",
      entries: entriesFor(catalog, recentEmojis(usage, quickSectionSize)),
    },
    {
      key: "frequent",
      title: "Mais usados",
      entries: entriesFor(catalog, frequentEmojis(usage, quickSectionSize)),
    },
  ].filter((section) => section.entries.length > 0);
}

function sectionsFor(
  catalog: EmojiCatalog,
  tab: EmojiTab,
  usage: EmojiUsage,
  query: string,
): EmojiSection[] {
  if (query.trim()) {
    // A search answers one question, so it gets one unlabelled list rather than
    // a heading the reader did not ask for.
    return [{ key: "search", title: "", entries: searchEmojis(catalog, query) }];
  }
  if (tab === "recent") return historySections(catalog, usage);
  return [
    { key: `group-${tab}`, title: emojiGroupLabels[tab], entries: emojisInGroup(catalog, tab) },
  ];
}

/**
 * One section's emoji, as a single tab stop navigated with the arrows.
 *
 * Focus is moved imperatively rather than through an effect so that opening the
 * picker, or re-rendering it because a search narrowed, never pulls focus on its
 * own — only a key the user actually pressed moves it.
 */
function EmojiGrid({
  entries,
  tone,
  labelledBy,
  onActivate,
}: {
  entries: readonly EmojiEntry[];
  tone: number;
  labelledBy: string | undefined;
  onActivate: (entry: EmojiEntry, cell: HTMLButtonElement) => void;
}) {
  const [activeIndex, setActiveIndex] = useState(0);
  const gridRef = useRef<HTMLDivElement>(null);

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const target = nextEmojiIndex(event.key, activeIndex, entries.length, gridColumns);
    if (target === null) return;
    event.preventDefault();
    setActiveIndex(target);
    gridRef.current?.querySelectorAll("button")[target]?.focus();
  };

  return (
    <div
      ref={gridRef}
      className="chat-emoji-picker__grid"
      role="group"
      aria-label={labelledBy ? undefined : "Resultados da busca"}
      aria-labelledby={labelledBy}
      onKeyDown={handleKeyDown}
    >
      {entries.map((entry, index) => (
        <button
          key={entry.unicode}
          type="button"
          className="chat-emoji-picker__emoji"
          aria-label={entry.label}
          aria-haspopup={entry.skins ? "dialog" : undefined}
          tabIndex={index === Math.min(activeIndex, entries.length - 1) ? 0 : -1}
          onClick={(event) => onActivate(entry, event.currentTarget)}
        >
          <span aria-hidden="true">{withSkinTone(entry, tone)}</span>
        </button>
      ))}
    </div>
  );
}

function EmojiCategoryBar({
  categories,
  tab,
  onChange,
}: {
  categories: EmojiCategory[];
  tab: EmojiTab;
  onChange: (tab: EmojiTab) => void;
}) {
  return (
    <div className="chat-emoji-picker__tabs" role="tablist" aria-label="Categorias de emoji">
      {categories.map((category) => (
        <button
          key={String(category.key)}
          type="button"
          role="tab"
          className="chat-emoji-picker__tab"
          aria-selected={category.key === tab}
          aria-label={category.label}
          title={category.label}
          onClick={() => onChange(category.key)}
        >
          <span aria-hidden="true">{category.icon}</span>
        </button>
      ))}
    </div>
  );
}

/**
 * What the picker shows while the catalog is on its way, and when it never
 * arrived.
 *
 * The failure is a dead end without the retry: the catalog is a lazily-imported
 * chunk, and a reader who lost it once has no other way back to the full
 * library. The quick reactions above the message keep working either way, so
 * nothing here closes the conversation or blocks reacting.
 */
function EmojiCatalogNotice({
  status,
  onRetry,
}: {
  status: EmojiCatalogStatus;
  onRetry: () => void;
}) {
  if (status === "loading") {
    return (
      <p className="chat-emoji-picker__status" role="status">
        Carregando emojis…
      </p>
    );
  }
  return (
    <div className="chat-emoji-picker__status" role="alert">
      <p>Não foi possível carregar os emojis.</p>
      <button type="button" className="chat-emoji-picker__retry" onClick={onRetry}>
        Tentar novamente
      </button>
    </div>
  );
}

function EmojiSearchField({
  searchRef,
  query,
  onQueryChange,
}: {
  searchRef: RefObject<HTMLInputElement | null>;
  query: string;
  onQueryChange: (query: string) => void;
}) {
  return (
    <div className="chat-emoji-picker__search">
      <span className="chat-emoji-picker__search-icon material-symbols-outlined" aria-hidden="true">
        search
      </span>
      <input
        ref={searchRef}
        type="search"
        aria-label="Buscar emoji"
        placeholder="Buscar emoji"
        value={query}
        onChange={(event) => onQueryChange(event.target.value)}
      />
    </div>
  );
}

function EmojiSections({
  sections,
  tone,
  onActivate,
}: {
  sections: EmojiSection[];
  tone: number;
  onActivate: (entry: EmojiEntry, cell: HTMLButtonElement) => void;
}) {
  if (sections.every((section) => section.entries.length === 0)) {
    return <p className="chat-emoji-picker__status">Nenhum emoji encontrado.</p>;
  }
  return (
    <>
      {sections.map((section) => (
        <section key={section.key} className="chat-emoji-picker__section">
          {section.title && (
            <h3 className="chat-emoji-picker__section-title" id={`emoji-section-${section.key}`}>
              {section.title}
            </h3>
          )}
          <EmojiGrid
            entries={section.entries}
            tone={tone}
            labelledBy={section.title ? `emoji-section-${section.key}` : undefined}
            onActivate={onActivate}
          />
        </section>
      ))}
    </>
  );
}

/** The emoji whose tone is being chosen, and the cell the palette hangs off. */
interface TonePick {
  entry: EmojiEntry;
  anchor: DOMRect;
  cell: HTMLButtonElement;
}

export default function EmojiPicker({ usage, onToneChange, onSelect }: EmojiPickerProps) {
  const { status, catalog, retry } = useEmojiCatalog();
  const [query, setQuery] = useState("");
  const [tab, setTab] = useState<EmojiTab | null>(null);
  const [tonePick, setTonePick] = useState<TonePick | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);

  // Opening a dialog moves focus into it, which here is the moment the catalog
  // arrives and the field exists. It is the only focus move the picker makes on
  // its own.
  useEffect(() => {
    searchRef.current?.focus();
  }, [catalog]);

  const hasHistory = usage.entries.length > 0;
  const activeTab = tab ?? (hasHistory ? "recent" : 0);
  const sections = useMemo(
    () => (catalog ? sectionsFor(catalog, activeTab, usage, query) : []),
    [catalog, activeTab, usage, query],
  );

  /**
   * An emoji with skin-tone variants asks which one; every other emoji is simply
   * chosen. The palette is what makes the choice contextual — it is anchored to
   * the cell that was clicked, not to a control in the header.
   */
  const activate = useCallback(
    (entry: EmojiEntry, cell: HTMLButtonElement) => {
      if (!entry.skins) {
        onSelect(entry.unicode);
        return;
      }
      setTonePick({ entry, anchor: cell.getBoundingClientRect(), cell });
    },
    [onSelect],
  );

  const dismissTone = useCallback(() => {
    setTonePick((current) => {
      current?.cell.focus();
      return null;
    });
  }, []);

  const pickTone = useCallback(
    (emoji: string, tone: number) => {
      setTonePick(null);
      onToneChange(tone);
      onSelect(emoji);
    },
    [onSelect, onToneChange],
  );

  const changeTab = useCallback((next: EmojiTab) => {
    setTab(next);
    setQuery("");
    if (bodyRef.current) bodyRef.current.scrollTop = 0;
  }, []);

  if (!catalog) {
    return <EmojiCatalogNotice status={status} onRetry={retry} />;
  }
  return (
    <div className="chat-emoji-picker">
      <EmojiSearchField searchRef={searchRef} query={query} onQueryChange={setQuery} />
      <EmojiCategoryBar
        categories={emojiCategories(populatedEmojiGroups(catalog), hasHistory)}
        tab={activeTab}
        onChange={changeTab}
      />
      <div className="chat-emoji-picker__sections" ref={bodyRef}>
        <EmojiSections sections={sections} tone={usage.tone} onActivate={activate} />
      </div>
      {tonePick && (
        <EmojiTonePalette
          entry={tonePick.entry}
          anchor={tonePick.anchor}
          tone={usage.tone}
          onSelect={pickTone}
          onDismiss={dismissTone}
        />
      )}
    </div>
  );
}
