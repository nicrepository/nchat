import {
  MediaDeviceFailure,
  Room,
  RoomEvent,
  Track,
  type LocalTrackPublication,
  type Participant,
  type RemoteParticipant,
  type RemoteTrack,
  type RemoteTrackPublication,
  type TrackPublication,
} from "livekit-client";

export type SpikeMediaErrorKind =
  | "camera_denied"
  | "camera_unavailable"
  | "microphone_denied"
  | "microphone_unavailable";

export class SpikeMediaError extends Error {
  readonly kind: SpikeMediaErrorKind;

  constructor(kind: SpikeMediaErrorKind) {
    super(kind);
    this.name = "SpikeMediaError";
    this.kind = kind;
  }
}

export interface LiveKitSpikeSessionCallbacks {
  onLocalElement(element: HTMLMediaElement): void;
  // element.dataset.participantIdentity carries the LiveKit participant
  // identity (the canonical user UUID) for every remote element, so a
  // multi-participant consumer (RF-24) can group elements by participant
  // without a second callback parameter breaking every existing call site.
  onRemoteElement(element: HTMLMediaElement): void;
  onElementRemoved(element: HTMLMediaElement): void;
  onDisconnected(): void;
  onReconnecting(): void;
  onReconnected(): void;
  onAudioPlaybackChanged(canPlaybackAudio: boolean): void;
  // Authoritative signal for the local microphone publication only (never
  // fired for a remote participant or for a non-microphone source). Fired
  // after enableMicrophone()/setMicrophoneEnabled() confirm the publication,
  // whenever the SDK reports RoomEvent.TrackMuted/TrackUnmuted for the local
  // mic, and after Reconnected resyncs the publication's real state.
  onMicrophoneStateChanged(enabled: boolean): void;
  // Represents a remote person even before they publish any track (RF-24
  // grid). Fired for participants already in the room when connect()
  // completes, and for every participant joining afterwards.
  onParticipantConnected(identity: string): void;
  onParticipantDisconnected(identity: string): void;
  // A remote camera can be muted (publication kept, no unsubscribe) instead
  // of unpublished. The subscribed element is never removed for this —
  // muting is not "no video", it is "temporarily no usable frame" — so the
  // consumer must stop treating it as available (RF-24 grid fallback) without
  // losing the element, and restore it on unmute. Never fired for a
  // microphone (local or remote): see onMicrophoneStateChanged for that.
  onRemoteVideoAvailabilityChanged(identity: string, available: boolean): void;
}

export interface LiveKitSpikeSession {
  startAudio(): Promise<void>;
  connect(serverUrl: string, token: string): Promise<void>;
  enableCamera(): Promise<void>;
  enableMicrophone(): Promise<void>;
  setCameraEnabled(enabled: boolean): Promise<void>;
  setMicrophoneEnabled(enabled: boolean): Promise<void>;
  disconnect(): Promise<void>;
}

export type LiveKitSpikeSessionFactory = (
  callbacks: LiveKitSpikeSessionCallbacks,
) => LiveKitSpikeSession;

class LiveKitSpikeSessionImpl implements LiveKitSpikeSession {
  private readonly room = new Room({ adaptiveStream: true, dynacast: true });
  private readonly remoteTracks = new Map<
    RemoteTrack,
    { participantSid: string; elements: Set<HTMLMediaElement> }
  >();
  private readonly callbacks: LiveKitSpikeSessionCallbacks;
  private localVideoElement: HTMLMediaElement | null = null;
  private disposed = false;
  // Distinct from `disposed`: `disposed` commits every other method to
  // rejecting/no-op'ing forever the moment disconnect() is first called,
  // while `disconnected` only flips once room.disconnect(true) has actually
  // succeeded — so a rejected attempt can be retried for real instead of
  // disconnect() being considered done just because it was tried once.
  private disconnected = false;
  private disconnectPromise: Promise<void> | null = null;

