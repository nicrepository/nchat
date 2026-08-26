import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// JSDOM has no layout, but ProseMirror reads geometry while pasting and scrolling.
const emptyDOMRect = () => new DOMRect();
Range.prototype.getClientRects = () => [] as unknown as DOMRectList;
Range.prototype.getBoundingClientRect = emptyDOMRect;
Element.prototype.getBoundingClientRect = emptyDOMRect;
// Same reason: jsdom has no viewport to scroll, so its window.scrollTo only logs
// "Not implemented". ChatShell resets the document scroll when it mounts, which
// is a real browser behaviour and not something a test should have to silence.
window.scrollTo = () => undefined;

afterEach(() => {
  cleanup();
});
