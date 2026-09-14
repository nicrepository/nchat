import { describe, expect, it } from "vitest";

import {
  isNamedRecipient,
  shouldExecuteInAppNotification,
  shouldExecuteNativeNotification,
  shouldExecuteSound,
  soundClassFor,
  type AlertExecutionInput,
} from "./soundRules";
import type { SoundNotificationMode } from "./soundPreference";
import type { WSNotificationPolicy } from "./useChatWebSocket";

const ME = "user-me";

function policy(overrides: Partial<WSNotificationPolicy> = {}): WSNotificationPolicy {
  return {
    policy_version: 1,
    // Allowed on the realtime path today: the evaluation runs on the foreground
    // surface, which is exactly the one the toast and the chime live on.
    in_app: "allow",
    sound: "allow",
    // Denied on the realtime path today, which is what the server publishes:
    // the evaluation runs on the foreground surface and declares no push
    // capability. A case that needs the OS surface says so explicitly.
    web_push: "deny",
    sound_class: "general",
    ...overrides,
  };
}

function execution(overrides: Partial<AlertExecutionInput> = {}): AlertExecutionInput {
  return {
    policy: policy(),
    currentUserId: ME,
    localMode: "all",
    isOwnMessage: false,
    isDuplicate: false,
    isMutedConversation: false,
    isActiveConversation: false,
    isWindowFocused: true,
    ...overrides,
  };
}

describe("the authoritative class", () => {
  it("comes from the server and is not read out of the body", () => {
    expect(soundClassFor(policy({ sound_class: "general" }), ME)).toBe("general");
    expect(soundClassFor(policy({ sound_class: "direct" }), ME)).toBe("direct");
  });

  it("is a mention when the server named this recipient", () => {
    expect(soundClassFor(policy({ named_user_ids: [ME, "user-other"] }), ME)).toBe("mention");
    expect(soundClassFor(policy({ named_user_ids: ["user-other"] }), ME)).toBe("general");
  });

  it("is a mention when the server named everyone", () => {
    expect(soundClassFor(policy({ names_everyone: true }), ME)).toBe("mention");
    expect(isNamedRecipient(policy({ names_everyone: true }), ME)).toBe(true);
  });

  it("names nobody when there is no decision at all", () => {
    expect(isNamedRecipient(undefined, ME)).toBe(false);
    expect(soundClassFor(undefined, ME)).toBe("unknown");
  });
});

describe("the central decision", () => {
  it("plays a permitted sound", () => {
    expect(shouldExecuteSound(execution())).toBe(true);
  });

  it("never plays a denied one", () => {
    expect(shouldExecuteSound(execution({ policy: policy({ sound: "deny" }) }))).toBe(false);
  });

  it("does not treat a missing decision as a denial", () => {
    expect(shouldExecuteSound(execution({ policy: undefined }))).toBe(true);
  });
});

// A chat-service that predates issue #744 sends no decision. Reading that as a
// denial would mute a browser served ahead of its backend during a rolling
// deploy — silently, which is the failure nobody notices. The event passes
// through the local gates alone, and nothing about the product's rules is
// reconstructed here.
describe("an event from a server that predates the policy contract", () => {
  const legacy = { policy: undefined };

  it("has no class, because nobody classified it", () => {
    expect(soundClassFor(undefined, ME)).toBe("unknown");
  });

  it("plays, rather than going silent", () => {
    expect(shouldExecuteSound(execution(legacy))).toBe(true);
  });

  it("still honours every purely local restriction", () => {
    expect(shouldExecuteSound(execution({ ...legacy, localMode: "off" }))).toBe(false);
    expect(shouldExecuteSound(execution({ ...legacy, isOwnMessage: true }))).toBe(false);
    expect(shouldExecuteSound(execution({ ...legacy, isDuplicate: true }))).toBe(false);
    expect(shouldExecuteSound(execution({ ...legacy, isMutedConversation: true }))).toBe(false);
    expect(
      shouldExecuteSound(
        execution({ ...legacy, isActiveConversation: true, isWindowFocused: true }),
      ),
    ).toBe(false);
  });

  it("does not restrict on a class it was never told", () => {
    expect(shouldExecuteSound(execution({ ...legacy, localMode: "mentions" }))).toBe(true);
    expect(shouldExecuteSound(execution({ ...legacy, localMode: "mentions_and_dms" }))).toBe(true);
  });

  it("names nobody, so no badge is claimed on a guess", () => {
    expect(isNamedRecipient(undefined, ME)).toBe(false);
  });
});

