import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { trapMeetingFocus } from "./meetingDialogFocus";

describe("meeting focus trap", () => {
  it("cycles around all native form fields and the initial heading", () => {
    render(
      <dialog open onKeyDown={trapMeetingFocus}>
        <h2 tabIndex={-1}>Heading</h2>
        <button>First</button>
        <input aria-label="Input" />
        <select aria-label="Select">
          <option>A</option>
        </select>
        <textarea aria-label="Text" />
        <button disabled>Disabled</button>
        <button>Last</button>
      </dialog>,
    );
    const first = screen.getByRole("button", { name: "First" });
    const last = screen.getByRole("button", { name: "Last" });
    expect(
      Array.from(
        screen.getByRole("dialog").querySelectorAll(":is(button, input, select, textarea):enabled"),
      ).map((el) => el.tagName),
    ).toEqual(["BUTTON", "INPUT", "SELECT", "TEXTAREA", "BUTTON"]);
    screen.getByRole("heading").focus();
    fireEvent.keyDown(screen.getByRole("heading"), { key: "Tab", shiftKey: true });
    expect(last).toHaveFocus();
    fireEvent.keyDown(last, { key: "Tab" });
    expect(first).toHaveFocus();
    fireEvent.keyDown(first, { key: "Tab", shiftKey: true });
    expect(last).toHaveFocus();
    screen.getByLabelText("Input").focus();
    fireEvent.keyDown(screen.getByLabelText("Input"), { key: "Tab" });
    expect(screen.getByLabelText("Input")).toHaveFocus();
    fireEvent.keyDown(last, { key: "Enter" });
    expect(screen.getByLabelText("Input")).toHaveFocus();
  });
  it("handles an empty dialog without throwing", () => {
    const { container } = render(<dialog open onKeyDown={trapMeetingFocus} tabIndex={-1} />);
    const dialog = container.querySelector("dialog")!;
    dialog.focus();
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(dialog).toHaveFocus();
  });
});
