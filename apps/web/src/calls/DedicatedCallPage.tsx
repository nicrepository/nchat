import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";

import { fetchSidebarData } from "../chat/chatApi";
import type { Call } from "../chat/callState";
import { resolveCall } from "../chat/resourceCallSignaling";
import DedicatedCallStage from "./DedicatedCallStage";
import { useCallSession } from "./CallSessionProvider";
import { emitCallTechnicalEvent } from "./callTelemetry";
import GlobalCallIndicator from "./GlobalCallIndicator";

const callIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

export default function DedicatedCallPage() {
  const { callId = "" } = useParams();
  const navigate = useNavigate();
  const session = useCallSession();
  const {
    acknowledgeDedicated,
    announceDedicated,
    beginResourceParticipation,
    calls,
    dedicatedRecoveryFailed,
    media,
    ownerState,
    registerDirectory,
    resource,
  } = session;
  const [resolved, setResolved] = useState<Call | null>(null);
  const [directory, setDirectory] = useState<Awaited<ReturnType<typeof fetchSidebarData>> | null>(
    null,
  );
  const [error, setError] = useState("");
  const activationCall = useRef("");
  const acknowledged = useRef(false);
  const invalidCallID = !callIDPattern.test(callId);

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
      return channel ? { id: channel.id, name: channel.name } : null;
    }
    if (resolved.target_type === "dm") {
      const dm = directory.dms.find((candidate) => candidate.id === resolved.target_id);
      return dm ? { id: dm.id, name: dm.name } : null;
    }
    const peerID =
      resolved.caller_id === directory.currentUserId ? resolved.callee_id : resolved.caller_id;
    const peer = directory.dms.find((dm) => dm.counterpart?.userId === peerID)?.counterpart;
    return { id: peerID, name: peer?.displayName ?? "Participante" };
  }, [directory, resolved]);

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
      // Registers the new participation only once join() itself confirms it
      // actually succeeded (issue #570 follow-up) — never inferred from
      // resource.callId changing, which a rejoin of the very same call_id
      // (e.g. reopening this exact dedicated tab for a call this user
      // already left) would never observably do.
      void resource
        .join({
          kind: resolved.target_type,
          id: resolved.target_id!,
          name: target.name,
          callId: resolved.call_id,
        })
        .then((joinedCallId) => {
          if (joinedCallId) void beginResourceParticipation(joinedCallId);
        });
    }
  }, [beginResourceParticipation, calls, ownerState, resolved, resource, target]);

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
        participants={media.participants}
        controls={controls}
        bindLocalMedia={media.bindLocalMedia}
        bindRemoteAudio={media.bindRemoteAudio}
        screenShareName={
          media.remoteScreenShare
            ? (media.participants.find(
                (participant) => participant.identity === media.remoteScreenShare?.identity,
              )?.displayName ?? "Participante")
            : undefined
        }
        bindScreenShare={media.remoteScreenShare?.bindMedia}
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
