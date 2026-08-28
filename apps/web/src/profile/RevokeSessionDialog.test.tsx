import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
});
