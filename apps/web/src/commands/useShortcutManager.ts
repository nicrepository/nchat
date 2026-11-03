import { useLayoutEffect } from "react";

import type { CommandRegistry } from "./commandRegistry";
import {
  allowsShortcutInEditableTarget,
  resolveShortcut,
  shouldIgnoreShortcutTarget,
  type ShortcutScope,
} from "./shortcutManager";

/** Owns the sole document keydown listener for registered application shortcuts. */
export function useShortcutManager(registry: CommandRegistry, scopes: readonly ShortcutScope[]) {
  useLayoutEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.defaultPrevented || event.repeat) return;
      const command = resolveShortcut(event, scopes);
      if (!command) return;
      // A scheduling modal owns interaction until closed; navigation must not
      // hide an in-flight create or leave a second overlay above the form.
      if (event.target instanceof Element && event.target.closest("dialog[open]")) {
        event.preventDefault();
        return;
      }
      if (shouldIgnoreShortcutTarget(event.target) && !allowsShortcutInEditableTarget(command))
        return;
      event.preventDefault();
      registry.execute(command);
    }

    // Registered commands need to be resolved before rich editors get a
    // chance to consume the key in their bubble handlers. Native editor keys
    // such as Ctrl/Cmd+Z have no matching command and are left untouched.
    window.addEventListener("keydown", onKeyDown, { capture: true });
    return () => window.removeEventListener("keydown", onKeyDown, { capture: true });
  }, [registry, scopes]);
}
