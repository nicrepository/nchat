import type { CommandId } from "./commandRegistry";

export type ShortcutScope = "global" | "composer";

interface ShortcutEvent {
  key: string;
  altKey?: boolean;
  ctrlKey?: boolean;
  metaKey?: boolean;
  shiftKey?: boolean;
}

interface ShortcutDefinition {
  command: CommandId;
  scope: ShortcutScope;
  matches: (event: ShortcutEvent) => boolean;
}

const shortcuts: ShortcutDefinition[] = [
  {
    command: "search.open",
    scope: "global",
    matches: (event) =>
      Boolean(event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "k",
  },
  {
    command: "shortcuts.open",
    scope: "global",
    matches: (event) =>
      Boolean(event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "/",
  },
  {
    command: "conversation.previous",
    scope: "global",
    matches: (event) =>
      event.altKey === true && !event.ctrlKey && !event.metaKey && event.key === "ArrowUp",
  },
  {
    command: "conversation.next",
    scope: "global",
    matches: (event) =>
      event.altKey === true && !event.ctrlKey && !event.metaKey && event.key === "ArrowDown",
  },
  {
    command: "conversation.historyBack",
    scope: "global",
    matches: (event) =>
      event.altKey === true && !event.ctrlKey && !event.metaKey && event.key === "ArrowLeft",
  },
  {
    command: "conversation.historyForward",
    scope: "global",
    matches: (event) =>
      event.altKey === true && !event.ctrlKey && !event.metaKey && event.key === "ArrowRight",
  },
];

export function resolveShortcut(
  event: ShortcutEvent,
  scopes: readonly ShortcutScope[],
): CommandId | null {
  return (
    shortcuts.find((shortcut) => scopes.includes(shortcut.scope) && shortcut.matches(event))
      ?.command ?? null
  );
}

/** Commands without native editor semantics remain available while the composer has focus. */
export function allowsShortcutInEditableTarget(command: CommandId): boolean {
  return (
    command === "shortcuts.open" ||
    command === "conversation.previous" ||
    command === "conversation.next" ||
    command === "conversation.historyBack" ||
    command === "conversation.historyForward"
  );
}

/** Input-like elements retain native editing and browser shortcuts by default. */
export function shouldIgnoreShortcutTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  return (
    (target instanceof HTMLElement && target.contentEditable === "true") ||
    Boolean(target.closest("input, textarea, select, [contenteditable]"))
  );
}
