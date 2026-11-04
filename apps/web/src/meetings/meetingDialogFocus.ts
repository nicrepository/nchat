import type { KeyboardEvent } from "react";

/** Keep Tab in the form, including when initial focus is on its heading. */
export function trapMeetingFocus(event: KeyboardEvent<HTMLDialogElement>) {
  if (event.key !== "Tab") return;
  const controls = Array.from(
    event.currentTarget.querySelectorAll<HTMLElement>(
      ":is(button, input, select, textarea):enabled",
    ),
  );
  const index = controls.indexOf(document.activeElement as HTMLElement);
  const backwards = event.shiftKey && index <= 0;
  const forwards = !event.shiftKey && index === controls.length - 1;
  if (!backwards && !forwards) return;
  event.preventDefault();
  const target = backwards ? controls.at(-1) : controls[0];
  target?.focus();
}