// The three states a client must tell apart. The middle one is the whole point
// of the backend always sending an object: without it, "not notifiable" and
// "older server" would be the same bytes.
describe("the three states of the decision contract", () => {
  it("absent means the server could not answer", () => {
    expect(shouldExecuteSound(execution({ policy: undefined }))).toBe(true);
  });

  it("an explicit deny with no reasons means the event is not notifiable", () => {
    expect(shouldExecuteSound(execution({ policy: policy({ sound: "deny" }) }))).toBe(false);
  });

  it("an explicit deny with reasons means the policy suppressed it", () => {
    expect(
      shouldExecuteSound(
        execution({ policy: policy({ sound: "deny", reasons: ["outside_work_hours"] }) }),
      ),
    ).toBe(false);
  });
});

// Issue #744's safety property: a central denial is final. No local state — no
// preference, no focus, no active conversation, no class — may turn it back
// into playback. The table walks every combination of the local inputs against
// both ways a denial can arrive.
describe("a central deny can never become an allow", () => {
  const modes: SoundNotificationMode[] = ["off", "all", "mentions", "mentions_and_dms"];
  const denials: Array<[string, WSNotificationPolicy]> = [
    ["an explicit deny", policy({ sound: "deny", reasons: ["outside_work_hours"] })],
    ["a deny that names this recipient", policy({ sound: "deny", named_user_ids: [ME] })],
    ["a deny in a direct conversation", policy({ sound: "deny", sound_class: "direct" })],
    ["a deny that names everyone", policy({ sound: "deny", names_everyone: true })],
  ];

  for (const [name, denied] of denials) {
    for (const localMode of modes) {
      for (const isWindowFocused of [true, false]) {
        for (const isActiveConversation of [true, false]) {
          for (const isMutedConversation of [true, false]) {
            it(`stays silent: ${name}, mode=${localMode}, focused=${isWindowFocused}, active=${isActiveConversation}, muted=${isMutedConversation}`, () => {
              expect(
                shouldExecuteSound(
                  execution({
                    policy: denied,
                    localMode,
                    isWindowFocused,
                    isActiveConversation,
                    isMutedConversation,
                  }),
                ),
              ).toBe(false);
            });
          }
        }
      }
    }
  }
});

describe("local restrictions only ever remove a permitted sound", () => {
  it("does not play a message the reader sent", () => {
    expect(shouldExecuteSound(execution({ isOwnMessage: true }))).toBe(false);
  });

  it("does not play a duplicate", () => {
    expect(shouldExecuteSound(execution({ isDuplicate: true }))).toBe(false);
  });

  // Mute moved to the server (issue #744, review round 6): a decision that
  // arrived has already applied it, so the browser must not apply it again.
  // Re-applying it would be the browser deciding policy a second time — and it
  // is the legacy path, where no decision arrived, that still needs the local
  // copy.
  it("leaves mute to the decision when one arrived", () => {
    const allowed = execution({ isMutedConversation: true });
    expect(shouldExecuteSound(allowed)).toBe(true);
    expect(shouldExecuteInAppNotification(allowed)).toBe(true);
  });

  it("still applies its own mute copy on the legacy path", () => {
    const legacy = execution({ policy: undefined, isMutedConversation: true });
    expect(shouldExecuteSound(legacy)).toBe(false);
    expect(shouldExecuteInAppNotification(legacy)).toBe(false);
    expect(shouldExecuteNativeNotification(legacy)).toBe(false);
  });
});

describe("the local chime preference", () => {
  const cases: Array<[SoundNotificationMode, WSNotificationPolicy, boolean]> = [
    ["off", policy(), false],
    ["off", policy({ named_user_ids: [ME] }), false],
    ["all", policy(), true],
    ["all", policy({ sound_class: "direct" }), true],
    ["mentions", policy(), false],
    ["mentions", policy({ sound_class: "direct" }), false],
    ["mentions", policy({ named_user_ids: [ME] }), true],
    ["mentions", policy({ sound_class: "direct", named_user_ids: [ME] }), true],
    ["mentions_and_dms", policy(), false],
    ["mentions_and_dms", policy({ sound_class: "direct" }), true],
    ["mentions_and_dms", policy({ named_user_ids: [ME] }), true],
    ["mentions_and_dms", policy({ names_everyone: true }), true],
  ];

  for (const [localMode, decision, want] of cases) {
    it(`${localMode} with class ${soundClassFor(decision, ME)} plays: ${want}`, () => {
      expect(shouldExecuteSound(execution({ localMode, policy: decision }))).toBe(want);
    });
  }
});

