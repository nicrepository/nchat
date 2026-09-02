import DeviceSettingsMenu, { type DeviceSettingsProps } from "./DeviceSettingsMenu";

export interface CallControlProps {
  microphoneEnabled: boolean;
  cameraEnabled: boolean;
  screenShareEnabled: boolean;
  pendingControl: "microphone" | "camera" | "screen-share" | null;
  onMicrophone: () => unknown;
  onCamera: () => unknown;
  onScreenShare: () => unknown;
  onEnd: () => unknown;
  /**
   * Device selection (issue #755). Optional so every existing fixture that
   * builds a CallControlProps without it keeps working unchanged — the two
   * real call sites (CallSessionProvider, DedicatedCallPage) always pass it.
   * Omitted entirely hides the trigger, never renders it disabled/broken.
   */
  devices?: DeviceSettingsProps;
}

export default function CallControls({
  microphoneEnabled,
  cameraEnabled,
  screenShareEnabled,
  pendingControl,
  onMicrophone,
  onCamera,
  onScreenShare,
  onEnd,
  devices,
}: CallControlProps) {
  return (
    <div className="call-presentation__controls" aria-label="Controles da chamada">
      <Control
        label={microphoneEnabled ? "Desativar microfone" : "Ativar microfone"}
        icon={microphoneEnabled ? "mic" : "mic_off"}
        pressed={microphoneEnabled}
        disabled={pendingControl === "microphone"}
        onClick={onMicrophone}
      />
      <Control
        label={cameraEnabled ? "Desativar câmera" : "Ativar câmera"}
        icon={cameraEnabled ? "videocam" : "videocam_off"}
        pressed={cameraEnabled}
        disabled={pendingControl === "camera"}
        onClick={onCamera}
      />
      <Control
        label={screenShareEnabled ? "Parar compartilhamento de tela" : "Compartilhar tela"}
        icon={screenShareEnabled ? "stop_screen_share" : "screen_share"}
        pressed={screenShareEnabled}
        disabled={pendingControl === "screen-share"}
        onClick={onScreenShare}
      />
      {devices && <DeviceSettingsMenu {...devices} />}
      <Control label="Encerrar chamada" icon="call_end" danger onClick={onEnd} />
    </div>
  );
}

function Control({
  label,
  icon,
  pressed,
  disabled,
  danger = false,
  onClick,
}: {
  label: string;
  icon: string;
  pressed?: boolean;
  disabled?: boolean;
  danger?: boolean;
  onClick: () => unknown;
}) {
  return (
    <button
      type="button"
      className={`call-presentation__control${danger ? " call-presentation__control--danger" : ""}`}
      aria-label={label}
      aria-pressed={pressed}
      disabled={disabled}
      onClick={onClick}
    >
      <span className="material-symbols-outlined" aria-hidden="true">
        {icon}
      </span>
    </button>
  );
}
