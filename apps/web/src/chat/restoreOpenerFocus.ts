/** Restore to a live, visible control; browser layout decides responsive visibility. */
export function restoreOpenerFocus(
  opener: HTMLElement | null,
  fallback: HTMLElement | null,
): boolean {
  for (const target of [opener, fallback]) {
    if (!canReceiveFocus(target)) continue;
    target.focus({ preventScroll: true });
    if (document.activeElement === target) return true;
  }
  return false;
}

function canReceiveFocus(target: HTMLElement | null): target is HTMLElement {
  if (!target?.isConnected || target.closest("[hidden], [inert], [aria-hidden='true'], :disabled"))
    return false;
  for (let node: HTMLElement | null = target; node; node = node.parentElement) {
    const style = window.getComputedStyle(node);
    if (style.display === "none" || style.visibility === "hidden") return false;
  }
  return true;
}