describe("the conversation this tab is already showing", () => {
  it("does not chime while the reader is looking at it", () => {
    expect(
      shouldExecuteSound(execution({ isActiveConversation: true, isWindowFocused: true })),
    ).toBe(false);
  });

  it("stays silent for room activity even when the window is not focused", () => {
    expect(
      shouldExecuteSound(execution({ isActiveConversation: true, isWindowFocused: false })),
    ).toBe(false);
  });

  it("still chimes for something addressed to the reader while they are away", () => {
    expect(
      shouldExecuteSound(
        execution({
          policy: policy({ named_user_ids: [ME] }),
          isActiveConversation: true,
          isWindowFocused: false,
        }),
      ),
    ).toBe(true);
    expect(
      shouldExecuteSound(
        execution({
          policy: policy({ sound_class: "direct" }),
          isActiveConversation: true,
          isWindowFocused: false,
        }),
      ),
    ).toBe(true);
  });

  it("chimes normally for a conversation this tab is not showing", () => {
    expect(
      shouldExecuteSound(execution({ isActiveConversation: false, isWindowFocused: true })),
    ).toBe(true);
  });
});

// Issue #744, round 4: one channel never authorises another.
//
// Each gate reads its own decision and nothing else, so the four combinations
// below have four different outcomes. Before this, a single `sound: allow`
// opened both surfaces, and the second row — the one the reviewer named — was
// the bug: a permitted chime bought an OS notification the policy had refused.
describe("the two surfaces are authorised independently", () => {
  const cases: Array<{
    name: string;
    sound: "allow" | "deny";
    webPush: "allow" | "deny";
    isWindowFocused: boolean;
    wantSound: boolean;
    wantNative: boolean;
  }> = [
    {
      name: "sound allowed, OS surface denied, in the foreground",
      sound: "allow",
      webPush: "deny",
      isWindowFocused: true,
      wantSound: true,
      wantNative: false,
    },
    {
      name: "sound allowed, OS surface denied, in the background",
      sound: "allow",
      webPush: "deny",
      isWindowFocused: false,
      wantSound: true,
      wantNative: false,
    },
    {
      name: "OS surface allowed, sound denied, in the background",
      sound: "deny",
      webPush: "allow",
      isWindowFocused: false,
      wantSound: false,
      wantNative: true,
    },
    {
      name: "neither allowed",
      sound: "deny",
      webPush: "deny",
      isWindowFocused: false,
      wantSound: false,
      wantNative: false,
    },
    {
      name: "both allowed",
      sound: "allow",
      webPush: "allow",
      isWindowFocused: false,
      wantSound: true,
      wantNative: true,
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const input = execution({
        policy: policy({ sound: tc.sound, web_push: tc.webPush }),
        isWindowFocused: tc.isWindowFocused,
      });
      expect(shouldExecuteSound(input)).toBe(tc.wantSound);
      expect(shouldExecuteNativeNotification(input)).toBe(tc.wantNative);
    });
  }

  it("does not let the chime preference reach the OS surface", () => {
    const input = execution({
      policy: policy({ web_push: "allow" }),
      localMode: "off",
    });
    expect(shouldExecuteSound(input)).toBe(false);
    expect(shouldExecuteNativeNotification(input)).toBe(true);
  });

  it("still honours the local restrictions that are not about sound", () => {
    const allowed = { policy: policy({ web_push: "allow" }) };
    expect(shouldExecuteNativeNotification(execution({ ...allowed, isOwnMessage: true }))).toBe(
      false,
    );
    expect(shouldExecuteNativeNotification(execution({ ...allowed, isDuplicate: true }))).toBe(
      false,
    );
  });

  it("keeps the legacy path for a payload that carries no decision at all", () => {
    const legacy = execution({ policy: undefined });
    expect(shouldExecuteSound(legacy)).toBe(true);
    expect(shouldExecuteNativeNotification(legacy)).toBe(true);
  });

  it("never falls back to the legacy path when the decision is an explicit deny", () => {
    const denied = execution({ policy: policy({ sound: "deny", web_push: "deny" }) });
    expect(shouldExecuteSound(denied)).toBe(false);
    expect(shouldExecuteNativeNotification(denied)).toBe(false);
  });
});

