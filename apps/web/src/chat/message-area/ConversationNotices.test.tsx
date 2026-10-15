import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import ConversationNotices from "./ConversationNotices";

/** CQ947-R2-04: one connection notice for a realtime failure, an owed read, or both. */
describe("ConversationNotices — the connection notice", () => {
  it.each([
    ["only a realtime failure", "falhou", null, 1],
    ["only an owed reconciliation", null, "devida", 1],
    ["both", "falhou", "devida", 1],
    ["neither", null, null, 0],
  ])("%s", (_name, realtimeError, syncError, shown) => {
    render(
      <ConversationNotices
        sendError={null}
        realtimeError={realtimeError}
        syncError={syncError}
        actionError={null}
        pinError={null}
        typingLabel={null}
      />,
    );

    expect(screen.queryAllByTestId("chat-realtime-error")).toHaveLength(shown);
  });
});
