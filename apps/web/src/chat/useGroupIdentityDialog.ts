import { useCallback, useRef, useState } from "react";

/**
 * Which group the "Alterar identidade" dialog is open for, and where focus
 * goes when it closes (issue #1026).
 *
 * The menu item that chose the action no longer exists once the menu closes,
 * so focus returns to the control that opened that menu — the row's
 * "Mais opções" button the action was run from — whether the dialog was
 * dismissed or saved. Nothing is restored when no trigger was handed over.
 */
export function useGroupIdentityDialog() {
  const [targetId, setTargetId] = useState<string | null>(null);
  const triggerRef = useRef<HTMLElement | null>(null);

  const open = useCallback((groupId: string, trigger: HTMLElement | null) => {
    triggerRef.current = trigger;
    setTargetId(groupId);
  }, []);

  const close = useCallback(() => {
    setTargetId(null);
    triggerRef.current?.focus();
    triggerRef.current = null;
  }, []);

  return { targetId, open, close };
}
