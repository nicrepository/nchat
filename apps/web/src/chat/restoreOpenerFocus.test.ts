import { afterEach, expect, it, vi } from "vitest";
import { restoreOpenerFocus } from "./restoreOpenerFocus";

afterEach(() => document.querySelectorAll("[data-focus-fixture]").forEach((node) => node.remove()));
it.each(["detached", "hidden", "ancestor-hidden", "disabled", "inert", "visibility"])(
  "restores fallback when opener is %s without scrolling",
  (mode) => {
    const container = document.createElement("div");
    container.dataset.focusFixture = "true";
    const opener = document.createElement("button");
    const fallback = document.createElement("button");
    container.append(opener, fallback);
    document.body.append(container);
    if (mode === "detached") opener.remove();
    if (mode === "hidden") opener.style.display = "none";
    if (mode === "ancestor-hidden") {
      const parent = document.createElement("div");
      parent.hidden = true;
      container.append(parent);
      parent.append(opener);
    }
    if (mode === "disabled") opener.disabled = true;
    if (mode === "inert") opener.setAttribute("inert", "");
    if (mode === "visibility") opener.style.visibility = "hidden";
    const focus = vi.spyOn(fallback, "focus");
    expect(restoreOpenerFocus(opener, fallback)).toBe(true);
    expect(document.activeElement).toBe(fallback);
    expect(focus).toHaveBeenCalledWith({ preventScroll: true });
  },
);
it("prioritizes a live opener and respects an inert fallback", () => {
  const container = document.createElement("div");
  container.dataset.focusFixture = "true";
  const opener = document.createElement("button");
  const fallback = document.createElement("button");
  container.append(opener, fallback);
  document.body.append(container);
  expect(restoreOpenerFocus(opener, fallback)).toBe(true);
  expect(document.activeElement).toBe(opener);
  fallback.setAttribute("inert", "");
  expect(restoreOpenerFocus(null, fallback)).toBe(false);
  expect(document.activeElement).toBe(opener);
});

it("falls back when a connected element cannot accept focus", () => {
  const container = document.createElement("div");
  container.dataset.focusFixture = "true";
  const opener = document.createElement("span");
  const fallback = document.createElement("button");
  container.append(opener, fallback);
  document.body.append(container);
  expect(restoreOpenerFocus(opener, fallback)).toBe(true);
  expect(document.activeElement).toBe(fallback);
});
