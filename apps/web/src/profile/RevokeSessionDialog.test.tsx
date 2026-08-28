import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act } from "react";
import { describe, expect, it, vi } from "vitest";

import RevokeSessionDialog from "./RevokeSessionDialog";

describe("RevokeSessionDialog", () => {
  it("focuses Cancelar first for the single-session variant", () => {
    render(<RevokeSessionDialog target="single" onClose={vi.fn()} onConfirm={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Cancelar" })).toHaveFocus();
    expect(screen.getByRole("heading")).toHaveTextContent(/revogar sessão\?/i);
  });

  it("shows the 'others' copy and calls onConfirm once, then onClose", async () => {
    let resolveConfirm!: () => void;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveConfirm = resolve;
        }),
    );
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<RevokeSessionDialog target="others" onClose={onClose} onConfirm={onConfirm} />);
    expect(screen.getByRole("heading")).toHaveTextContent(/revogar outras sessões\?/i);
    const confirmButton = screen.getByRole("button", { name: /revogar sessões/i });
    await user.click(confirmButton);
    await user.click(confirmButton);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    resolveConfirm();
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("keeps the dialog open on a rejected onConfirm", async () => {
    const onConfirm = vi.fn().mockRejectedValueOnce(new Error("fail"));
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<RevokeSessionDialog target="single" onClose={onClose} onConfirm={onConfirm} />);
    await user.click(screen.getByRole("button", { name: /revogar sessão/i }));
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    expect(onClose).not.toHaveBeenCalled();
  });

  it("closes on Escape", () => {
    const onClose = vi.fn();
    render(<RevokeSessionDialog target="single" onClose={onClose} onConfirm={vi.fn()} />);
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("ignores Escape while a revoke is in flight", async () => {
    const onClose = vi.fn();
    const onConfirm = vi.fn(() => new Promise<void>(() => {})); // never resolves
    const user = userEvent.setup();
    render(<RevokeSessionDialog target="single" onClose={onClose} onConfirm={onConfirm} />);
    await user.click(screen.getByRole("button", { name: /revogar sessão/i }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Cancelar" })).toBeDisabled());
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
  });

  it("wraps focus forward from the last button back to the first on Tab", () => {
    render(<RevokeSessionDialog target="single" onClose={vi.fn()} onConfirm={vi.fn()} />);
    const cancel = screen.getByRole("button", { name: "Cancelar" });
    const confirm = screen.getByRole("button", { name: /revogar sessão/i });
    confirm.focus();
    expect(confirm).toHaveFocus();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Tab" });
    expect(cancel).toHaveFocus();
  });

  it("wraps focus backward from the first button to the last on Shift+Tab", () => {
    render(<RevokeSessionDialog target="single" onClose={vi.fn()} onConfirm={vi.fn()} />);
    const cancel = screen.getByRole("button", { name: "Cancelar" });
    const confirm = screen.getByRole("button", { name: /revogar sessão/i });
    expect(cancel).toHaveFocus();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Tab", shiftKey: true });
    expect(confirm).toHaveFocus();
  });

  it("does not move focus on Shift+Tab when focus is already on the last button", () => {
    render(<RevokeSessionDialog target="single" onClose={vi.fn()} onConfirm={vi.fn()} />);
    const confirm = screen.getByRole("button", { name: /revogar sessão/i });
    confirm.focus();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Tab", shiftKey: true });
    expect(confirm).toHaveFocus();
  });

  it("does nothing on Tab when every button is disabled during a pending submit", async () => {
    const onConfirm = vi.fn(() => new Promise<void>(() => {})); // never resolves
    const user = userEvent.setup();
    render(<RevokeSessionDialog target="single" onClose={vi.fn()} onConfirm={onConfirm} />);
    await user.click(screen.getByRole("button", { name: /revogar sessão/i }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Cancelar" })).toBeDisabled());
    // Should not throw even though no button matches "button:not(:disabled)".
    expect(() =>
      fireEvent.keyDown(screen.getByRole("dialog"), { key: "Tab" }),
    ).not.toThrow();
  });

  it("ignores non-Tab, non-Escape keys", () => {
    const onClose = vi.fn();
    render(<RevokeSessionDialog target="single" onClose={onClose} onConfirm={vi.fn()} />);
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "a" });
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Cancelar" })).toHaveFocus();
  });

  it("closes via backdrop click", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<RevokeSessionDialog target="single" onClose={onClose} onConfirm={vi.fn()} />);
    const backdrop = document.querySelector(".revoke-session__backdrop") as HTMLElement;
    await user.click(backdrop);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("does not call onClose or throw when the revoke resolves after unmount", async () => {
    let resolveConfirm!: () => void;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveConfirm = resolve;
        }),
    );
    const onClose = vi.fn();
    const user = userEvent.setup();
    const { unmount } = render(
      <RevokeSessionDialog target="single" onClose={onClose} onConfirm={onConfirm} />,
    );
    await user.click(screen.getByRole("button", { name: /revogar sessão/i }));
    unmount();
    // Resolving after unmount must not touch state on the unmounted component.
    expect(() => resolveConfirm()).not.toThrow();
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    expect(onClose).not.toHaveBeenCalled();
  });

  it("ignores a second confirm click fired before the disabled attribute commits", async () => {
    let resolveConfirm!: () => void;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveConfirm = resolve;
        }),
    );
    const onClose = vi.fn();
    render(<RevokeSessionDialog target="single" onClose={onClose} onConfirm={onConfirm} />);
    const confirmButton = screen.getByRole("button", { name: /revogar sessão/i });
    // Two clicks dispatched inside one `act` batch land before React commits
    // the `disabled` attribute from the first click's setPending(true). Only
    // the submittingRef guard in confirm() stops the second call from
    // re-entering while the first request is still in flight.
    act(() => {
      confirmButton.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      confirmButton.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(onConfirm).toHaveBeenCalledTimes(1);
    resolveConfirm();
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it("does not throw when the revoke rejects after unmount", async () => {
    let rejectConfirm!: (error: Error) => void;
    const onConfirm = vi.fn(
      () =>
        new Promise<void>((_resolve, reject) => {
          rejectConfirm = reject;
        }),
    );
    const user = userEvent.setup();
    const { unmount } = render(
      <RevokeSessionDialog target="single" onClose={vi.fn()} onConfirm={onConfirm} />,
    );
    await user.click(screen.getByRole("button", { name: /revogar sessão/i }));
    unmount();
    expect(() => rejectConfirm(new Error("fail"))).not.toThrow();
    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
  });
});
