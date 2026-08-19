export interface CallControlProps {
  microphoneEnabled: boolean;
  cameraEnabled: boolean;
  screenShareEnabled: boolean;
  pendingControl: "microphone" | "camera" | "screen-share" | null;
  onMicrophone: () => unknown;
  onCamera: () => unknown;
  onScreenShare: () => unknown;
  onEnd: () => unknown;
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