  constructor(callbacks: LiveKitSpikeSessionCallbacks) {
    this.callbacks = callbacks;
    this.room
      .on(RoomEvent.TrackSubscribed, this.onTrackSubscribed)
      .on(RoomEvent.TrackUnsubscribed, this.onTrackUnsubscribed)
      .on(RoomEvent.ParticipantConnected, this.onParticipantConnected)
      .on(RoomEvent.ParticipantDisconnected, this.onParticipantDisconnected)
      .on(RoomEvent.Disconnected, this.onDisconnected)
      .on(RoomEvent.Reconnecting, this.onReconnecting)
      .on(RoomEvent.Reconnected, this.onReconnected)
      .on(RoomEvent.AudioPlaybackStatusChanged, this.onAudioPlaybackChanged)
      .on(RoomEvent.TrackMuted, this.onTrackMuted)
      .on(RoomEvent.TrackUnmuted, this.onTrackUnmuted);
  }

  async connect(serverUrl: string, token: string): Promise<void> {
    if (this.disposed) return;
    await this.room.connect(serverUrl, token, { autoSubscribe: true });
    if (this.disposed) {
      await this.room.disconnect(true);
      return;
    }
    // Participants already in the room never fire RoomEvent.ParticipantConnected
    // for this client — that event is only for joins after we are already
    // connected — so existing occupants (RF-24: someone joining after others)
    // are replayed explicitly here.
    for (const participant of this.room.remoteParticipants.values()) {
      this.callbacks.onParticipantConnected(participant.identity);
    }
    // A camera publication's own current state is authoritative on join: a
    // late joiner must never infer "camera on" from the mere existence of a
    // subscribed track when the publisher already muted it — no future
    // TrackMuted is guaranteed for a mute that happened before we arrived.
    this.syncRemoteVideoAvailability();
  }

  async startAudio(): Promise<void> {
    if (this.disposed) return;
    await this.room.startAudio();
  }

  async enableCamera(): Promise<void> {
    if (this.disposed) return;
    let publication: LocalTrackPublication | undefined;
    try {
      publication = await this.room.localParticipant.setCameraEnabled(true);
    } catch (error) {
      throw mediaError("camera", error);
    }
    if (this.disposed) {
      await this.disableCameraAfterInterruptedEnable();
      return;
    }
    try {
      this.attachLocalVideo(publication?.track);
    } catch (error) {
      await this.disableCameraAfterInterruptedEnable();
      throw mediaError("camera", error);
    }
  }

  async enableMicrophone(): Promise<void> {
    if (this.disposed) return;
    try {
      await this.room.localParticipant.setMicrophoneEnabled(true);
    } catch (error) {
      throw mediaError("microphone", error);
    }
    if (this.disposed) {
      await this.disableMicrophoneAfterInterruptedEnable();
      return;
    }
    this.notifyMicrophoneState();
  }

  async setCameraEnabled(enabled: boolean): Promise<void> {
    if (this.disposed) return;
    let publication: LocalTrackPublication | undefined;
    try {
      publication = await this.room.localParticipant.setCameraEnabled(enabled);
    } catch (error) {
      throw mediaError("camera", error);
    }
    if (this.disposed) {
      if (enabled) await this.disableCameraAfterInterruptedEnable();
      return;
    }
    if (!enabled) {
      this.removeLocalVideo();
      return;
    }
    try {
      this.attachLocalVideo(publication?.track);
    } catch (error) {
      await this.disableCameraAfterInterruptedEnable();
      throw mediaError("camera", error);
    }
  }

  async setMicrophoneEnabled(enabled: boolean): Promise<void> {
    if (this.disposed) return;
    try {
      await this.room.localParticipant.setMicrophoneEnabled(enabled);
    } catch (error) {
      throw mediaError("microphone", error);
    }
    if (this.disposed) {
      if (enabled) await this.disableMicrophoneAfterInterruptedEnable();
      return;
    }
    this.notifyMicrophoneState();
  }

  disconnect(): Promise<void> {
    if (this.disconnected) return Promise.resolve();
    if (this.disconnectPromise) return this.disconnectPromise;
    this.disposed = true;
    const attempt = this.dispose();
    const tracked: Promise<void> = attempt.then(
      () => {
        this.disconnected = true;
        if (this.disconnectPromise === tracked) this.disconnectPromise = null;
      },
      (error: unknown) => {
        // Cache cleared on rejection too: a retried disconnect() must call
        // room.disconnect(true) again, never replay this same failure.
        if (this.disconnectPromise === tracked) this.disconnectPromise = null;
        throw error;
      },
    );
    this.disconnectPromise = tracked;
    return tracked;
  }

