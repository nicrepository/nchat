import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { useBackButton } from "./conversationBack";

function Header({ destination, goBack }: { destination: string; goBack: () => boolean }) {
  const { ref, onBack } = useBackButton(goBack, destination);
  return (
    <>
      <button key={destination} ref={ref} onClick={onBack}>
        Voltar
      </button>
      <button>Outro controle</button>
    </>
  );
}

describe("useBackButton focus recovery", () => {
  it.each([1, 2])("keeps focus recovery pending after %i activation(s)", (activations) => {
    const goBack = vi.fn().mockReturnValueOnce(true).mockReturnValue(false);
    const { rerender } = render(<Header destination="dm:B" goBack={goBack} />);
    const original = screen.getByRole("button", { name: "Voltar" });
    original.focus();
    for (let press = 0; press < activations; press += 1) fireEvent.click(original);

    // A different header replaces the focused control when the trip arrives.
    rerender(<Header destination="channel:A" goBack={goBack} />);

    expect(original).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Voltar" })).toHaveFocus();
    expect(goBack).toHaveBeenCalledTimes(activations);
  });

  it("does not transfer an unmounted conversation's pending recovery to a new mount", async () => {
    const goBack = vi.fn(() => true);
    const { unmount } = render(<Header destination="dm:B" goBack={goBack} />);
    const original = screen.getByRole("button", { name: "Voltar" });
    original.focus();
    fireEvent.click(original);
    const focus = vi.spyOn(original, "focus");

    unmount();
    render(<Header destination="channel:C" goBack={goBack} />);
    await act(async () => {});

    expect(focus).not.toHaveBeenCalled();
    expect(original).not.toBeInTheDocument();
    expect(document.body).toHaveFocus();
    expect(screen.getByRole("button", { name: "Voltar" })).not.toHaveFocus();
    focus.mockRestore();
  });

  it("keeps intentional focus and consumes the recovery before another conversation", () => {
    const goBack = vi.fn().mockReturnValueOnce(true).mockReturnValue(false);
    const { rerender } = render(<Header destination="dm:B" goBack={goBack} />);
    const original = screen.getByRole("button", { name: "Voltar" });
    original.focus();
    fireEvent.click(original);
    fireEvent.click(original);
    const chosen = screen.getByRole("button", { name: "Outro controle" });
    chosen.focus();

    rerender(<Header destination="channel:A" goBack={goBack} />);
    expect(chosen).toHaveFocus();

    chosen.blur();
    rerender(<Header destination="channel:C" goBack={goBack} />);
    expect(document.body).toHaveFocus();
    expect(screen.getByRole("button", { name: "Voltar" })).not.toHaveFocus();
  });
});
