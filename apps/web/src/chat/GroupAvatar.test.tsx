import { render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import GroupAvatar from "./GroupAvatar";

function avatar(ui: React.ReactElement): HTMLElement {
  const { container } = render(ui);
  return container.querySelector(".group-avatar") as HTMLElement;
}

describe("GroupAvatar (issue #1026)", () => {
  afterEach(() => vi.restoreAllMocks());

  it.each([
    ["Infraestrutura", "I"],
    ["Equipe de Plataforma", "ED"],
    ["  muitos   espaços  aqui ", "ME"],
    ["Ágil Ética", "ÁÉ"],
    ["日本 チーム", "日チ"],
    ["🚀 Lançamento", "🚀L"],
    ["", "?"],
    ["   ", "?"],
    ["x".repeat(120), "X"],
  ])("derives the initials of %j as %j", (name, initials) => {
    expect(avatar(<GroupAvatar name={name} />).textContent).toBe(initials);
  });

  it("recomputes the initials from the current name (a rename)", () => {
    const { container, rerender } = render(<GroupAvatar name="Infra" />);
    rerender(<GroupAvatar name="Plataforma Web" />);
    expect(container.textContent).toBe("PW");
  });

  it("shows the emoji instead of initials, as text, and keeps it across a rename", () => {
    const { container, rerender } = render(<GroupAvatar name="Infra" emoji="👩‍💻" />);
    rerender(<GroupAvatar name="Outro nome" emoji="👩‍💻" />);
    const element = container.querySelector(".group-avatar") as HTMLElement;
    expect(element.textContent).toBe("👩‍💻");
    expect(element.dataset.mode).toBe("emoji");
    expect(element.children).toHaveLength(0);
  });

  it("never renders markup from the emoji value", () => {
    const element = avatar(<GroupAvatar name="X" emoji="<img src=x onerror=alert(1)>" />);
    expect(element.querySelector("img")).toBeNull();
    expect(element.textContent).toBe("<img src=x onerror=alert(1)>");
  });

  it("is neutral and deterministic: no colour class, no randomness, no inline style", () => {
    const random = vi.spyOn(Math, "random");
    const first = avatar(<GroupAvatar name="Equipe" />);
    const second = avatar(<GroupAvatar name="Outra equipe bem diferente" />);
    expect(random).not.toHaveBeenCalled();
    for (const element of [first, second]) {
      expect(element.className).toBe("group-avatar group-avatar--sm");
      expect(element.getAttribute("style")).toBeNull();
      expect(element.getAttribute("aria-hidden")).toBe("true");
      expect(element.dataset.mode).toBe("auto");
    }
  });

  it("supports the sizes the surfaces use", () => {
    expect(avatar(<GroupAvatar name="A" size="md" />).className).toContain("group-avatar--md");
    expect(avatar(<GroupAvatar name="A" size="lg" />).className).toContain("group-avatar--lg");
  });
});
