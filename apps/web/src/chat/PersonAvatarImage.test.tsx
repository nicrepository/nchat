import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PersonAvatarImage } from "./PersonAvatarImage";

describe("PersonAvatarImage", () => {
  it("renders the image when src is usable", () => {
    const { container } = render(
      <PersonAvatarImage src="https://x/a.png" initials="CA" imgClassName="img" />,
    );
    expect(container.querySelector("img")).toHaveAttribute("src", "https://x/a.png");
  });

  it("falls back to initials when src is absent", () => {
    render(<PersonAvatarImage initials="CA" imgClassName="img" />);
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(screen.getByText("CA")).toBeInTheDocument();
  });

  it("falls back to initials, without a broken-image glyph, once the image errors", () => {
    const { container } = render(
      <PersonAvatarImage src="https://x/broken.png" initials="CA" imgClassName="img" />,
    );
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(screen.getByText("CA")).toBeInTheDocument();
  });

  it("retries a changed src after a previous one failed", () => {
    const { container, rerender } = render(
      <PersonAvatarImage src="https://x/broken.png" initials="CA" imgClassName="img" />,
    );
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).not.toBeInTheDocument();

    rerender(<PersonAvatarImage src="https://x/new.png" initials="CA" imgClassName="img" />);
    expect(container.querySelector("img")).toHaveAttribute("src", "https://x/new.png");
  });

  it("uses referrerPolicy no-referrer and an empty alt by default", () => {
    const { container } = render(
      <PersonAvatarImage src="https://x/a.png" initials="CA" imgClassName="img" />,
    );
    const img = container.querySelector("img")!;
    expect(img).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(img).toHaveAttribute("alt", "");
  });

  it("accepts an explicit alt for when the avatar is the sole identity", () => {
    const { container } = render(
      <PersonAvatarImage
        src="https://x/a.png"
        initials="CA"
        imgClassName="img"
        alt="Caio Almeida"
      />,
    );
    expect(container.querySelector("img")).toHaveAttribute("alt", "Caio Almeida");
  });
});
