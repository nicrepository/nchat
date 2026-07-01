import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// JSDOM has no layout, but ProseMirror reads geometry while pasting and scrolling.
const emptyDOMRect = () => new DOMRect();
Range.prototype.getClientRects = () => [] as unknown as DOMRectList;
Range.prototype.getBoundingClientRect = emptyDOMRect;
Element.prototype.getBoundingClientRect = emptyDOMRect;

afterEach(() => {
  cleanup();
});
