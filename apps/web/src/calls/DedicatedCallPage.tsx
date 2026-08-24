import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";

import {
  fetchChannelCallParticipantProfiles,
  fetchGroupCallParticipantProfiles,
  fetchSidebarData,
  type CallParticipantProfile,
} from "../chat/chatApi";
import { localParticipantDisplayName } from "../chat/messageDisplay";
import type { Call } from "../chat/callState";
import { resolveCall } from "../chat/resourceCallSignaling";
import { useSelfProfile } from "../profile/selfProfile";
import DedicatedCallStage from "./DedicatedCallStage";
import { useCallSession } from "./CallSessionProvider";
import { emitCallTechnicalEvent } from "./callTelemetry";
import GlobalCallIndicator from "./GlobalCallIndicator";

const callIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

// Must stay <= the server's domain.MaxCallParticipantProfileIDs (issue #612):
// a resource call room has no enforced participant ceiling, so a roster can
// exceed one batch request's cap. Chunking here — rather than raising the
// server cap — keeps that cap as a real abuse-control boundary.
const CALL_PARTICIPANT_CHUNK_SIZE = 50;

export default function DedicatedCallPage() {
  const { callId = "" } = useParams();
  const navigate = useNavigate();
  const session = useCallSession();
  const {
    acknowledgeDedicated,
    activateResourceParticipation,
    announceDedicated,
    calls,
    dedicatedRecoveryFailed,
    media,
    ownerState,
    registerDirectory,
  } = session;
  const [resolved, setResolved] = useState<Call | null>(null);
  const [directory, setDirectory] = useState<Awaited<ReturnType<typeof fetchSidebarData>> | null>(
    null,
  );
  const [error, setError] = useState("");
  const activationCall = useRef("");
  const acknowledged = useRef(false);
  const invalidCallID = !callIDPattern.test(callId);

  // Local call-presentation identity (issue #612), reused from the shared
  // session-scoped profile cache — never a second GET /auth/me just for calls.
  const selfProfile = useSelfProfile();
  const selfDisplayName = selfProfile.status === "ready" ? selfProfile.profile.displayName : "";
  const selfAvatarUrl = selfProfile.status === "ready" ? selfProfile.profile.avatarUrl : undefined;
  const localDisplayName = localParticipantDisplayName(selfDisplayName);
  const [participantProfiles, setParticipantProfiles] = useState<
    Map<string, CallParticipantProfile>
  >(new Map());

  useEffect(() => {
    if (invalidCallID) return;
    let active = true;
    Promise.all([resolveCall(callId), fetchSidebarData()]).then(
      ([call, nextDirectory]) => {
        if (!active) return;
        setResolved(call);
        setDirectory(nextDirectory);
        registerDirectory(nextDirectory);
        announceDedicated(call.call_id);
      },
      () => active && setError("Não foi possível abrir esta chamada."),
    );
    return () => {
      active = false;
    };
  }, [announceDedicated, callId, invalidCallID, registerDirectory]);

  const target = useMemo(() => {
    if (!resolved || !directory) return null;
    if (resolved.target_type === "channel") {
      const channel = directory.channels.find((candidate) => candidate.id === resolved.target_id);
      // A channel/group name is never an individual's identity (issue #612)
      // — avatarUrl stays undefined here, never a room-level picture.
      return channel ? { id: channel.id, name: channel.name, avatarUrl: undefined } : null;
    }
    if (resolved.target_type === "dm") {
      const dm = directory.dms.find((candidate) => candidate.id === resolved.target_id);
      return dm ? { id: dm.id, name: dm.name, avatarUrl: undefined } : null;
    }
    const peerID =
      resolved.caller_id === directory.currentUserId ? resolved.callee_id : resolved.caller_id;
    const peer = directory.dms.find((dm) => dm.counterpart?.userId === peerID)?.counterpart;
    // The direct peer's real avatar (issue #612), already resolved by the
    // existing DMCounterpart contract — reused here rather than fetched
    // again, and only ever attached to the header when the call actually is
    // one specific person (this branch), never for a channel/group above.
    return { id: peerID, name: peer?.displayName ?? "Participante", avatarUrl: peer?.avatarUrl };
  }, [directory, resolved]);

  // Batch-resolves every currently-known resource participant's identity
  // (issue #612) — never one request per tile. Direct calls have no
  // resourceTarget and skip this entirely, since the counterpart contract
  // already carries their identity.
  //
  // The dependency key is canonical and set-like — deduplicated and sorted —
  // so [A, B] and [B, A] (an SDK/event reorder with no membership change)
  // produce the identical string and never re-fire the effect. This never
  // reads or writes media.participants itself beyond one .map(); the
  // original array/objects are untouched.
  const participantIdentityKey = Array.from(new Set(media.participants.map((p) => p.identity)))
    .sort()
    .join(",");
  useEffect(() => {
    if (!resolved || (resolved.target_type !== "channel" && resolved.target_type !== "dm")) return;
    const ids = participantIdentityKey ? participantIdentityKey.split(",") : [];
    if (ids.length === 0) return;
    let active = true;
    const target =
      resolved.target_type === "channel"
        ? { fetch: fetchChannelCallParticipantProfiles, id: resolved.target_id! }
        : { fetch: fetchGroupCallParticipantProfiles, id: resolved.target_id! };
    // Chunked to stay within MaxCallParticipantProfileIDs (issue #612): a
    // room can exceed the batch cap, so this issues one request per
    // CALL_PARTICIPANT_CHUNK_SIZE-sized slice of the (already deduplicated,
    // sorted) id list rather than ever sending an oversized batch. Chunks
    // are independent requests — completion order does not matter, since
    // each chunk's result is merged by canonical user ID rather than
    // replacing the whole map.
    const chunks: string[][] = [];
    for (let i = 0; i < ids.length; i += CALL_PARTICIPANT_CHUNK_SIZE) {
      chunks.push(ids.slice(i, i + CALL_PARTICIPANT_CHUNK_SIZE));
    }
    Promise.allSettled(chunks.map((chunk) => target.fetch(target.id, chunk))).then((results) => {
      if (!active) return;
      const resolvedProfiles = results.flatMap((result) =>
        result.status === "fulfilled" ? result.value : [],
      );
      // One request failing (network, transient 5xx) degrades only the
      // chunk it covered to initials — it must never discard profiles a
      // sibling chunk already resolved, and never falls back to an
      // obsolete/partial map from a previous generation.
      setParticipantProfiles(new Map(resolvedProfiles.map((profile) => [profile.userId, profile])));
    });
    return () => {
      active = false;
    };
  }, [resolved, participantIdentityKey]);

  useEffect(() => {
    if (
      !resolved ||
      !target ||
      ownerState !== "local" ||
      activationCall.current === resolved.call_id
    )
      return;
    if (resolved.target_type === "user" && calls.call?.call_id !== resolved.call_id) return;
    activationCall.current = resolved.call_id;
    if (resolved.target_type === "user") {
      void calls.activateMedia();
    } else if (resolved.target_type === "channel" || resolved.target_type === "dm") {
      // activateResourceParticipation both runs the real join() and — only
      // once it actually confirms success (issue #570 follow-up) —
      // registers the new participation; never inferred from
      // resource.callId changing, which a rejoin of the very same call_id
      // (e.g. reopening this exact dedicated tab for a call this user
      // already left) would never observably do. Deliberately NOT
      // joinResourceParticipation: this effect runs on every successful
      // ownerState -> "local" transition, including a plain handoff
      // continuation reconnecting media for an ALREADY-active participation
      // — treating that as a fresh join intent would risk masking a real,
      // currently-accepted "left" for it (issue #594 adversarial follow-up,
      // round 3).
      void activateResourceParticipation({
        kind: resolved.target_type,
        id: resolved.target_id!,
        name: target.name,
        callId: resolved.call_id,
      });
    }
  }, [activateResourceParticipation, calls, ownerState, resolved, target]);

  useEffect(() => {
    if (!resolved || acknowledged.current || activationCall.current !== resolved.call_id) return;
    if (media.status === "connected") {
      acknowledged.current = true;
      acknowledgeDedicated(resolved.call_id, true);
    } else if (media.status === "error" || media.status === "permission-denied") {
      acknowledged.current = true;
      acknowledgeDedicated(resolved.call_id, false);
    }
  }, [acknowledgeDedicated, media.status, resolved]);

  if (invalidCallID || error) {
    return (
      <main className="dedicated-call dedicated-call--message">
        <p role="alert">{invalidCallID ? "Chamada inválida." : error}</p>
      </main>
    );
  }
  if (dedicatedRecoveryFailed && ownerState !== "local") {
    return (
      <main className="dedicated-call dedicated-call--message">
        <p role="alert">Não foi possível recuperar esta chamada nesta aba.</p>
        <button type="button" onClick={() => navigate("/chat")}>
          Voltar para o chat
        </button>
      </main>
    );
  }
  if (!resolved || !directory || !target) {
    return (
      <main className="dedicated-call dedicated-call--message">
        <p role="status">Preparando chamada…</p>
      </main>
    );
  }

  const title = target.name;
  const participantCount = Math.max(1, media.participants.length + 1);
  const controls = {
    microphoneEnabled: media.microphoneEnabled,
    cameraEnabled: media.cameraEnabled,
    screenShareEnabled: media.screenShareEnabled,
    pendingControl: media.pendingControl,
    onMicrophone: media.toggleMicrophone,
    onCamera: media.toggleCamera,
    onScreenShare: media.toggleScreenShare,
    onEnd: () => {
      emitCallTechnicalEvent("end");
      if (resolved.target_type === "user") {
        calls.end();
        return;
      }
      // Issue #570: leaving a resource/group call from the dedicated tab
      // must fully converge — participant leave (#569), release dedicated
      // ownership, then close this tab (falling back to /chat when the
      // browser won't let it close itself) — never just the local leave
      // that used to leave this tab stuck and the main tab still pointing
      // at ownership nobody holds anymore. A failed leave (network, server)
      // must not close the window: the existing error/retry state stays on
      // screen instead.
      void session
        .leaveDedicated(resolved.call_id)
        .then(() => {
          window.close();
          if (!window.closed) navigate("/chat");
        })
        .catch(() => undefined);
    },
  };

  return (
    <>
      {ownerState !== "local" && (
        <GlobalCallIndicator
          title={title}
          participantCount={participantCount}
          onReturn={() => void session.takeOver()}
        />
      )}
      <DedicatedCallStage
        title={title}
        status={
          media.status === "connected"
            ? "connected"
            : media.status === "reconnecting"
              ? "reconnecting"
              : media.status === "error" || media.status === "permission-denied"
                ? "failed"
                : "connecting"
        }
        participantCount={participantCount}
        participants={media.participants.map((participant) => ({
          ...participant,
          avatarUrl: participantProfiles.get(participant.identity)?.avatarUrl,
        }))}
        controls={controls}
        bindLocalMedia={media.bindLocalMedia}
        bindRemoteAudio={media.bindRemoteAudio}
        localScreenShareActive={media.screenShareEnabled}
        bindLocalScreenShare={media.bindLocalScreenShare}
        screenShareName={
          media.remoteScreenShare
            ? (media.participants.find(
                (participant) => participant.identity === media.remoteScreenShare?.identity,
              )?.displayName ?? "Participante")
            : undefined
        }
        bindScreenShare={media.remoteScreenShare?.bindMedia}
        hasLocalVideo={media.hasLocalVideo}
        localSeed={directory.currentUserId}
        localDisplayName={localDisplayName}
        localAvatarUrl={selfAvatarUrl}
        headerAvatar={
          resolved.target_type === "user"
            ? { seed: target.id, avatarUrl: target.avatarUrl }
            : undefined
        }
        onMinimize={() => {
          void session.releaseDedicated(resolved.call_id).then(() => {
            window.close();
            if (!window.closed) navigate("/chat");
          });
        }}
      />
    </>
  );
}