  private readonly onTrackSubscribed = (
    track: RemoteTrack,
    publication: RemoteTrackPublication,
    participant: RemoteParticipant,
  ): void => {
    if (
      this.disposed ||
      this.remoteTracks.has(track) ||
      (track.kind !== Track.Kind.Video && track.kind !== Track.Kind.Audio) ||
      !this.isCurrentParticipant(participant)
    ) {
      return;
    }
    const element = track.attach();
    element.autoplay = true;
    element.dataset.participantIdentity = participant.identity;
    const elements = new Set([element]);
    this.remoteTracks.set(track, { participantSid: participant.sid, elements });
    this.callbacks.onRemoteElement(element);
    if (track.kind === Track.Kind.Audio) {
      void element.play().catch(() => {
        if (!this.disposed) this.callbacks.onAudioPlaybackChanged(false);
      });
    } else if (this.isRemoteCamera(publication, participant) && publication.isMuted) {
      // The publication's own state at subscribe time is authoritative: this
      // covers a participant who joins/publishes already muted, after our
      // own connect() (the initial-join case is also covered by
      // syncRemoteVideoAvailability() below, since event ordering during
      // connect() is not guaranteed).
      this.callbacks.onRemoteVideoAvailabilityChanged(participant.identity, false);
    }
  };

  private readonly onTrackUnsubscribed = (track: RemoteTrack): void => {
    if (this.disposed) return;
    this.removeRemoteTrack(track);
  };

  private readonly onParticipantConnected = (participant: RemoteParticipant): void => {
    if (!this.disposed) this.callbacks.onParticipantConnected(participant.identity);
  };

  private readonly onParticipantDisconnected = (participant: RemoteParticipant): void => {
    if (this.disposed) return;
    for (const [track, remoteTrack] of this.remoteTracks) {
      if (remoteTrack.participantSid === participant.sid) this.removeRemoteTrack(track);
    }
    this.callbacks.onParticipantDisconnected(participant.identity);
  };

  private readonly onDisconnected = (): void => {
    if (!this.disposed) this.callbacks.onDisconnected();
  };

  private readonly onReconnecting = (): void => {
    if (!this.disposed) this.callbacks.onReconnecting();
  };

  private readonly onReconnected = (): void => {
    if (this.disposed) return;
    // Publications may have been muted/unmuted — local or remote — while the
    // signal connection was down: never assume the pre-reconnect state still
    // matches the SDK after Reconnected.
    this.notifyMicrophoneState();
    this.syncRemoteVideoAvailability();
    this.callbacks.onReconnected();
  };

  private readonly onAudioPlaybackChanged = (canPlaybackAudio: boolean): void => {
    if (!this.disposed) this.callbacks.onAudioPlaybackChanged(canPlaybackAudio);
  };

  private readonly onTrackMuted = (
    publication: TrackPublication,
    participant: Participant,
  ): void => {
    if (this.disposed) return;
    if (this.isLocalMicrophone(publication, participant)) {
      this.callbacks.onMicrophoneStateChanged(false);
      return;
    }
    if (this.isRemoteCamera(publication, participant)) {
      this.callbacks.onRemoteVideoAvailabilityChanged(participant.identity, false);
    }
  };

  private readonly onTrackUnmuted = (
    publication: TrackPublication,
    participant: Participant,
  ): void => {
    if (this.disposed) return;
    if (this.isLocalMicrophone(publication, participant)) {
      this.callbacks.onMicrophoneStateChanged(true);
      return;
    }
    if (this.isRemoteCamera(publication, participant)) {
      this.callbacks.onRemoteVideoAvailabilityChanged(participant.identity, true);
    }
  };

  private isLocalMicrophone(publication: TrackPublication, participant: Participant): boolean {
    return (
      participant === this.room.localParticipant && publication.source === Track.Source.Microphone
    );
  }

  // Never true for the local participant's own camera: RF-23/RF-24 both
  // drive the local preview from setCameraEnabled()/attachLocalVideo(), never
  // from a mute event, so a local self-mute must not be mistaken for a
  // remote participant's camera going away.
  private isRemoteCamera(publication: TrackPublication, participant: Participant): boolean {
    return participant !== this.room.localParticipant && publication.source === Track.Source.Camera;
  }

