import { expect, type ConsoleMessage, type Page } from "@playwright/test";

export type AllowedConsoleError = (message: ConsoleMessage) => boolean;

const unexpectedErrors = new WeakMap<Page, string[]>();

/** Captures browser failures while allowing only errors named by the caller. */
export function captureBrowserErrors(page: Page, allowed: AllowedConsoleError[] = []) {
  const errors: string[] = [];
  unexpectedErrors.set(page, errors);
  page.on("pageerror", (error) => errors.push(`pageerror: ${error.message}`));
  page.on("console", (message) => {
    if (message.type() !== "error" || allowed.some((allow) => allow(message))) return;
    errors.push(`console.error: ${message.text()}`);
  });
}

export function expectNoUnexpectedBrowserErrors(page: Page) {
  expect(unexpectedErrors.get(page)).toEqual([]);
}

export function allowsServiceUnavailable(path: string): AllowedConsoleError {
  return (message) =>
    message.text() ===
      "Failed to load resource: the server responded with a status of 503 (Service Unavailable)" &&
    message.location().url.includes(path);
}
