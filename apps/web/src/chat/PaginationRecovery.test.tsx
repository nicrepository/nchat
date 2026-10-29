import { fireEvent, render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { expect, it, vi } from "vitest";

import PaginationRecovery from "./PaginationRecovery";

function Recovery({ busy = false, remaining = 2 }: { busy?: boolean; remaining?: number }) {
  return (
    <PaginationRecovery
      remaining={remaining}
      hasMore
      busy={busy}
      onRecover={() => {}}
      onCancel={() => {}}
    />
  );
}

function nativeDisabledBlur() {
  document.body.tabIndex = -1;
  document.body.focus();
  document.body.removeAttribute("tabindex");
  expect(document.body).toHaveFocus();
}

it.each([false, true])(
  "restores the initiating button after a disabled batch (StrictMode=%s)",
  (strict) => {
    const view = render(<Recovery />, { wrapper: strict ? StrictMode : undefined });
    const button = screen.getByRole("button", { name: "Recuperar contexto de leitura" });
    button.focus();
    fireEvent.click(button);
    view.rerender(<Recovery busy />);
    expect(button).toBeDisabled();
    // JSDOM does not model the native blur caused by disabling a focused button.
    nativeDisabledBlur();
    view.rerender(<Recovery />);
    expect(button).toBeEnabled();
    expect(button).toHaveFocus();
  },
);

it("does not take focus from a control chosen while a batch was pending", () => {
  const view = render(
    <>
      <button>Outro controle</button>
      <Recovery />
    </>,
  );
  const button = screen.getByRole("button", { name: "Recuperar contexto de leitura" });
  button.focus();
  fireEvent.click(button);
  view.rerender(
    <>
      <button>Outro controle</button>
      <Recovery busy />
    </>,
  );
  nativeDisabledBlur();
  screen.getByRole("button", { name: "Outro controle" }).focus();
  view.rerender(
    <>
      <button>Outro controle</button>
      <Recovery />
    </>,
  );
  expect(screen.getByRole("button", { name: "Outro controle" })).toHaveFocus();
});

it("keeps native disabled semantics and blocks repeated mouse and keyboard activation", () => {
  const recover = vi.fn();
  const view = render(
    <PaginationRecovery
      remaining={7}
      hasMore
      busy={false}
      onRecover={recover}
      onCancel={() => {}}
    />,
  );
  const button = screen.getByRole("button", { name: "Recuperar contexto de leitura" });
  button.focus();
  fireEvent.click(button);
  view.rerender(
    <PaginationRecovery remaining={7} hasMore busy onRecover={recover} onCancel={() => {}} />,
  );
  fireEvent.click(button);
  fireEvent.keyDown(button, { key: "Enter" });
  fireEvent.keyDown(button, { key: " " });
  expect(button).toBeDisabled();
  expect(recover).toHaveBeenCalledTimes(1);
});

it("does not focus an old button after the owning view has been replaced", () => {
  const view = render(<Recovery key="u1" />);
  const button = screen.getByRole("button", { name: "Recuperar contexto de leitura" });
  button.focus();
  fireEvent.click(button);
  view.rerender(<Recovery key="u1" busy />);
  nativeDisabledBlur();
  view.rerender(<Recovery key="u2" />);
  expect(button).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Recuperar contexto de leitura" })).not.toHaveFocus();
});

it("leaves final focus to the owner when the recovery button disappears", () => {
  const view = render(<Recovery />);
  const button = screen.getByRole("button", { name: "Recuperar contexto de leitura" });
  button.focus();
  fireEvent.click(button);
  view.rerender(<Recovery busy />);
  nativeDisabledBlur();
  view.rerender(<Recovery remaining={0} />);
  expect(button).not.toBeInTheDocument();
  expect(document.body).toHaveFocus();
});
