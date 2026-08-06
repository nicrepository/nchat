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
  private disconnectPromise: Promise<void> | null = null;

  constructor(callbacks: LiveKitSpikeSessionCallbacks) {
    this.callbacks = callbacks;
    this.room
      .on(RoomEvent.TrackSubscribed, this.onTrackSubscribed)
      .on(RoomEvent.TrackUnsubscribed, this.onTrackUnsubscribed)
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
    if (this.disposed) await this.room.disconnect(true);
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
    if (this.disconnectPromise) return this.disconnectPromise;
    this.disposed = true;
    this.disconnectPromise = this.dispose();
    return this.disconnectPromise;
  }

  private readonly onTrackSubscribed = (
    track: RemoteTrack,
    _publication: RemoteTrackPublication,
    participant: RemoteParticipant,
  ): void => {
    if (
      this.disposed ||
      this.remoteTracks.has(track) ||
      (track.kind !== Track.Kind.Video && track.kind !== Track.Kind.Audio)
    ) {
      return;
    }
    const element = track.attach();
    element.autoplay = true;
    const elements = new Set([element]);
    this.remoteTracks.set(track, { participantSid: participant.sid, elements });
    this.callbacks.onRemoteElement(element);
    if (track.kind === Track.Kind.Audio) {
      void element.play().catch(() => {
        if (!this.disposed) this.callbacks.onAudioPlaybackChanged(false);
      });
    }
  };

  private readonly onTrackUnsubscribed = (track: RemoteTrack): void => {
    if (this.disposed) return;
    this.removeRemoteTrack(track);
  };

  private readonly onParticipantDisconnected = (participant: RemoteParticipant): void => {
    if (this.disposed) return;
    for (const [track, remoteTrack] of this.remoteTracks) {
      if (remoteTrack.participantSid === participant.sid) this.removeRemoteTrack(track);
    }
  };

  private readonly onDisconnected = (): void => {
    if (!this.disposed) this.callbacks.onDisconnected();
  };

  private readonly onReconnecting = (): void => {
    if (!this.disposed) this.callbacks.onReconnecting();
  };

  private readonly onReconnected = (): void => {
    if (this.disposed) return;
    // The publication may have been muted/unmuted server-side, or recreated,
    // while the signal connection was down: never assume the pre-reconnect
    // React state still matches the SDK after Reconnected.
    this.notifyMicrophoneState();
    this.callbacks.onReconnected();
  };

  private readonly onAudioPlaybackChanged = (canPlaybackAudio: boolean): void => {
    if (!this.disposed) this.callbacks.onAudioPlaybackChanged(canPlaybackAudio);
  };

  private readonly onTrackMuted = (
    publication: TrackPublication,
    participant: Participant,
  ): void => {
    if (this.disposed || !this.isLocalMicrophone(publication, participant)) return;
    this.callbacks.onMicrophoneStateChanged(false);
  };

  private readonly onTrackUnmuted = (
    publication: TrackPublication,
    participant: Participant,
  ): void => {
    if (this.disposed || !this.isLocalMicrophone(publication, participant)) return;
    this.callbacks.onMicrophoneStateChanged(true);
  };

  private isLocalMicrophone(publication: TrackPublication, participant: Participant): boolean {
    return (
      participant === this.room.localParticipant && publication.source === Track.Source.Microphone
    );
  }

  private notifyMicrophoneState(): void {
    if (this.disposed) return;
    const publication = this.room.localParticipant.getTrackPublication(Track.Source.Microphone);
    this.callbacks.onMicrophoneStateChanged(publication ? !publication.isMuted : false);
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
