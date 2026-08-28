import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import type { SelfProfile } from "./profileApi";
import { _resetSelfProfile, refreshSelfProfile, useSelfProfile } from "./selfProfile";

const { mockFetchMyProfile } = vi.hoisted(() => ({
  mockFetchMyProfile: vi.fn<(signal?: AbortSignal) => Promise<SelfProfile>>(),
}));

vi.mock("./profileApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./profileApi")>();
  return { ...actual, fetchMyProfile: (signal?: AbortSignal) => mockFetchMyProfile(signal) };
});

/** Renders the state as text so assertions read the hook's contract, not internals. */
function Probe() {
  const state = useSelfProfile();
  return (
    <span data-testid="probe">
      {state.status === "ready" ? `ready:${state.profile.displayName}` : state.status}
    </span>
  );
}

const probe = () => screen.getByTestId("probe").textContent;

beforeEach(() => {
  clearTokens();
  vi.clearAllMocks();
  _resetSelfProfile();
  mockFetchMyProfile.mockResolvedValue({ id: "u1", displayName: "Ana" });
});

afterEach(() => {
  _resetSelfProfile();
  clearTokens();
});

describe("useSelfProfile", () => {
  it("loads once and reports loading then ready", async () => {
    render(<Probe />);

    expect(probe()).toBe("loading");
    await waitFor(() => expect(probe()).toBe("ready:Ana"));
    expect(mockFetchMyProfile).toHaveBeenCalledTimes(1);
  });

  it("serves one shared load to concurrent consumers", async () => {
    render(
      <>
        <Probe />
        <Probe />
      </>,
    );

    await waitFor(() => expect(screen.getAllByTestId("probe")[0]?.textContent).toBe("ready:Ana"));
    expect(screen.getAllByTestId("probe")[1]?.textContent).toBe("ready:Ana");
    expect(mockFetchMyProfile).toHaveBeenCalledTimes(1);
  });

  it("reports error when the load fails, never a profile", async () => {
    mockFetchMyProfile.mockRejectedValue(new Error("boom"));
    render(<Probe />);

    await waitFor(() => expect(probe()).toBe("error"));
  });

  it("republishes after a confirmed profile change without a reload", async () => {
    render(<Probe />);
    await waitFor(() => expect(probe()).toBe("ready:Ana"));

    mockFetchMyProfile.mockResolvedValue({ id: "u1", displayName: "Ana Souza" });
    act(() => refreshSelfProfile());

    await waitFor(() => expect(probe()).toBe("ready:Ana Souza"));
  });

  it("keeps the newest of two close refreshes when the earlier response arrives last", async () => {
    render(<Probe />);
    await waitFor(() => expect(probe()).toBe("ready:Ana"));

    let resolveEarlier: (profile: SelfProfile) => void = () => {};
    let resolveLatest: (profile: SelfProfile) => void = () => {};
    mockFetchMyProfile
      .mockImplementationOnce(
        () =>
          new Promise<SelfProfile>((resolve) => {
            resolveEarlier = resolve;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise<SelfProfile>((resolve) => {
            resolveLatest = resolve;
          }),
      );

    act(() => refreshSelfProfile());
    act(() => refreshSelfProfile());
    // Initial load plus exactly the two confirmed invalidations: no effect-driven duplicate fetch.
    expect(mockFetchMyProfile).toHaveBeenCalledTimes(3);

    await act(async () => {
      resolveLatest({ id: "u1", displayName: "Ana Atual" });
    });
    expect(probe()).toBe("ready:Ana Atual");

    await act(async () => {
      resolveEarlier({ id: "u1", displayName: "Ana Antiga" });
    });
    expect(probe()).toBe("ready:Ana Atual");
  });

  it("drops the previous session's profile the moment the session changes", async () => {
    setTokens("token-a");
    render(<Probe />);
    await waitFor(() => expect(probe()).toBe("ready:Ana"));

    // Never settles: what matters is the state between the switch and the answer.
    mockFetchMyProfile.mockReturnValue(new Promise<SelfProfile>(() => {}));
    act(() => setTokens("token-b"));
    expect(probe()).toBe("loading");
  });

  it("ignores a response that arrives after the session changed", async () => {
    setTokens("token-a");
    let resolveA: (profile: SelfProfile) => void = () => {};
    mockFetchMyProfile.mockReturnValue(
      new Promise<SelfProfile>((resolve) => {
        resolveA = resolve;
      }),
    );
    render(<Probe />);
    expect(probe()).toBe("loading");

    mockFetchMyProfile.mockReturnValue(new Promise<SelfProfile>(() => {}));
    act(() => setTokens("token-b"));
    // Session A's answer lands late: it belongs to a session that is gone.
    await act(async () => {
      resolveA({ id: "u1", displayName: "Ana" });
    });

    expect(probe()).toBe("loading");
  });

  it("reloads for the new session after a logout and login", async () => {
    setTokens("token-a");
    render(<Probe />);
    await waitFor(() => expect(probe()).toBe("ready:Ana"));

    mockFetchMyProfile.mockResolvedValue({ id: "u2", displayName: "Bruno" });
    act(() => clearTokens());
    act(() => setTokens("token-b"));

    await waitFor(() => expect(probe()).toBe("ready:Bruno"));
  });

  it("passes an abort signal so a superseded load can be cancelled", async () => {
    render(<Probe />);
    await waitFor(() => expect(probe()).toBe("ready:Ana"));

    const signal = mockFetchMyProfile.mock.calls[0]?.[0];
    expect(signal).toBeInstanceOf(AbortSignal);
    expect(signal?.aborted).toBe(false);

    act(() => refreshSelfProfile());
    expect(signal?.aborted).toBe(true);
  });
});
