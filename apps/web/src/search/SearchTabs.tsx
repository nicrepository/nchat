import { useRef, type KeyboardEvent } from "react";

import { SEARCH_PANEL_ID, TAB_LABELS, tabId } from "./searchLabels";
import { SEARCH_CATEGORIES, type SearchTab } from "./searchTypes";

const TABS: SearchTab[] = ["all", ...SEARCH_CATEGORIES];

/**
 * WAI-ARIA tabs with automatic activation: one tab stop (roving tabindex),
 * ArrowLeft/ArrowRight wrap around, Home/End jump to the ends. On a phone the
 * row scrolls sideways; every label stays whole.
 */
export default function SearchTabs({
  active,
  onChange,
}: {
  active: SearchTab;
  onChange: (tab: SearchTab) => void;
}) {
  const refs = useRef(new Map<SearchTab, HTMLButtonElement>());

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const index = TABS.indexOf(active);
    const next = {
      ArrowRight: TABS[(index + 1) % TABS.length],
      ArrowLeft: TABS[(index - 1 + TABS.length) % TABS.length],
      Home: TABS[0],
      End: TABS[TABS.length - 1],
    }[event.key];
    if (!next) return;
    event.preventDefault();
    onChange(next);
    refs.current.get(next)?.focus();
  }

  return (
    <div
      className="global-search__tabs"
      role="tablist"
      aria-label="Tipo de resultado"
      onKeyDown={onKeyDown}
    >
      {TABS.map((tab) => {
        const selected = tab === active;
        return (
          <button
            key={tab}
            ref={(node) => {
              if (node) refs.current.set(tab, node);
              else refs.current.delete(tab);
            }}
            type="button"
            role="tab"
            id={tabId(tab)}
            aria-selected={selected}
            aria-controls={SEARCH_PANEL_ID}
            tabIndex={selected ? 0 : -1}
            className={`global-search__tab${selected ? " global-search__tab--active" : ""}`}
            onClick={() => onChange(tab)}
          >
            {TAB_LABELS[tab]}
          </button>
        );
      })}
    </div>
  );
}