/**
 * Issue #744: the three channels are decided separately by the engine, so the
 * three gates must stay separately authorised.
 *
 * Every case below sets the channels deliberately at odds with each other. That
 * is the point: a gate that read a neighbour's channel — or a single boolean
 * standing for the plan — passes a uniform fixture and fails here.
 */
describe("the three surfaces are authorised independently", () => {
  const cases: {
    name: string;
    inApp: "allow" | "deny";
    sound: "allow" | "deny";
    webPush: "allow" | "deny";
    wantInApp: boolean;
    wantSound: boolean;
    wantNative: boolean;
  }[] = [
    {
      name: "in-app only: the toast may appear, nothing else may",
      inApp: "allow",
      sound: "deny",
      webPush: "deny",
      wantInApp: true,
      wantSound: false,
      wantNative: false,
    },
    {
      name: "sound only: an allowed chime does not authorise a toast",
      inApp: "deny",
      sound: "allow",
      webPush: "deny",
      wantInApp: false,
      wantSound: true,
      wantNative: false,
    },
    {
      name: "push only: an allowed OS surface does not authorise a toast",
      inApp: "deny",
      sound: "deny",
      webPush: "allow",
      wantInApp: false,
      wantSound: false,
      wantNative: true,
    },
    {
      name: "everything denied: no surface runs",
      inApp: "deny",
      sound: "deny",
      webPush: "deny",
      wantInApp: false,
      wantSound: false,
      wantNative: false,
    },
    {
      name: "everything allowed: each surface is on its own authorisation",
      inApp: "allow",
      sound: "allow",
      webPush: "allow",
      wantInApp: true,
      wantSound: true,
      wantNative: true,
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const input = execution({
        policy: policy({ in_app: tc.inApp, sound: tc.sound, web_push: tc.webPush }),
        // Focused and looking at another conversation, so no local execution
        // gate removes anything and the central decision is the only thing
        // under test. Focus matters to the in-app surface specifically — a
        // window nobody is looking at cannot draw one.
        isWindowFocused: true,
      });
      expect(shouldExecuteInAppNotification(input)).toBe(tc.wantInApp);
      expect(shouldExecuteSound(input)).toBe(tc.wantSound);
      expect(shouldExecuteNativeNotification(input)).toBe(tc.wantNative);
    });
  }

  it("never turns a central in-app denial into a toast, whatever is local", () => {
    const denied = policy({ in_app: "deny", sound: "allow", web_push: "allow" });
    const locals: Partial<AlertExecutionInput>[] = [
      { isWindowFocused: true },
      { isWindowFocused: false },
      { isActiveConversation: true },
      { isActiveConversation: false },
      { localMode: "all" },
      { localMode: "off" },
      { policy: policy({ in_app: "deny", named_user_ids: [ME], sound_class: "direct" }) },
    ];
    for (const local of locals) {
      expect(shouldExecuteInAppNotification(execution({ policy: denied, ...local }))).toBe(false);
    }
  });

  it("keeps the accepted legacy path: no decision is not a denial", () => {
    // Rollout compatibility, unchanged by this channel: a chat-service that
    // predates issue #744 sends no object at all, and absence must not silence
    // a surface. It is the local gates alone from there.
    const legacy = execution({ policy: undefined });
    expect(shouldExecuteInAppNotification(legacy)).toBe(true);
    expect(shouldExecuteSound(legacy)).toBe(true);
    expect(shouldExecuteNativeNotification(legacy)).toBe(true);
  });

  it("still honours the local restrictions that are not policy", () => {
    const allowed = { policy: policy({ in_app: "allow" }) };
    expect(shouldExecuteInAppNotification(execution({ ...allowed, isOwnMessage: true }))).toBe(
      false,
    );
    expect(shouldExecuteInAppNotification(execution({ ...allowed, isDuplicate: true }))).toBe(
      false,
    );
  });
});

