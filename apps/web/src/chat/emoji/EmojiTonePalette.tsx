/**
 * The skin-tone choice for one emoji (issue #496, CQ round 3).
 *
 * It replaces the global "Padrão" select that used to sit in the picker header.
 * A tone chosen there applied to emoji the reader could not see, and cost header
 * space that belongs to the search field; here the choice is made against the
 * emoji it applies to, at the moment it applies.
 *
 * Rendered through a portal and placed against the emoji's own box, so it is
 * never clipped by the picker's scroll container and never moves the grid under
 * the reader's cursor.
 */

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";

import { skinToneLabels } from "./emojiCategories";
import { withSkinTone, type EmojiEntry } from "./emojiCatalog";
import type { VisibleBounds } from "./useAnchoredPicker";

export interface EmojiTonePaletteProps {
  entry: EmojiEntry;
  /** The emoji cell this palette belongs to, in viewport coordinates. */
  anchor: DOMRect;
  /** The reader's remembered tone, marked as current. */
  tone: number;
  onSelect: (emoji: string, tone: number) => void;
  onDismiss: () => void;
  /** The message whose reaction picker opened this palette: see EmojiPicker. */
  reactionFor?: string;
  /**
   * The band the palette must stay inside, when the window is not it (issue
   * #1152): a touch reaction picker passes the part of the visual viewport its
   * menu does not cover. Absent for any other host.
   */
  bounds?: () => VisibleBounds;
}

/** The window itself: where a palette is kept unless its host knows better. */
function windowBounds(): VisibleBounds {
  return { top: 0, bottom: window.innerHeight, left: 0, right: window.innerWidth };
}

/**
 * Places the palette over its emoji: centred, above when there is room and below
 * when there is not, and always whole inside its band — over the emoji itself
 * when the band is too short for either side.
 */
function placePalette(element: HTMLElement, anchor: DOMRect, bounds: VisibleBounds): void {
  const padding = 8;
  const gap = 6;
  const box = element.getBoundingClientRect();
  const left = Math.min(
    Math.max(bounds.left + padding, anchor.left + anchor.width / 2 - box.width / 2),
    bounds.right - box.width - padding,
  );
  const above = anchor.top - box.height - gap;
  const fitsAbove = above >= bounds.top + padding;
  const top = fitsAbove
    ? above
    : Math.min(anchor.bottom + gap, bounds.bottom - box.height - padding);
  element.style.left = `${left}px`;
  element.style.top = `${top}px`;
  // The origin points back at the emoji, so the open animation reads as the
  // palette growing out of the thing it is about.
  element.style.transformOrigin = fitsAbove ? "center bottom" : "center top";
  element.style.visibility = "visible";
}

const toneSteps: Record<string, number> = {
  ArrowRight: 1,
  ArrowDown: 1,
  ArrowLeft: -1,
  ArrowUp: -1,
};

const lastTone = skinToneLabels.length - 1;

/** Movement within the six tones, refusing to leave either end. */
function nextTone(key: string, tone: number): number | null {
  if (key === "Home") return tone === 0 ? null : 0;
  if (key === "End") return tone === lastTone ? null : lastTone;
  const step = toneSteps[key];
  if (step === undefined) return null;
  const target = tone + step;
  return target >= 0 && target <= lastTone ? target : null;
}

export default function EmojiTonePalette({
  entry,
  anchor,
  tone,
  onSelect,
  onDismiss,
  reactionFor,
  bounds = windowBounds,
}: EmojiTonePaletteProps) {
  const paletteRef = useRef<HTMLDivElement>(null);
  const [active, setActive] = useState(tone);

  useLayoutEffect(() => {
    if (paletteRef.current) placePalette(paletteRef.current, anchor, bounds());
  }, [anchor, bounds]);

  // Opening moves focus onto the tone the reader last chose, so the palette is
  // usable from the keyboard the instant it appears.
  useEffect(() => {
    paletteRef.current?.querySelectorAll("button")[tone]?.focus();
  }, [tone]);

  useEffect(() => {
    const dismiss = (event: MouseEvent) => {
      if (!paletteRef.current?.contains(event.target as Node)) onDismiss();
    };
    document.addEventListener("mousedown", dismiss);
    return () => document.removeEventListener("mousedown", dismiss);
  }, [onDismiss]);

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      event.stopPropagation();
      onDismiss();
      return;
    }
    // A sub-overlay with a single Tab stop (the tones are roving, moved with
    // the arrows). It is portalled to <body>, so a Tab let through would leave
    // the end of the document — and the modal hosting the picker with it.
    // Tab therefore stays here; Escape or a choice is the way out, and both
    // hand focus back to the emoji (issue #1026).
    if (event.key === "Tab") {
      event.preventDefault();
      event.stopPropagation();
      return;
    }
    const target = nextTone(event.key, active);
    if (target === null) return;
    event.preventDefault();
    setActive(target);
    paletteRef.current?.querySelectorAll("button")[target]?.focus();
  };

  return createPortal(
    <div
      ref={paletteRef}
      // Portalled out of the conversation, so it carries the chat scope with it
      // — see the picker's own container for why.
      className="chat-theme chat-emoji-tone"
      data-reaction-picker-for={reactionFor}
      role="dialog"
      aria-label={`Tom de pele para ${entry.label}`}
      style={{ visibility: "hidden" }}
      onKeyDown={handleKeyDown}
    >
      {skinToneLabels.map((label, index) => {
        const variant = withSkinTone(entry, index);
        return (
          <button
            key={label}
            type="button"
            className="chat-emoji-tone__option"
            aria-label={`${entry.label} — ${label}`}
            aria-current={index === tone}
            tabIndex={index === active ? 0 : -1}
            onClick={() => onSelect(variant, index)}
          >
            <span aria-hidden="true">{variant}</span>
          </button>
        );
      })}
    </div>,
    document.body,
  );
}
