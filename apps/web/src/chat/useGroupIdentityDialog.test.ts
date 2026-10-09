import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { useGroupIdentityDialog } from "./useGroupIdentityDialog";

describe("useGroupIdentityDialog (issue #1026)", () => {
  it("opens for a group and hands focus back to the trigger on close", () => {
    const trigger = document.createElement("button");
    document.body.append(trigger);
    const { result } = renderHook(() => useGroupIdentityDialog());

    act(() => result.current.open("dm-1", trigger));
    expect(result.current.targetId).toBe("dm-1");
    act(() => result.current.close());

    expect(result.current.targetId).toBeNull();
    expect(trigger).toHaveFocus();
    trigger.remove();
  });

  it("closes without a trigger and does not refocus a previous one", () => {
    const stale = document.createElement("button");
    document.body.append(stale);
    const { result } = renderHook(() => useGroupIdentityDialog());

    act(() => result.current.open("dm-1", stale));
    act(() => result.current.close());
    stale.blur();
    act(() => result.current.open("dm-2", null));
    act(() => result.current.close());

    expect(result.current.targetId).toBeNull();
    expect(stale).not.toHaveFocus();
    stale.remove();
  });
});
