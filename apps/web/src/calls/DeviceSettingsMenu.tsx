import { useCallback, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";

import type { CallDeviceKind, CallMediaDevice } from "../media/liveKitSession";

export interface DeviceSettingsProps {
  mediaDevices: Record<CallDeviceKind, CallMediaDevice[]>;
  activeDeviceId: Partial<Record<CallDeviceKind, string>>;
  devicePendingKinds: CallDeviceKind[];
  deviceError: string | null;
  audioOutputSupported: boolean;
  onSwitch: (kind: CallDeviceKind, deviceId: string) => void;
}

const kindOrder: CallDeviceKind[] = ["audioinput", "videoinput", "audiooutput"];
const kindLabels: Record<CallDeviceKind, string> = {
  audioinput: "Microfone",
  videoinput: "Câmera",
  audiooutput: "Saída de áudio",
};

function deviceLabel(device: CallMediaDevice, index: number): string {
  // Labels can be empty before the browser has granted permission (issue
  // #755) — never render the raw deviceId, fall back to a readable ordinal.
  const label = device.label.trim();
  return label !== "" ? label : `${kindLabels[device.kind]} ${index + 1}`;
}

const panelWidth = 260;
const panelGap = 8;
const viewportMargin = 8;
// A crude estimate (3 fields + status/error rows) — good enough to avoid a
// double-measure pass; clamping below still keeps it fully on screen even
// when the real height differs.
const estimatedPanelHeight = 240;

interface PanelPosition {
  top: number;
  left: number;
}

function panelPosition(trigger: DOMRect): PanelPosition {
  const opensAbove =
    trigger.bottom + panelGap + estimatedPanelHeight > window.innerHeight &&
    trigger.top > estimatedPanelHeight;
  const top = opensAbove
    ? trigger.top - panelGap - estimatedPanelHeight
    : trigger.bottom + panelGap;
  const maxLeft = window.innerWidth - panelWidth - viewportMargin;
  const left = Math.min(
    Math.max(viewportMargin, trigger.right - panelWidth),
    Math.max(viewportMargin, maxLeft),
  );
  return { top: Math.max(viewportMargin, top), left };
}

/**
 * "Configurações de áudio e vídeo" — the shared device picker (issue #755),
 * rendered once by CallControls and therefore identical in FloatingCallWindow
 * and DedicatedCallStage: same component, same props, sourced from the same
 * useCallMedia state, so the two surfaces can never disagree about which
 * device is selected.
 *
 * Portalled into <body> (same reasoning as ConversationActionsMenu): the
 * floating call window is a small, draggable, near-edge surface, and an
 * absolutely positioned descendant of it would clip or push it around.
 * Position is fixed viewport coordinates, clamped so it never runs off
 * screen near an edge.
 */
export default function DeviceSettingsMenu({
  mediaDevices,
  activeDeviceId,
  devicePendingKinds,
  deviceError,
  audioOutputSupported,
  onSwitch,
}: DeviceSettingsProps) {
  const panelId = useId();
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const restoreFocusRef = useRef(false);

  const close = useCallback((restoreFocus: boolean) => {
    restoreFocusRef.current = restoreFocus;
    setOpen(false);
  }, []);

  const applyPosition = useCallback(() => {
    const node = panelRef.current;
    const trigger = triggerRef.current?.getBoundingClientRect();
    if (!node || !trigger) return;
    const { top, left } = panelPosition(trigger);
    node.style.top = `${top}px`;
    node.style.left = `${left}px`;
  }, []);

  // A callback ref, so the panel is positioned in the same commit that mounts
  // it — never a visible jump from (0,0) to its real spot.
  const attachPanel = useCallback(
    (node: HTMLDivElement | null) => {
      panelRef.current = node;
      if (node) applyPosition();
    },
    [applyPosition],
  );

  useEffect(() => {
    if (!open) return;
    window.addEventListener("scroll", applyPosition, true);
    window.addEventListener("resize", applyPosition);
    return () => {
      window.removeEventListener("scroll", applyPosition, true);
      window.removeEventListener("resize", applyPosition);
    };
  }, [open, applyPosition]);

  // Focus lands on the first usable field when the panel opens, and returns
  // to the trigger only when a keyboard/outside-click gesture closed it —
  // never when a device switch itself re-renders while open.
  useEffect(() => {
    if (open) {
      panelRef.current?.querySelector<HTMLSelectElement>("select:not(:disabled)")?.focus();
      return;
    }
    if (restoreFocusRef.current) {
      restoreFocusRef.current = false;
      triggerRef.current?.focus();
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const closeIfOutside = (event: Event) => {
      const target = event.target as Node | null;
      if (panelRef.current?.contains(target) || triggerRef.current?.contains(target)) return;
      close(false);
    };
    document.addEventListener("mousedown", closeIfOutside);
    document.addEventListener("focusin", closeIfOutside);
    return () => {
      document.removeEventListener("mousedown", closeIfOutside);
      document.removeEventListener("focusin", closeIfOutside);
    };
  }, [open, close]);

  return (
    <div className="call-device-menu">
      <button
        ref={triggerRef}
        type="button"
        className="call-presentation__control"
        aria-label="Configurações de áudio e vídeo"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        onClick={() => {
          restoreFocusRef.current = false;
          setOpen((current) => !current);
        }}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          tune
        </span>
      </button>
      {open &&
        createPortal(
          <div
            ref={attachPanel}
            id={panelId}
            role="dialog"
            aria-label="Configurações de áudio e vídeo"
            aria-modal="false"
            className="call-device-menu__panel"
            onKeyDown={(event) => {
              if (event.key !== "Escape") return;
              event.preventDefault();
              event.stopPropagation();
              close(true);
            }}
            // Never lets a click inside the panel reach a control underneath
            // it (issue #755: the overlay must not toggle mic/camera/end).
            onMouseDown={(event) => event.stopPropagation()}
          >
            {kindOrder.map((kind) => {
              if (kind === "audiooutput" && !audioOutputSupported) {
                return (
                  <p key={kind} className="call-device-menu__unsupported">
                    O navegador controla a saída de áudio
                  </p>
                );
              }
              const devices = mediaDevices[kind];
              const selectId = `${panelId}-${kind}`;
              const pending = devicePendingKinds.includes(kind);
              return (
                <label key={kind} className="call-device-menu__field" htmlFor={selectId}>
                  <span>{kindLabels[kind]}</span>
                  <select
                    id={selectId}
                    value={activeDeviceId[kind] ?? ""}
                    disabled={pending || devices.length === 0}
                    onChange={(event) => onSwitch(kind, event.target.value)}
                  >
                    {devices.length === 0 && <option value="">Carregando dispositivos…</option>}
                    {devices.map((device, index) => (
                      <option key={device.deviceId} value={device.deviceId}>
                        {deviceLabel(device, index)}
                      </option>
                    ))}
                  </select>
                </label>
              );
            })}
            <p className="call-device-menu__status" role="status" aria-live="polite">
              {devicePendingKinds.length > 0 ? "Alterando dispositivo…" : ""}
            </p>
            {deviceError && (
              <p className="call-device-menu__error" role="alert">
                {deviceError}
              </p>
            )}
          </div>,
          document.body,
        )}
    </div>
  );
}
