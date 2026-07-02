import { forwardRef, useEffect, useImperativeHandle, useState } from "react";
import type { MentionCandidate } from "./chatTypes";

export interface MentionListRef {
  onKeyDown: (event: KeyboardEvent) => boolean;
}

interface MentionListProps {
  items: MentionCandidate[];
  command: (item: MentionCandidate) => void;
}

const MentionList = forwardRef<MentionListRef, MentionListProps>(({ items, command }, ref) => {
  const [selectedIndex, setSelectedIndex] = useState(0);

  useEffect(() => setSelectedIndex(0), [items]);

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

  if (!items.length) return <div className="mention-list__empty">Nenhum resultado</div>;

  return (
    <div className="mention-list" role="listbox" aria-label="Sugestões de menção">
      {items.map((item, index) => (
        <button
          key={`${item.mentionType}:${item.id}`}
          type="button"
          role="option"
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
          {item.label}
        </button>
      ))}
    </div>
  );
});

MentionList.displayName = "MentionList";

export default MentionList;
