/**
 * What the conversation header and the call bar should show about this
 * conversation's resource call (RF-24, issues #622 and #657).
 *
 * Split out of ChatMessageArea (issue #496, CQ follow-up): discovery,
 * participation, whether the join action is currently available and which of the
 * bar's three modes applies were four interleaved chains of conditionals inside
 * the component that renders them. They are one question — "is there a call
 * here, and what is this reader's relationship to it" — so they answer it here,
 * once, and the component renders the answer.
 */

import { useMemo } from "react";

import type { ActiveResourceCallBarProps } from "../calls/ActiveResourceCallBar";
import type { ChatOutletContext } from "./ChatShell";
import type { ResourceCallHeaderState } from "./ChatMessageArea";

export interface ResourceCallBarInput {
  kind: "channel" | "dm";
  targetId: string;
  /** The conversation's display name, used verbatim in the bar's title. */
  resolvedName: string;
  /** The domain discriminant: only a channel or a group DM has a room. */
  detailsKind: "channel" | "group" | "direct" | null;
  ctx: ChatOutletContext;
}

export interface ResourceCallBarState {
  /** Undefined while the bar takes over, which is what deduplicates the header. */
  headerState: ResourceCallHeaderState | undefined;
  /** Null when there is nothing to announce. */
  barProps: ActiveResourceCallBarProps | null;
}

/**
 * RF-24/#622 round 2: a resource call room applies only to a channel or a group
 * DM — a 1:1 keeps using RF-23's onStartCall and never touches discovery at all.
 */
function resourceCallKindFor(input: ResourceCallBarInput): "channel" | "dm" | null {
  if (input.kind === "channel") return "channel";
  return input.detailsKind === "group" ? "dm" : null;
}

/**
 * #642: a channel keeps the "#" prefix used everywhere else in this header; a
 * group DM has no channel-style handle, so it uses the bare name (the issue
 * explicitly forbids inventing a "#" for it). Resource calls are always
 * call_type "audio" by construction — RF-24 is one multiparty room, never
 * RF-23's audio/video split — so "Chamada de voz" is an invariant, not a guess.
 */
function barTitle(input: ResourceCallBarInput): string {
  const prefix = input.detailsKind === "group" ? "" : "#";
  return `Chamada de voz — ${prefix}${input.resolvedName}`;
}

interface Discovery {
  callKind: "channel" | "dm" | null;
  call: ReturnType<NonNullable<ChatOutletContext["getResourceCall"]>> | null;
  exists: boolean;
  participating: boolean;
}

/**
 * Discovery and participation are read independently of whether joining is
 * currently possible: ctx.joinResourceCall is undefined exactly while the shared
 * Room is busy elsewhere, and a call must never be hidden just because of that.
 */
function discover(input: ResourceCallBarInput): Discovery {
  const callKind = resourceCallKindFor(input);
  if (!callKind || !input.targetId) {
    return { callKind: null, call: null, exists: false, participating: false };
  }
  const call = input.ctx.getResourceCall?.(callKind, input.targetId) ?? null;
  return {
    callKind,
    call,
    exists: call?.status === "active",
    participating: input.ctx.isParticipatingIn?.(callKind, input.targetId) ?? false,
  };
}

function participatingBarProps(
  input: ResourceCallBarInput,
  startedAt: string,
): ActiveResourceCallBarProps {
  const session = input.ctx.resourceCallSession;
  if (!session) {
    return { mode: "participating-info", title: barTitle(input), startedAt };
  }
  return {
    mode: "participating-local",
    title: barTitle(input),
    startedAt,
    participants: session.participants,
    localId: session.localId,
    localName: session.localName,
    localInitials: session.localInitials,
    localAvatarUrl: session.localAvatarUrl,
    activeSpeakerId: session.activeSpeakerId,
    microphoneEnabled: session.microphoneEnabled,
    microphonePending: session.microphonePending,
    onToggleMicrophone: session.onToggleMicrophone,
    onLeave: session.onLeave,
    onOpenFullCall: session.onOpenFullCall,
  };
}

function barPropsFor(
  input: ResourceCallBarInput,
  discovery: Discovery,
): ActiveResourceCallBarProps | null {
  if (!discovery.exists || !discovery.call) return null;
  if (discovery.participating) return participatingBarProps(input, discovery.call.created_at);
  const callId = discovery.call.call_id;
  const callKind = discovery.callKind;
  return {
    mode: "available",
    title: barTitle(input),
    startedAt: discovery.call.created_at,
    joinDisabled: !input.ctx.joinResourceCall,
    onJoin: () => {
      if (callKind) {
        input.ctx.joinResourceCall?.({
          kind: callKind,
          id: input.targetId,
          name: input.resolvedName,
          callId,
        });
      }
    },
  };
}

/**
 * #657: when a call exists here, or the reader is in one, the header's own
 * actions are suppressed and the bar takes over. The "Chamada" start button
 * survives only when there is no call and the reader is not in one.
 */
function headerStateFor(
  input: ResourceCallBarInput,
  discovery: Discovery,
): ResourceCallHeaderState | undefined {
  if (!discovery.callKind || discovery.exists || discovery.participating) return undefined;
  const callKind = discovery.callKind;
  return {
    disabled: !input.ctx.joinResourceCall,
    onCall: () =>
      input.ctx.joinResourceCall?.({
        kind: callKind,
        id: input.targetId,
        name: input.resolvedName,
      }),
  };
}

export function useResourceCallBar(input: ResourceCallBarInput): ResourceCallBarState {
  const { kind, targetId, resolvedName, detailsKind, ctx } = input;
  return useMemo(() => {
    const current = { kind, targetId, resolvedName, detailsKind, ctx };
    const discovery = discover(current);
    return {
      headerState: headerStateFor(current, discovery),
      barProps: barPropsFor(current, discovery),
    };
  }, [kind, targetId, resolvedName, detailsKind, ctx]);
}
