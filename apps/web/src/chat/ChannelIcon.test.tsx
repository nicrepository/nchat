import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import ChannelIcon from "./ChannelIcon";
import { channelAccessibleName } from "./channelIdentity";

// Issue #1024: a channel's identity is its visibility — never an avatar.
describe("ChannelIcon", () => {
  const iconOf = (isPrivate: boolean, className?: string) =>
    render(<ChannelIcon isPrivate={isPrivate} className={className} />).container
      .firstElementChild as SVGElement;

  it("draws a bare # for a public channel", () => {
    const icon = iconOf(false);

    expect(icon.tagName).toBe("svg");
    expect(icon.dataset.channelIcon).toBe("public");
    // The # glyph: two verticals and two horizontals, and nothing else.
    expect(icon.querySelectorAll(":scope > line")).toHaveLength(4);
    expect(icon.querySelectorAll("rect, path")).toHaveLength(0);
    expect(icon.querySelector("[data-channel-lock]")).toBeNull();
  });

  it("draws the # with a lock for a private channel", () => {
    const icon = iconOf(true);

    expect(icon.dataset.channelIcon).toBe("private");
    // Still the four strokes of the #, plus a lock: its body and its shackle.
    expect(icon.querySelectorAll(":scope > line")).toHaveLength(4);
    const lock = icon.querySelector("[data-channel-lock]");
    expect(lock?.querySelectorAll("rect")).toHaveLength(1);
    expect(lock?.querySelectorAll("path")).toHaveLength(1);
  });

  it("is decorative and carries no image, text or initials", () => {
    for (const isPrivate of [false, true]) {
      const icon = iconOf(isPrivate);

      expect(icon).toHaveAttribute("aria-hidden", "true");
      expect(icon.textContent).toBe("");
      expect(icon.querySelector("img, image, text")).toBeNull();
      // Neutral: it inherits the row's colour, never a colour of its own.
      expect(icon).toHaveAttribute("stroke", "currentColor");
      expect(icon).not.toHaveAttribute("style");
    }
  });

  it("takes the surface's sizing class", () => {
    expect(iconOf(false, "chat-sidebar__icon")).toHaveClass("chat-sidebar__icon");
  });
});

describe("channelAccessibleName", () => {
  it("names a public channel", () => {
    expect(channelAccessibleName("geral", false)).toBe("Canal geral");
  });

  it("says a private channel is private, in words", () => {
    expect(channelAccessibleName("financeiro", true)).toBe("Canal privado financeiro");
  });
});
