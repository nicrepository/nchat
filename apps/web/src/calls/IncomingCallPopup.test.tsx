import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import IncomingCallPopup from "./IncomingCallPopup";

const baseProps = {
  name: "Caio Almeida",
  targetKind: "user" as const,
  callType: "video" as const,
  onAccept: vi.fn(),
  onReject: vi.fn(),
};

describe("IncomingCallPopup", () => {
  it("renders the caller's real name", () => {
    render(<IncomingCallPopup {...baseProps} />);
    expect(screen.getByText("Caio Almeida")).toBeInTheDocument();
  });

  it("shows two-letter initials, not a single raw character, when there is no avatar", () => {
    const { container } = render(<IncomingCallPopup {...baseProps} />);
    expect(container.querySelector(".incoming-call__avatar")).toHaveTextContent("CA");
  });

  it("renders the personalized avatar image when avatarUrl is set", () => {
    const { container } = render(<IncomingCallPopup {...baseProps} avatarUrl="https://x/a.png" />);
    expect(container.querySelector("img")).toHaveAttribute("src", "https://x/a.png");
  });

  it("falls back to initials, not a broken-image glyph, when the avatar fails to load", () => {
    const { container } = render(
      <IncomingCallPopup {...baseProps} avatarUrl="https://x/broken.png" />,
    );
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(container.querySelector(".incoming-call__avatar")).toHaveTextContent("CA");
  });
});
