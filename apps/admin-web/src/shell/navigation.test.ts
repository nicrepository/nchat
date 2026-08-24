import { describe, expect, it } from "vitest";

import { ADMIN_NAV, visibleNavItems } from "./navigation";

describe("ADMIN_NAV", () => {
  it("covers the planned console sections", () => {
    expect(ADMIN_NAV.map((item) => item.id)).toEqual([
      "overview",
      "users",
      "channels",
      "security",
      "files",
      "configuration",
      "authentication",
      "email",
      "calls",
      "links",
      "integrations",
      "system",
      "health",
      "audit",
    ]);
  });

  // A section that is not implemented must not carry a route. Giving it one is
  // how a placeholder becomes a control that looks like it works.
  it("only routes the sections that exist", () => {
    const routed = ADMIN_NAV.filter((item) => item.path !== undefined).map((item) => item.id);
    expect(routed).toEqual([
      "overview",
      "users",
      "channels",
      "security",
      "files",
      "configuration",
      "health",
      "audit",
    ]);
  });

  it("declares a capability for every section", () => {
    for (const item of ADMIN_NAV) {
      expect(item.capability).toMatch(/^admin\./);
    }
  });
});

describe("visibleNavItems", () => {
  it("shows nothing when the session grants nothing", () => {
    expect(visibleNavItems(() => false)).toEqual([]);
  });

  it("filters by the capability the session reports", () => {
    const items = visibleNavItems((capability) => capability === "admin.audit.read");
    expect(items.map((item) => item.id)).toEqual(["audit"]);
  });

  it("shows every section to a session that grants everything", () => {
    expect(visibleNavItems(() => true)).toHaveLength(ADMIN_NAV.length);
  });
});
