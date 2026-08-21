import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SEARCH_DEBOUNCE_MS, useDebouncedValue } from "./useDebouncedValue";

afterEach(() => {
  vi.useRealTimers();
});

function Probe() {
  const [value, setValue] = useState("");
  const debounced = useDebouncedValue(value);
  return (
    <div>
      <input aria-label="busca" value={value} onChange={(e) => setValue(e.target.value)} />
      <span data-testid="settled">{debounced}</span>
    </div>
  );
}

describe("useDebouncedValue", () => {
  it("holds the value until typing stops", () => {
    vi.useFakeTimers();
    render(<Probe />);
    const input = screen.getByLabelText("busca");

    // Three keystrokes in quick succession: only the last one settles.
    for (const value of ["a", "an", "ana"]) {
      act(() => {
        fireEvent.change(input, { target: { value } });
      });
      act(() => {
        vi.advanceTimersByTime(SEARCH_DEBOUNCE_MS / 3);
      });
    }
    expect(screen.getByTestId("settled")).toHaveTextContent("");

    act(() => {
      vi.advanceTimersByTime(SEARCH_DEBOUNCE_MS);
    });
    expect(screen.getByTestId("settled")).toHaveTextContent("ana");
  });

  it("clears the pending timer on unmount", () => {
    vi.useFakeTimers();
    const { unmount } = render(<Probe />);
    unmount();
    act(() => {
      vi.advanceTimersByTime(SEARCH_DEBOUNCE_MS * 2);
    });
  });
});