  // A track callback can arrive after its participant already disconnected
  // (a queued event racing ParticipantDisconnected), or after the same
  // identity reconnected as a genuinely new RemoteParticipant instance/SID.
  // room.remoteParticipants is keyed by identity and always holds whichever
  // instance is current, so comparing by reference — never by identity
  // string alone — tells a late/stale callback apart from a legitimate
  // reentry without needing to track SIDs ourselves.
  private isCurrentParticipant(participant: RemoteParticipant): boolean {
    return this.room.remoteParticipants.get(participant.identity) === participant;
  }

  private notifyMicrophoneState(): void {
    if (this.disposed) return;
    const publication = this.room.localParticipant.getTrackPublication(Track.Source.Microphone);
    this.callbacks.onMicrophoneStateChanged(publication ? !publication.isMuted : false);
  }

  // Single source of truth for remote camera availability, driven by the
  // Room's actual current publications rather than accumulated mute/unmute
  // events — used after connect() (late join) and after Reconnected (state
  // may have changed while the signal connection was down). Only reports for
  // a publication we have actually subscribed to: an unsubscribed one has no
  // element for the hook to show either way.
  private syncRemoteVideoAvailability(): void {
    if (this.disposed) return;
    for (const participant of this.room.remoteParticipants.values()) {
      const publication = participant.getTrackPublication(Track.Source.Camera);
      if (!publication?.isSubscribed) continue;
      this.callbacks.onRemoteVideoAvailabilityChanged(participant.identity, !publication.isMuted);
    }
  }

  private async dispose(): Promise<void> {
    try {
      await this.room.disconnect(true);
    } finally {
      this.removeAdapterListeners();
      this.removeLocalVideo();
      for (const track of [...this.remoteTracks.keys()]) this.removeRemoteTrack(track);
    }
  }

  private removeAdapterListeners(): void {
    this.room
      .off(RoomEvent.TrackSubscribed, this.onTrackSubscribed)
      .off(RoomEvent.TrackUnsubscribed, this.onTrackUnsubscribed)
      .off(RoomEvent.ParticipantConnected, this.onParticipantConnected)
      .off(RoomEvent.ParticipantDisconnected, this.onParticipantDisconnected)
      .off(RoomEvent.Disconnected, this.onDisconnected)
      .off(RoomEvent.Reconnecting, this.onReconnecting)
      .off(RoomEvent.Reconnected, this.onReconnected)
      .off(RoomEvent.AudioPlaybackStatusChanged, this.onAudioPlaybackChanged)
      .off(RoomEvent.TrackMuted, this.onTrackMuted)
      .off(RoomEvent.TrackUnmuted, this.onTrackUnmuted);
  }

  private removeRemoteTrack(track: RemoteTrack): void {
    const remoteTrack = this.remoteTracks.get(track);
    if (!remoteTrack) return;
    this.remoteTracks.delete(track);
    const elements = new Set([...remoteTrack.elements, ...track.detach()]);
    for (const element of elements) this.callbacks.onElementRemoved(element);
  }

  private async disableCameraAfterInterruptedEnable(): Promise<void> {
    try {
      await this.room.localParticipant.setCameraEnabled(false);
    } catch {
      // Best effort only: preserve the original device/attach failure.
    }
  }

  private async disableMicrophoneAfterInterruptedEnable(): Promise<void> {
    try {
      await this.room.localParticipant.setMicrophoneEnabled(false);
    } catch {
      // Best effort only during teardown.
    }
  }

  private attachLocalVideo(track: { attach(): HTMLMediaElement } | undefined): void {
    if (this.disposed || !track || this.localVideoElement) return;
    const element = track.attach();
    if (this.disposed) {
      element.remove();
      return;
    }
    element.autoplay = true;
    element.muted = true;
    if (element instanceof HTMLVideoElement) element.playsInline = true;
    this.localVideoElement = element;
    this.callbacks.onLocalElement(element);
  }

  private removeLocalVideo(): void {
    if (!this.localVideoElement) return;
    this.callbacks.onElementRemoved(this.localVideoElement);
    this.localVideoElement = null;
  }
}

export const createLiveKitSpikeSession: LiveKitSpikeSessionFactory = (callbacks) =>
  new LiveKitSpikeSessionImpl(callbacks);

function mediaError(device: "camera" | "microphone", error: unknown): SpikeMediaError {
  const denied = MediaDeviceFailure.getFailure(error) === MediaDeviceFailure.PermissionDenied;
  return new SpikeMediaError(`${device}_${denied ? "denied" : "unavailable"}`);
}
