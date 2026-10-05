import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

export default function OwnershipMemberMenu({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  const trigger = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<{ top: number; left: number } | null>(null);
  function close() {
    setPosition(null);
    trigger.current?.focus();
  }
  useEffect(() => {
    if (!position) return;
    popup.current?.querySelector<HTMLButtonElement>("button")?.focus();
    function outside(event: PointerEvent) {
      const target = event.target as Node;
      if (!popup.current?.contains(target) && !trigger.current?.contains(target)) setPosition(null);
    }
    function reposition() {
      setPosition(null);
    }
    document.addEventListener("pointerdown", outside);
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
    };
  }, [position]);
  function toggle() {
    if (position) {
      close();
      return;
    }
    const rect = trigger.current!.getBoundingClientRect();
    setPosition({
      top: Math.max(8, Math.min(rect.bottom + 6, window.innerHeight - 190)),
      left: Math.max(8, Math.min(rect.right - 248, window.innerWidth - 256)),
    });
  }
  return (
    <>
      <button
        ref={trigger}
        type="button"
        className="ownership-roster__more"
        aria-label={label}
        aria-expanded={position !== null}
        onClick={toggle}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          more_horiz
        </span>
      </button>
      {position &&
        createPortal(
          <div
            ref={popup}
            className="ownership-member-menu chat-theme"
            style={position}
            role="group"
            aria-label={label}
            onClickCapture={() => trigger.current?.focus()}
            onClick={close}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.stopPropagation();
                close();
              }
            }}
          >
            {children}
          </div>,
          document.body,
        )}
    </>
  );
}
