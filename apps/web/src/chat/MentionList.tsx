import { Fragment, forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import type { MentionCandidate } from "./chatTypes";

export interface MentionListRef {
  onKeyDown: (event: KeyboardEvent) => boolean;
}

interface MentionListProps {
  items: MentionCandidate[];
  command: (item: MentionCandidate) => void;
  loadState?: "loading" | "ready" | "error";
}

const MentionList = forwardRef<MentionListRef, MentionListProps>(
  ({ items, command, loadState = "ready" }, ref) => {
    const [selectedIndex, setSelectedIndex] = useState(0);
    const optionRefs = useRef<Array<HTMLButtonElement | null>>([]);

    useEffect(() => setSelectedIndex(0), [items]);
    useEffect(() => {
      optionRefs.current[selectedIndex]?.scrollIntoView?.({ block: "nearest" });
    }, [selectedIndex]);

    const select = (index: number) => {
      const item = items[index];
      if (item) command(item);
    };

    const handleKey = (event: Pick<KeyboardEvent, "key">) => {
      if (!items.length) return event.key === "Escape";
      if (event.key === "ArrowUp") {
        setSelectedIndex((selectedIndex + items.length - 1) % items.length);
        return true;
      }
      if (event.key === "ArrowDown") {
        setSelectedIndex((selectedIndex + 1) % items.length);
        return true;
      }
      if (event.key === "Enter") {
        select(selectedIndex);
        return true;
      }
      return event.key === "Escape";
    };

    useImperativeHandle(ref, () => ({ onKeyDown: handleKey }));

    if (!items.length) {
      const message =
        loadState === "loading"
          ? "Carregando sugestões"
          : loadState === "error"
            ? "Não foi possível carregar sugestões"
            : "Nenhum resultado";
      return (
        <div className="mention-list__empty" role="status">
          {message}
        </div>
      );
    }

    return (
      <div className="mention-list" role="listbox" aria-label="Sugestões de menção">
        {items.map((item, index) => (
          <Fragment key={`${item.mentionType}:${item.id}`}>
            {(index === 0 || items[index - 1].mentionType !== item.mentionType) && (
              <div className="mention-list__heading" role="presentation">
                {item.mentionType === "user"
                  ? "Pessoas"
                  : item.mentionType === "channel"
                    ? "Canais"
                    : "Especial"}
              </div>
            )}
            <button
              ref={(element) => {
                optionRefs.current[index] = element;
              }}
              type="button"
              role="option"
              tabIndex={-1}
              aria-selected={index === selectedIndex}
              className={`mention-list__item${index === selectedIndex ? " mention-list__item--active" : ""}`}
              onMouseDown={(event) => {
                event.preventDefault();
                select(index);
              }}
            >
              <span className="mention-list__sigil" aria-hidden="true">
                {item.mentionType === "channel" ? "#" : "@"}
              </span>
              <span className="mention-list__label">{item.label}</span>
              {index === selectedIndex && (
                <span
                  className="material-symbols-outlined mention-list__selected"
                  aria-hidden="true"
                >
                  check
                </span>
              )}
            </button>
          </Fragment>
        ))}
      </div>
    );
  },
);

MentionList.displayName = "MentionList";

export default MentionList;
