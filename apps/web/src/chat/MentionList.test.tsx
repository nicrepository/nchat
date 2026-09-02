import { act, createRef } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import MentionList from "./MentionList";
import type { MentionListRef } from "./MentionList";

const items = [
  { mentionType: "user" as const, id: "user-1", label: "Ana" },
  { mentionType: "all" as const, id: "00000000-0000-0000-0000-000000000000", label: "all" },
];

describe("MentionList", () => {
  it("supports arrow navigation, selection and Escape", () => {
    const command = vi.fn();
    const ref = createRef<MentionListRef>();
    render(<MentionList ref={ref} items={items} command={command} />);

    act(() => expect(ref.current?.onKeyDown({ key: "ArrowDown" } as KeyboardEvent)).toBe(true));
    expect(screen.getByRole("option", { name: /all/ })).toHaveAttribute("aria-selected", "true");
    act(() => expect(ref.current?.onKeyDown({ key: "ArrowUp" } as KeyboardEvent)).toBe(true));
    act(() => expect(ref.current?.onKeyDown({ key: "Enter" } as KeyboardEvent)).toBe(true));

    expect(command).toHaveBeenCalledWith(items[0]);
    expect(ref.current?.onKeyDown({ key: "Escape" } as KeyboardEvent)).toBe(true);
    expect(ref.current?.onKeyDown({ key: "Tab" } as KeyboardEvent)).toBe(false);
  });

  it("selects an item with the pointer", () => {
    const command = vi.fn();
    render(<MentionList items={items} command={command} />);
    fireEvent.mouseDown(screen.getByRole("option", { name: /all/ }));
    expect(command).toHaveBeenCalledWith(items[1]);
  });

  it("renders an empty state and only consumes Escape", () => {
    const ref = createRef<MentionListRef>();
    render(<MentionList ref={ref} items={[]} command={vi.fn()} />);
    expect(screen.getByText("Nenhum resultado")).toBeInTheDocument();
    expect(ref.current?.onKeyDown({ key: "Escape" } as KeyboardEvent)).toBe(true);
    expect(ref.current?.onKeyDown({ key: "Enter" } as KeyboardEvent)).toBe(false);
  });

  it("announces loading and error states", () => {
    const { rerender } = render(<MentionList items={[]} command={vi.fn()} loadState="loading" />);
    expect(screen.getByRole("status")).toHaveTextContent("Carregando sugestões");

    rerender(<MentionList items={[]} command={vi.fn()} loadState="error" />);
    expect(screen.getByRole("status")).toHaveTextContent("Não foi possível carregar sugestões");
  });

  it("groups people, channels and special options with a non-color selected marker", () => {
    render(
      <MentionList
        items={[items[0], { mentionType: "channel", id: "channel-1", label: "geral" }, items[1]]}
        command={vi.fn()}
      />,
    );

    expect(screen.getByText("Pessoas")).toBeInTheDocument();
    expect(screen.getByText("Canais")).toBeInTheDocument();
    expect(screen.getByText("Especial")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Ana/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("check")).toHaveAttribute("aria-hidden", "true");
    expect(screen.getByRole("option", { name: /geral/ })).toHaveTextContent("#geral");
  });
});
