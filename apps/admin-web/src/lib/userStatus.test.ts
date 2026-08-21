import { describe, expect, it } from "vitest";

import { noStatusActionReason, userStatusAction } from "./userStatus";

describe("userStatusAction", () => {
  // The table the domain really supports. Anything outside it is an operation
  // the API refuses, so the console must not offer it.
  const cases: { status: string; target: string | null }[] = [
    { status: "active", target: "suspended" },
    { status: "suspended", target: "active" },
    { status: "invited", target: null },
    { status: "locked", target: null },
    { status: "deleted", target: null },
  ];

  for (const { status, target } of cases) {
    it(`${status} → ${target ?? "no action"}`, () => {
      const action = userStatusAction(status);
      if (target === null) {
        expect(action).toBeNull();
        return;
      }
      expect(action?.targetStatus).toBe(target);
    });
  }

  // Fail closed: a status this build has never heard of gets no button, rather
  // than the opposite one.
  it("offers nothing for a status it does not know", () => {
    for (const unknown of ["", "archived", "pending", "ACTIVE"]) {
      expect(userStatusAction(unknown)).toBeNull();
    }
  });

  it("labels each direction with its own wording and impact", () => {
    const deactivate = userStatusAction("active");
    expect(deactivate?.label).toBe("Desativar");
    expect(deactivate?.impact).toContain("sessões ativas são encerradas");
    expect(deactivate?.confirmBody("Ana")).toContain("Ana");

    const activate = userStatusAction("suspended");
    expect(activate?.label).toBe("Ativar");
    expect(activate?.impact).toContain("Nenhuma sessão anterior é restaurada");
  });
});

describe("noStatusActionReason", () => {
  it("names the flow that owns each unmanaged state", () => {
    expect(noStatusActionReason("invited")).toContain("convite");
    expect(noStatusActionReason("locked")).toContain("bloqueado");
    expect(noStatusActionReason("whatever")).toContain("não gerenciado");
  });
});
