import { fireEvent, render, screen } from "@testing-library/react";
import { StrictMode, useRef } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { useSessionPresentation } from "./useSessionPresentation";

const abandoned = vi.fn();
function View({
  suspended = false,
  pending = false,
  scope = "query:groups",
  removed = false,
  ready = !suspended,
}: {
  suspended?: boolean;
  pending?: boolean;
  scope?: string;
  removed?: boolean;
  ready?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const cancel = useSessionPresentation(ref, ready, suspended, {
    pending,
    scope,
    onAbandon: abandoned,
  });
  return (
    <div ref={ref} tabIndex={-1} data-testid="reading-root">
      {!suspended && (
        <>
          {!removed && (
            <button id="reading-control" onClick={cancel}>
              Controle anterior
            </button>
          )}
          {pending && (
            <div data-reading-recovery="true">
              <button onClick={cancel}>Cancelar recuperação</button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

let height: number;
beforeEach(() => {
  height = 1200;
  abandoned.mockClear();
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(() => height);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(200);
});
afterEach(() => {
  vi.restoreAllMocks();
});

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

it.each(["scroll", "cancel", "focus"])(
  "intentional %s abandons the old offset instead of restoring it later",
  (action) => {
    const view = render(<View />);
    const root = reading();
    view.rerender(<View suspended />);
    height = 400;
    view.rerender(<View pending />);
    if (action === "scroll") {
      root.scrollTop = 100;
      fireEvent.scroll(root);
    } else if (action === "cancel")
      fireEvent.click(screen.getByRole("button", { name: "Cancelar recuperação" }));
    else screen.getByRole("button", { name: "Controle anterior" }).focus();
    expect(abandoned).toHaveBeenCalledTimes(1);
    height = 1200;
    view.rerender(<View />);
    expect(root.scrollTop).toBe(action === "scroll" ? 100 : 0);
  },
);

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

it.each([false, true])(
  "manual pagination preserves the focus key across removed controls (StrictMode=%s)",
  (strict) => {
    const view = render(<View />, { wrapper: strict ? StrictMode : undefined });
    reading();
    fireEvent.click(screen.getByRole("button", { name: "Controle anterior" }));
    view.rerender(<View suspended />);
    expect(screen.queryByRole("button", { name: "Controle anterior" })).not.toBeInTheDocument();
    view.rerender(<View />);
    expect(screen.getByRole("button", { name: "Controle anterior" })).toHaveFocus();
    expect(screen.getByTestId("reading-root").scrollTop).toBe(600);
  },
);

it("external focus during confirmation does not discard the reading offset", () => {
  const view = render(
    <>
      <button>Outro controle</button>
      <View />
    </>,
  );
  const root = reading();
  view.rerender(
    <>
      <button>Outro controle</button>
      <View suspended />
    </>,
  );
  root.scrollTop = 0;
  screen.getByRole("button", { name: "Outro controle" }).focus();
  view.rerender(
    <>
      <button>Outro controle</button>
      <View />
    </>,
  );
  expect(screen.getByRole("button", { name: "Outro controle" })).toHaveFocus();
  expect(root.scrollTop).toBe(600);
});

it("moving focus outside while recovery is incomplete preserves scroll but not the old focus", () => {
  const view = render(
    <>
      <button>Outro controle</button>
      <View />
    </>,
  );
  const root = reading();
  view.rerender(
    <>
      <button>Outro controle</button>
      <View suspended />
    </>,
  );
  height = 400;
  view.rerender(
    <>
      <button>Outro controle</button>
      <View pending />
    </>,
  );
  // Recovery controls may lose focus to the shell without abandoning reading.
  screen.getByRole("button", { name: "Cancelar recuperação" }).focus();
  screen.getByRole("button", { name: "Outro controle" }).focus();
  height = 1200;
  view.rerender(
    <>
      <button>Outro controle</button>
      <View />
    </>,
  );
  expect(screen.getByRole("button", { name: "Outro controle" })).toHaveFocus();
  expect(root.scrollTop).toBe(600);
});

it("unmount and a new mount do not reuse an old focus or reading intention", () => {
  const view = render(<View />);
  reading();
  view.rerender(<View suspended />);
  view.unmount();
  render(<View />);
  expect(screen.getByTestId("reading-root").scrollTop).toBe(0);
  expect(screen.getByRole("button", { name: "Controle anterior" })).not.toHaveFocus();
});
