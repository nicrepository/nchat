import { fireEvent, render, screen, within } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import MeetingSelection from "./MeetingSelection";

it("renders at most 50 selections and keeps inspection/removal usable across pages", () => {
  const participants = Array.from({ length: 120 }, (_, i) => ({
    user_id: String(i),
    display_name: `Pessoa ${i}`,
  }));
  const toggle = vi.fn();
  const { rerender } = render(
    <MeetingSelection participants={participants} locked={false} toggle={toggle} />,
  );
  const list = () => within(screen.getByRole("list", { name: "Participantes selecionados" }));
  expect(list().getAllByRole("listitem")).toHaveLength(50);
  expect(screen.getByRole("button", { name: "Selecionados anteriores" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Próximos selecionados" }));
  expect(list().getByText("Pessoa 50")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Remover Pessoa 50" }));
  expect(toggle).toHaveBeenCalledWith(participants[50]);
  fireEvent.click(screen.getByRole("button", { name: "Próximos selecionados" }));
  expect(list().getAllByRole("listitem")).toHaveLength(20);
  expect(screen.getByRole("button", { name: "Próximos selecionados" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Selecionados anteriores" }));
  expect(list().getByText("Pessoa 50")).toBeInTheDocument();
  rerender(<MeetingSelection participants={participants.slice(0, 20)} locked toggle={toggle} />);
  expect(list().getAllByRole("listitem")).toHaveLength(20);
  expect(screen.getByRole("button", { name: "Remover Pessoa 0" })).toBeDisabled();
});