/**
 * The local execution boundary (issue #744, review round 6).
 *
 * The server produces the maximum set of surfaces a recipient may receive. A
 * session may narrow that with facts only it holds, and may never widen it.
 */
describe("local execution can only narrow the central decision", () => {
  it("suppresses the toast for the conversation this tab is showing", () => {
    const allowed = policy({ in_app: "allow" });
    expect(
      shouldExecuteInAppNotification(execution({ policy: allowed, isActiveConversation: false })),
    ).toBe(true);
    // The one fact the server cannot observe — see PresenceConnected — and it
    // only ever removes.
    expect(
      shouldExecuteInAppNotification(execution({ policy: allowed, isActiveConversation: true })),
    ).toBe(false);
  });

  it("never turns a central denial into a surface, whatever the local state is", () => {
    const denied = policy({ in_app: "deny", sound: "deny", web_push: "deny" });
    const locals: Partial<AlertExecutionInput>[] = [
      { isActiveConversation: false },
      { isActiveConversation: true },
      { isWindowFocused: false },
      { isWindowFocused: true },
      { localMode: "all" },
      { isMutedConversation: false },
    ];
    for (const local of locals) {
      const input = execution({ policy: denied, ...local });
      expect(shouldExecuteInAppNotification(input)).toBe(false);
      expect(shouldExecuteSound(input)).toBe(false);
      expect(shouldExecuteNativeNotification(input)).toBe(false);
    }
  });

  it("keeps the chime preference local and one-directional", () => {
    // It can take an allowed chime away...
    expect(shouldExecuteSound(execution({ localMode: "off" }))).toBe(false);
    // ...and cannot give back one the centre denied, nor reach another surface.
    const denied = policy({ sound: "deny", in_app: "allow" });
    expect(shouldExecuteSound(execution({ policy: denied, localMode: "all" }))).toBe(false);
    expect(shouldExecuteInAppNotification(execution({ policy: denied, localMode: "off" }))).toBe(
      true,
    );
  });
});

/**
 * Focus is a local execution gate for the in-app surface (issue #744, review
 * round 7).
 *
 * A toast is drawn in this window. A window nobody is looking at cannot draw
 * one, and it must not be queued for later either: by the time the reader comes
 * back the message is old and the alert would land on top of whatever they
 * returned to. The OS-level surface is the one that exists for that case, and it
 * is decided on its own channel.
 */
describe("the in-app surface needs a focused window", () => {
  it("is not raised in an unfocused window", () => {
    const allowed = policy({ in_app: "allow" });
    expect(
      shouldExecuteInAppNotification(
        execution({ policy: allowed, isWindowFocused: false, isActiveConversation: false }),
      ),
    ).toBe(false);
  });

  it("is raised in a focused window showing another conversation", () => {
    const allowed = policy({ in_app: "allow" });
    expect(
      shouldExecuteInAppNotification(
        execution({ policy: allowed, isWindowFocused: true, isActiveConversation: false }),
      ),
    ).toBe(true);
  });

  it("stays denied when the centre denied it, focused or not", () => {
    const denied = policy({ in_app: "deny" });
    for (const isWindowFocused of [true, false]) {
      expect(shouldExecuteInAppNotification(execution({ policy: denied, isWindowFocused }))).toBe(
        false,
      );
    }
  });

  // Focus is local capability, not corporate policy, so it applies on the
  // legacy path too: an older server said nothing about surfaces, and a hidden
  // window still cannot draw one.
  it("applies on the legacy path as well", () => {
    expect(
      shouldExecuteInAppNotification(execution({ policy: undefined, isWindowFocused: false })),
    ).toBe(false);
    expect(
      shouldExecuteInAppNotification(execution({ policy: undefined, isWindowFocused: true })),
    ).toBe(true);
  });

  // It must not reach across to the other channels: they have their own rules
  // about where the reader is.
  it("does not change the other two surfaces", () => {
    const allowed = policy({ in_app: "allow", sound: "allow", web_push: "allow" });
    const hidden = execution({ policy: allowed, isWindowFocused: false });
    expect(shouldExecuteSound(hidden)).toBe(true);
    expect(shouldExecuteNativeNotification(hidden)).toBe(true);
  });
});
