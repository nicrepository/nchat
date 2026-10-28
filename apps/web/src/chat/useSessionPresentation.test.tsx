import { fireEvent, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { useSessionPresentation } from "./useSessionPresentation";

const abandoned = vi.fn();
function View({ suspended = false, pending = false, scope = "query:groups", removed = false, ready = !suspended }: {
  suspended?: boolean;
  pending?: boolean;
  scope?: string;
  removed?: boolean;
  ready?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const cancel = useSessionPresentation(ref, ready, suspended, { pending, scope, onAbandon: abandoned });
  return <div ref={ref} tabIndex={-1} data-testid="reading-root">
    {!suspended && <>
      {!removed && <button id="reading-control">Controle anterior</button>}
      {pending && <div data-reading-recovery="true"><button onClick={cancel}>Cancelar recuperação</button></div>}
    </>}
  </div>;
}

let height: number;
beforeEach(() => {
  height = 1200;
  abandoned.mockClear();
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(() => height);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(200);
});
afterEach(() => { vi.restoreAllMocks(); });

function reading() {
  const root = screen.getByTestId("reading-root");
  root.scrollTop = 600;
  fireEvent.scroll(root);
  screen.getByRole("button", { name: "Controle anterior" }).focus();
  return root;
}

it("keeps the old offset pending until the depth and scroll range are available", () => {
  const view = render(<View />);
  const root = reading();
  view.rerender(<View suspended />);
  height = 400;
  view.rerender(<View pending />);
  expect(root.scrollTop).toBe(0);
  expect(screen.getByRole("button", { name: "Controle anterior" })).not.toHaveFocus();
  height = 1200;
  view.rerender(<View />);
  expect(root.scrollTop).toBe(600);
  expect(screen.getByRole("button", { name: "Controle anterior" })).toHaveFocus();
});

it.each(["scroll", "cancel", "focus"])("intentional %s abandons the old offset instead of restoring it later", (action) => {
  const view = render(<View />);
  const root = reading();
  view.rerender(<View suspended />);
  height = 400;
  view.rerender(<View pending />);
  if (action === "scroll") { root.scrollTop = 100; fireEvent.scroll(root); }
  else if (action === "cancel") fireEvent.click(screen.getByRole("button", { name: "Cancelar recuperação" }));
  else screen.getByRole("button", { name: "Controle anterior" }).focus();
  expect(abandoned).toHaveBeenCalledTimes(1);
  height = 1200;
  view.rerender(<View />);
  expect(root.scrollTop).toBe(action === "scroll" ? 100 : 0);
});

it("a new query/tab has no previous reading intention", () => {
  const view = render(<View />);
  const root = reading();
  view.rerender(<View suspended />);
  view.rerender(<View scope="other:users" />);
  expect(root.scrollTop).toBe(0);
  expect(screen.getByRole("button", { name: "Controle anterior" })).not.toHaveFocus();
});

it("intentional focus while controls are visible but results are still loading cancels restoration", () => {
  const view = render(<View />);
  const root = reading();
  view.rerender(<View suspended />);
  view.rerender(<View ready={false} />);
  // Model the visible field's new reading position; JSDOM does not scroll on focus.
  root.scrollTop = 0;
  screen.getByRole("button", { name: "Controle anterior" }).focus();
  expect(abandoned).toHaveBeenCalledTimes(1);
  view.rerender(<View />);
  expect(root.scrollTop).toBe(0);
});

it("removed content uses the beginning and an existing root instead of an invalid offset/control", () => {
  const view = render(<View />);
  const root = reading();
  view.rerender(<View suspended />);
  height = 300;
  view.rerender(<View removed />);
  expect(root.scrollTop).toBe(0);
  expect(root).toHaveFocus();
});
