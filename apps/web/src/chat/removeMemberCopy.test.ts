import { describe, expect, it } from "vitest";

import { ApiRequestError } from "../lib/api";
import { removalConsequence, removeMemberErrorMessage } from "./removeMemberCopy";

// The confirmation's copy (issue #469).
//
// These are claims about the domain, not about wording preferences: what a
// removal takes away differs by conversation, and the sentence must not
// promise a revocation the visibility rule does not perform.
describe("removalConsequence", () => {
  it("states lost access for a private channel", () => {
    const text = removalConsequence("channel", true);

    expect(text).toContain("canal privado");
    expect(text).toContain("perderá o acesso");
  });

  // chat.channel_visible_to_user admits every workspace role that can reach
  // public channels, with or without a membership row. Saying otherwise here
  // would be the panel lying about the server.
  it("promises no loss of reading for a public channel", () => {
    const text = removalConsequence("channel", false);

    expect(text).toContain("continua visível");
    expect(text).not.toContain("perderá o acesso");
  });

  it("states lost participation for a group", () => {
    const text = removalConsequence("group", false);

    expect(text).toContain("grupo");
    expect(text).toContain("perderá o acesso");
  });

  // The domain stores a display name and no gender, so the copy must not
  // assume one.
  it("never infers a gender for the person being removed", () => {
    for (const text of [
      removalConsequence("channel", true),
      removalConsequence("channel", false),
      removalConsequence("group", false),
    ]) {
      expect(text).not.toMatch(/\b(Ele|Ela)\b/);
    }
  });
});

describe("removeMemberErrorMessage", () => {
  it("says what each refusal means without quoting the server", () => {
    const cases: [number, string][] = [
      [403, "Você não tem permissão para remover esta pessoa."],
      [404, "Esta conversa não está mais disponível."],
      [400, "Não é possível remover esta pessoa desta conversa."],
      [429, "Muitas solicitações em sequência. Aguarde um momento e tente novamente."],
      [0, "Sem conexão. Verifique sua rede e tente novamente."],
    ];
    for (const [status, expected] of cases) {
      expect(
        removeMemberErrorMessage(new ApiRequestError(status, "denied", "internal detail")),
      ).toBe(expected);
    }
  });

  // Whatever the server wrote is for an operator: it can name a table, a
  // constraint or another workspace's data, and none of that belongs on screen.
  it("never forwards the server's own message", () => {
    const message = removeMemberErrorMessage(
      new ApiRequestError(
        500,
        "internal",
        'duplicate key value violates "chat.channel_members_pkey"',
      ),
    );

    expect(message).toBe("Não foi possível remover. Tente novamente.");
  });

  it("falls back for anything that is not an API error", () => {
    expect(removeMemberErrorMessage(new TypeError("boom"))).toBe(
      "Não foi possível remover. Tente novamente.",
    );
    expect(removeMemberErrorMessage(undefined)).toBe("Não foi possível remover. Tente novamente.");
  });
});
