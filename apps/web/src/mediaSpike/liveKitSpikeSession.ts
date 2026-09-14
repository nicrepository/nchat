import {
  MediaDeviceFailure,
  Room,
  RoomEvent,
  Track,
  supportsAudioOutputSelection,
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

// Reuses the browser's own MediaDeviceKind union (audioinput/videoinput/
// audiooutput) rather than a parallel one — it is exactly the set
// Room.getLocalDevices/switchActiveDevice/RoomEvent.ActiveDeviceChanged
// already use (issue #755).
export type CallDeviceKind = MediaDeviceKind;

export interface CallMediaDevice {
  deviceId: string;
  kind: CallDeviceKind;
  label: string;
}

export type SpikeDeviceErrorKind = "denied" | "not_found" | "unavailable";

export class SpikeDeviceError extends Error {
  readonly deviceKind: CallDeviceKind;
  readonly kind: SpikeDeviceErrorKind;

  constructor(deviceKind: CallDeviceKind, kind: SpikeDeviceErrorKind) {
    super(`${deviceKind}_${kind}`);
    this.name = "SpikeDeviceError";
    this.deviceKind = deviceKind;
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
  // completes, and for every participant joining afterwards. displayName is
  // the server-issued LiveKit participant name (RF-24 follow-up); empty when
  // the SDK reports none, never the identity UUID.
  onParticipantConnected(identity: string, displayName: string): void;
  onParticipantDisconnected(identity: string): void;
  // A remote camera can be muted (publication kept, no unsubscribe) instead
  // of unpublished. The subscribed element is never removed for this —
  // muting is not "no video", it is "temporarily no usable frame" — so the
  // consumer must stop treating it as available (RF-24 grid fallback) without
  // losing the element, and restore it on unmute. Never fired for a
  // microphone: see onMicrophoneStateChanged/onRemoteAudioAvailabilityChanged.
  onRemoteVideoAvailabilityChanged(identity: string, available: boolean): void;
  // Mirrors the remote microphone publication's effective mute state. This
  // updates the existing RF-24 participant entry; it never creates a second
  // roster for presentation consumers.
  onRemoteAudioAvailabilityChanged(identity: string, available: boolean): void;
  onActiveSpeakersChanged(identities: string[]): void;
  onScreenShareChanged(enabled: boolean): void;
  // Local screen-share preview only — deliberately separate from
  // onLocalElement (camera) so a consumer can never conflate the two local
  // preview surfaces. Fired with the attached preview element after a
  // successful local enable/reconnect-resync, and with null on explicit
  // stop, native LocalTrackUnpublished, or teardown.
  onLocalScreenShareChanged(element: HTMLMediaElement | null): void;
  // trackSid identifies the specific LiveKit publication a remote
  // screen-share subscription belongs to — finer-grained than `identity`,
  // since the same participant can unpublish and republish (a new trackSid)
  // before the old publication's removal event arrives.
  //
  // trackSid ALONE is still not sufficient to key a removal safely: LiveKit
  // can in principle deliver a second TrackSubscribed for the very same
  // publication (same trackSid) under a brand-new RemoteTrack wrapper object
  // around a reconnect/rebind/replay, without the old wrapper's own
  // TrackUnsubscribed having arrived yet — this adapter's own dedup guard
  // (onTrackSubscribed's `this.remoteTracks.has(track)`) is keyed by
  // RemoteTrack object identity, not trackSid, so two such entries can
  // legitimately coexist with an identical trackSid for a window. A stale
  // removal for the OLD wrapper must never be mistaken for a removal of the
  // NEW one just because they share a trackSid.
  //
  // `element` is therefore always the concrete, already-attached element
  // this specific subscription instance owns — passed on BOTH the add
  // (`active: true`) and the matching remove (`active: false`) — so a
  // consumer can verify a removal refers to the exact instance its own
  // corresponding add created (e.g. `registry.get(trackSid)?.element ===
  // element`) before ever deleting anything, never trusting trackSid alone.
  onRemoteScreenShareChanged(
    identity: string,
    trackSid: string,
    element: HTMLMediaElement,
    active: boolean,
  ): void;
  // Device list changed (hot-plug: headset/webcam connected or removed) —
  // never fired for a mic/camera on/off toggle. The consumer should
  // re-enumerate; this never by itself implies the active device changed
  // (see onActiveDeviceChanged below).
  onDeviceListChanged(): void;
  // The SDK's own confirmed active device for `kind` — fired after a
  // successful switchActiveDevice() AND whenever the SDK itself falls back
  // to another device because the previously active one disappeared. This
  // is the single source of truth for "what device is really applied";
  // never inferred locally from the deviceId a caller merely requested.
  onActiveDeviceChanged(kind: CallDeviceKind, deviceId: string): void;
}

export interface LiveKitSpikeSession {
  startAudio(): Promise<void>;
  connect(serverUrl: string, token: string): Promise<void>;
  enableCamera(): Promise<void>;
  enableMicrophone(): Promise<void>;
  setCameraEnabled(enabled: boolean): Promise<void>;
  setMicrophoneEnabled(enabled: boolean): Promise<void>;
  setScreenShareEnabled(enabled: boolean): Promise<void>;
  disconnect(): Promise<void>;
  // Enumerates devices of one kind. Never requests permission by itself —
  // labels are empty until mic/camera permission was already granted
  // through the normal enable flow, and the caller renders a fallback name
  // for that case (issue #755: never capture media just to open the
  // selector).
  listMediaDevices(kind: CallDeviceKind): Promise<CallMediaDevice[]>;
  // The SDK-confirmed active device for `kind`, or undefined before any
  // capture/switch has established one.
  getActiveDevice(kind: CallDeviceKind): string | undefined;
  // Switches the device LiveKit uses for `kind`. Never captures a new
  // device when the corresponding track is currently disabled/unpublished —
  // it only updates the preferred-device option for the next capture, so
  // selecting another mic/camera while it is off can never turn it on.
  switchActiveDevice(kind: CallDeviceKind, deviceId: string): Promise<void>;
  // Real browser feature detection for `audiooutput` selection (setSinkId
  // support) — never assumed true.
  isAudioOutputSupported(): boolean;
}

export type LiveKitSpikeSessionFactory = (
  callbacks: LiveKitSpikeSessionCallbacks,
) => LiveKitSpikeSession;

class LiveKitSpikeSessionImpl implements LiveKitSpikeSession {
  private readonly room = new Room({ adaptiveStream: true, dynacast: true });
  private readonly remoteTracks = new Map<
    RemoteTrack,
    {
      participantSid: string;
      elements: Set<HTMLMediaElement>;
      screenShareIdentity?: string;
      screenShareTrackSid?: string;
      // The exact element THIS subscription instance attached — carried
      // through to the matching removal so a consumer can verify a removal
      // belongs to this instance rather than trusting trackSid alone (see
      // onRemoteScreenShareChanged's own doc comment).
      screenShareElement?: HTMLMediaElement;
    }
  >();
  private readonly callbacks: LiveKitSpikeSessionCallbacks;
  private localVideoElement: HTMLMediaElement | null = null;
  private localScreenShareElement: HTMLMediaElement | null = null;
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
      .on(RoomEvent.TrackUnmuted, this.onTrackUnmuted)
      .on(RoomEvent.ActiveSpeakersChanged, this.onActiveSpeakersChanged)
      .on(RoomEvent.LocalTrackUnpublished, this.onLocalTrackUnpublished)
      .on(RoomEvent.MediaDevicesChanged, this.onDeviceListChanged)
      .on(RoomEvent.ActiveDeviceChanged, this.onActiveDeviceChanged);
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
      this.callbacks.onParticipantConnected(participant.identity, participant.name ?? "");
    }
    // A camera publication's own current state is authoritative on join: a
    // late joiner must never infer "camera on" from the mere existence of a
    // subscribed track when the publisher already muted it — no future
    // TrackMuted is guaranteed for a mute that happened before we arrived.
    this.syncRemoteVideoAvailability();
    this.syncRemoteAudioAvailability();
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

  async setScreenShareEnabled(enabled: boolean): Promise<void> {
    if (this.disposed) return;
    const publication = await this.room.localParticipant.setScreenShareEnabled(enabled);
    if (this.disposed) {
      if (enabled) await this.disableScreenShareAfterInterruptedEnable();
      return;
    }
    if (enabled) {
      // Attach ONLY the already-created local screen track LiveKit just
      // published — never a second getDisplayMedia/createScreenTracks call.
      this.attachLocalScreenShare(publication?.track);
    } else {
      this.removeLocalScreenShare();
    }
    this.callbacks.onScreenShareChanged(enabled);
  }

  async listMediaDevices(kind: CallDeviceKind): Promise<CallMediaDevice[]> {
    if (this.disposed) return [];
    let devices: MediaDeviceInfo[];
    try {
      devices = await Room.getLocalDevices(kind, false);
    } catch (error) {
      throw deviceError(kind, error);
    }
    if (this.disposed) return [];
    return devices.map((device) => ({ deviceId: device.deviceId, kind, label: device.label }));
  }

  getActiveDevice(kind: CallDeviceKind): string | undefined {
    if (this.disposed) return undefined;
    return this.room.getActiveDevice(kind);
  }

  async switchActiveDevice(kind: CallDeviceKind, deviceId: string): Promise<void> {
    if (this.disposed) return;
    try {
      await this.room.switchActiveDevice(kind, deviceId);
    } catch (error) {
      throw deviceError(kind, error);
    }
  }

  isAudioOutputSupported(): boolean {
    return supportsAudioOutputSelection();
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
    const screenShare = publication.source === Track.Source.ScreenShare;
    this.remoteTracks.set(track, {
      participantSid: participant.sid,
      elements,
      ...(screenShare
        ? {
            screenShareIdentity: participant.identity,
            screenShareTrackSid: publication.trackSid,
            screenShareElement: element,
          }
        : {}),
    });
    if (screenShare) {
      this.callbacks.onRemoteScreenShareChanged(
        participant.identity,
        publication.trackSid,
        element,
        true,
      );
    } else {
      this.callbacks.onRemoteElement(element);
    }
    if (track.kind === Track.Kind.Audio) {
      if (publication.source === Track.Source.Microphone && publication.isMuted) {
        this.callbacks.onRemoteAudioAvailabilityChanged(participant.identity, false);
      }
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
    if (!this.disposed) {
      this.callbacks.onParticipantConnected(participant.identity, participant.name ?? "");
    }
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
    this.syncRemoteAudioAvailability();
    this.syncLocalScreenShareState();
    this.callbacks.onReconnected();
  };

  private readonly onAudioPlaybackChanged = (canPlaybackAudio: boolean): void => {
    if (!this.disposed) this.callbacks.onAudioPlaybackChanged(canPlaybackAudio);
  };

  private readonly onActiveSpeakersChanged = (participants: Participant[]): void => {
    if (!this.disposed) {
      this.callbacks.onActiveSpeakersChanged(participants.map(({ identity }) => identity));
    }
  };

  private readonly onLocalTrackUnpublished = (publication: LocalTrackPublication): void => {
    if (!this.disposed && publication.source === Track.Source.ScreenShare) {
      this.removeLocalScreenShare();
      this.callbacks.onScreenShareChanged(false);
    }
  };

  private readonly onDeviceListChanged = (): void => {
    if (!this.disposed) this.callbacks.onDeviceListChanged();
  };

  private readonly onActiveDeviceChanged = (kind: CallDeviceKind, deviceId: string): void => {
    if (!this.disposed) this.callbacks.onActiveDeviceChanged(kind, deviceId);
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
    if (this.isLocalScreenShare(publication, participant)) {
      // LiveKit unpublishes rather than muting local screen share (see
      // setTrackEnabled's disable branch), so this is defensive/idempotent
      // rather than an expected transition in normal operation.
      this.removeLocalScreenShare();
      this.callbacks.onScreenShareChanged(false);
      return;
    }
    if (this.isRemoteMicrophone(publication, participant)) {
      this.callbacks.onRemoteAudioAvailabilityChanged(participant.identity, false);
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
    if (this.isLocalScreenShare(publication, participant)) {
      this.callbacks.onScreenShareChanged(true);
      return;
    }
    if (this.isRemoteMicrophone(publication, participant)) {
      this.callbacks.onRemoteAudioAvailabilityChanged(participant.identity, true);
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

  private isLocalScreenShare(publication: TrackPublication, participant: Participant): boolean {
    return (
      participant === this.room.localParticipant && publication.source === Track.Source.ScreenShare
    );
  }

  // Never true for the local participant's own camera: RF-23/RF-24 both
  // drive the local preview from setCameraEnabled()/attachLocalVideo(), never
  // from a mute event, so a local self-mute must not be mistaken for a
  // remote participant's camera going away.
  private isRemoteCamera(publication: TrackPublication, participant: Participant): boolean {
    return participant !== this.room.localParticipant && publication.source === Track.Source.Camera;
  }

  private isRemoteMicrophone(publication: TrackPublication, participant: Participant): boolean {
    return (
      participant !== this.room.localParticipant && publication.source === Track.Source.Microphone
    );
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

  private syncRemoteAudioAvailability(): void {
    if (this.disposed) return;
    for (const participant of this.room.remoteParticipants.values()) {
      const publication = participant.getTrackPublication(Track.Source.Microphone);
      if (!publication?.isSubscribed) continue;
      this.callbacks.onRemoteAudioAvailabilityChanged(participant.identity, !publication.isMuted);
    }
  }

  private async dispose(): Promise<void> {
    try {
      await this.room.disconnect(true);
    } finally {
      this.removeAdapterListeners();
      this.removeLocalVideo();
      this.removeLocalScreenShare();
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
      .off(RoomEvent.TrackUnmuted, this.onTrackUnmuted)
      .off(RoomEvent.ActiveSpeakersChanged, this.onActiveSpeakersChanged)
      .off(RoomEvent.LocalTrackUnpublished, this.onLocalTrackUnpublished)
      .off(RoomEvent.MediaDevicesChanged, this.onDeviceListChanged)
      .off(RoomEvent.ActiveDeviceChanged, this.onActiveDeviceChanged);
  }

  private removeRemoteTrack(track: RemoteTrack): void {
    const remoteTrack = this.remoteTracks.get(track);
    if (!remoteTrack) return;
    this.remoteTracks.delete(track);
    if (
      remoteTrack.screenShareIdentity &&
      remoteTrack.screenShareTrackSid &&
      remoteTrack.screenShareElement
    ) {
      // Always the exact element THIS instance attached — never a
      // placeholder — so a consumer can verify this removal belongs to the
      // entry it currently has for this trackSid before deleting it.
      this.callbacks.onRemoteScreenShareChanged(
        remoteTrack.screenShareIdentity,
        remoteTrack.screenShareTrackSid,
        remoteTrack.screenShareElement,
        false,
      );
    }
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

  private async disableScreenShareAfterInterruptedEnable(): Promise<void> {
    try {
      await this.room.localParticipant.setScreenShareEnabled(false);
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

  // Deliberately separate from attachLocalVideo/removeLocalVideo (camera):
  // this owns its own dedicated callback (onLocalScreenShareChanged) rather
  // than routing through onElementRemoved, so a consumer can never conflate
  // the local camera preview with the local screen-share preview.
  private attachLocalScreenShare(track: { attach(): HTMLMediaElement } | undefined): void {
    if (this.disposed || !track || this.localScreenShareElement) return;
    const element = track.attach();
    if (this.disposed) {
      element.remove();
      return;
    }
    element.autoplay = true;
    element.muted = true;
    if (element instanceof HTMLVideoElement) element.playsInline = true;
    this.localScreenShareElement = element;
    this.callbacks.onLocalScreenShareChanged(element);
  }

  private removeLocalScreenShare(): void {
    if (!this.localScreenShareElement) return;
    this.localScreenShareElement.remove();
    this.localScreenShareElement = null;
    this.callbacks.onLocalScreenShareChanged(null);
  }

  // Reconnect resync only — never calls setScreenShareEnabled/
  // createScreenTracks/getDisplayMedia. Reads whatever the SDK's own
  // publication state already is after Reconnected and reattaches the
  // already-existing track if it survived (attachLocalScreenShare no-ops if
  // already attached), or converges to false/cleared if the publication
  // disappeared or its underlying capture ended while disconnected.
  private syncLocalScreenShareState(): void {
    if (this.disposed) return;
    const publication = this.room.localParticipant.getTrackPublication(Track.Source.ScreenShare);
    const track = publication?.track;
    if (!track || track.mediaStreamTrack.readyState === "ended") {
      this.removeLocalScreenShare();
      this.callbacks.onScreenShareChanged(false);
      return;
    }
    this.attachLocalScreenShare(track);
    this.callbacks.onScreenShareChanged(true);
  }
}

export const createLiveKitSpikeSession: LiveKitSpikeSessionFactory = (callbacks) =>
  new LiveKitSpikeSessionImpl(callbacks);

function mediaError(device: "camera" | "microphone", error: unknown): SpikeMediaError {
  const denied = MediaDeviceFailure.getFailure(error) === MediaDeviceFailure.PermissionDenied;
  return new SpikeMediaError(`${device}_${denied ? "denied" : "unavailable"}`);
}

function deviceError(kind: CallDeviceKind, error: unknown): SpikeDeviceError {
  const failure = MediaDeviceFailure.getFailure(error);
  const mapped: SpikeDeviceErrorKind =
    failure === MediaDeviceFailure.PermissionDenied
      ? "denied"
      : failure === MediaDeviceFailure.NotFound
        ? "not_found"
        : "unavailable";
  return new SpikeDeviceError(kind, mapped);
}
