import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import OutgoingCallPopup from "./OutgoingCallPopup";

const baseProps = {
  name: "Bruno Lima",
  callType: "video" as const,
  onCancel: vi.fn(),
};

describe("OutgoingCallPopup", () => {
  it("renders the callee's real name", () => {
    render(<OutgoingCallPopup {...baseProps} />);
    expect(screen.getByText("Bruno Lima")).toBeInTheDocument();
  });

  it("labels a video call distinctly from an audio call", () => {
    const { rerender } = render(<OutgoingCallPopup {...baseProps} callType="video" />);
    expect(screen.getByText("Chamada de vídeo")).toBeInTheDocument();

    rerender(<OutgoingCallPopup {...baseProps} callType="audio" />);
    expect(screen.getByText("Chamada de voz")).toBeInTheDocument();
    expect(screen.queryByText("Chamada de vídeo")).not.toBeInTheDocument();
  });

  it("shows two-letter initials, not a single raw character, when there is no avatar", () => {
    const { container } = render(<OutgoingCallPopup {...baseProps} />);
    expect(container.querySelector(".outgoing-call__avatar")).toHaveTextContent("BL");
  });

  it("renders the personalized avatar image when avatarUrl is set", () => {
    const { container } = render(<OutgoingCallPopup {...baseProps} avatarUrl="https://x/a.png" />);
    expect(container.querySelector("img")).toHaveAttribute("src", "https://x/a.png");
  });

  it("falls back to initials, not a broken-image glyph, when the avatar fails to load", () => {
    const { container } = render(
      <OutgoingCallPopup {...baseProps} avatarUrl="https://x/broken.png" />,
    );
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(container.querySelector(".outgoing-call__avatar")).toHaveTextContent("BL");
  });

  it("shows the ringing status, available to screen readers via a live region", () => {
    render(<OutgoingCallPopup {...baseProps} />);
    expect(screen.getByRole("status")).toHaveTextContent("Ligando…");
  });

  it("switches the status to a cancelling-in-progress message, never a second lifecycle", () => {
    render(<OutgoingCallPopup {...baseProps} cancelling />);
    expect(screen.getByRole("status")).toHaveTextContent("Cancelando…");
    expect(screen.queryByText("Ligando…")).not.toBeInTheDocument();
  });

  it("exposes a cancel button with a full, unambiguous accessible name", () => {
    render(<OutgoingCallPopup {...baseProps} />);
    expect(
      screen.getByRole("button", { name: "Cancelar chamada para Bruno Lima" }),
    ).toBeInTheDocument();
  });

  it("calls onCancel exactly once per click", () => {
    const onCancel = vi.fn();
    render(<OutgoingCallPopup {...baseProps} onCancel={onCancel} />);
    fireEvent.click(screen.getByRole("button", { name: /Cancelar chamada/ }));
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it("disables the cancel button while cancelling is in progress, so a duplicate click cannot fire onCancel again", () => {
    const onCancel = vi.fn();
    render(<OutgoingCallPopup {...baseProps} onCancel={onCancel} cancelling />);
    const button = screen.getByRole("button", { name: /Cancelar chamada/ });
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("never steals focus on mount (non-modal, no autofocus)", () => {
    const bodyFocusBefore = document.activeElement;
    render(<OutgoingCallPopup {...baseProps} />);
    expect(document.activeElement).toBe(bodyFocusBefore);
    expect(screen.getByRole("button", { name: /Cancelar chamada/ })).not.toHaveFocus();
  });

  it("presents the card as a non-modal region, not a dialog", () => {
    render(<OutgoingCallPopup {...baseProps} />);
    expect(screen.getByRole("region", { name: "Ligando para Bruno Lima" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("marks the avatar as decorative — the adjacent name already identifies the person", () => {
    const { container } = render(<OutgoingCallPopup {...baseProps} />);
    expect(container.querySelector(".outgoing-call__avatar")).toHaveAttribute(
      "aria-hidden",
      "true",
    );
  });
});
