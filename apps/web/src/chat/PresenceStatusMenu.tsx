/**
 * PresenceStatusMenu — the viewer's quick status control (issue #798).
 *
 * One trigger in the sidebar footer, one menu: the six states the product
 * offers, a duration for whichever is chosen, a way back to automatic, and the
 * existing "Definir mensagem de status" path (the profile's custom status — it
 * is not reimplemented here, and it is not presence).
 *
 * Everything shown is what the server holds. A choice is shown at once and
 * replaced by the server's answer; a refused one goes back to the server's
 * state with "Não foi possível atualizar seu status." and a retry.
 *
 * Keyboard: Enter/Space choose (they are buttons), arrows move, Escape closes
 * and returns focus to the trigger. Touch: every item is a full-width button,
 * and on a narrow screen the menu is a bottom sheet — nothing depends on hover.
 */

import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router";

import "./PresenceStatusMenu.css";
import { usePresenceDetail } from "./presence";
import PresenceDot from "./PresenceDot";
import {
  formatPresenceUntil,
  manualDotState,
  manualPresenceLabels,
  manualPresenceStates,
  MIN_PRESENCE_DURATION_MS,
  parseLocalDateTime,
  presenceDurationLabels,
  presenceExpiry,
  summarizeSelfPresence,
  usePresenceSettings,
  type ManualPresenceState,
  type PresenceDuration,
  type PresenceSettingsController,
  type SelfPresenceSummary,
} from "./presenceSettings";

const durations: readonly PresenceDuration[] = ["1h", "4h", "today", "week"];

type Step =
  | { kind: "states" }
  | { kind: "duration"; state: ManualPresenceState }
  | { kind: "custom"; state: ManualPresenceState };

const statesStep: Step = { kind: "states" };

/** Focuses the first enabled item of the popup, or a given selector. */
function focusFirst(popup: HTMLElement | null, selector = "[role^='menuitem'], input, button") {
  popup?.querySelector<HTMLElement>(selector)?.focus();
}

function moveFocus(popup: HTMLElement | null, delta: number) {
  const items = Array.from(popup?.querySelectorAll<HTMLElement>("[role^='menuitem']") ?? []);
  if (items.length === 0) return;
  const current = items.indexOf(document.activeElement as HTMLElement);
  const next =
    ((((current < 0 ? -1 : current) + delta) % items.length) + items.length) % items.length;
  items[next]?.focus();
}

/** Closes on a press or focus outside the trigger and the popup. */
function useOutsideClose(
  open: boolean,
  refs: { trigger: HTMLElement | null; popup: HTMLElement | null },
  close: () => void,
) {
  const { trigger, popup } = refs;
  useEffect(() => {
    if (!open) return;
    const closeIfOutside = (event: Event) => {
      const target = event.target as Node | null;
      if (popup?.contains(target) || trigger?.contains(target)) return;
      close();
    };
    document.addEventListener("pointerdown", closeIfOutside);
    document.addEventListener("focusin", closeIfOutside);
    return () => {
      document.removeEventListener("pointerdown", closeIfOutside);
      document.removeEventListener("focusin", closeIfOutside);
    };
  }, [open, trigger, popup, close]);
}

export default function PresenceStatusMenu({
  selfId,
  displayName,
}: {
  selfId: string;
  displayName: string;
}) {
  const controller = usePresenceSettings();
  const detail = usePresenceDetail(selfId);
  const summary = summarizeSelfPresence(controller.settings, detail, controller.pending);
  const [open, setOpen] = useState(false);
  const [step, setStep] = useState<Step>(statesStep);
  const [trigger, setTrigger] = useState<HTMLButtonElement | null>(null);
  const [popup, setPopup] = useState<HTMLDivElement | null>(null);
  const restoreFocus = useRef(false);
  const popupId = useId();

  const close = useCallback((returnFocus = false) => {
    restoreFocus.current = returnFocus;
    setOpen(false);
    setStep(statesStep);
  }, []);
  const closeOutside = useCallback(() => close(false), [close]);
  useOutsideClose(open, { trigger, popup }, closeOutside);

  useEffect(() => {
    if (open) {
      focusFirst(popup);
      return;
    }
    if (restoreFocus.current) {
      restoreFocus.current = false;
      trigger?.focus();
    }
  }, [open, step, popup, trigger]);

  const choose = (state: ManualPresenceState, expiresAt: number) => {
    controller.set(state, expiresAt);
    close(true);
  };

  return (
    <div className="presence-status">
      <button
        ref={setTrigger}
        type="button"
        className="presence-status__trigger"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? popupId : undefined}
        aria-busy={controller.pending}
        aria-label={
          summary.dot === "unknown" ? "Alterar status" : `Status: ${summary.label}. Alterar status`
        }
        onClick={() => (open ? close(false) : setOpen(true))}
      >
        <PresenceDot state={summary.dot} inline title={summary.label} />
        <span className="presence-status__trigger-label" aria-hidden="true">
          {summary.label}
        </span>
      </button>
      {controller.failed && <StatusError onRetry={controller.retry} />}
      {open &&
        createPortal(
          <StatusPopup
            id={popupId}
            popupRef={setPopup}
            trigger={trigger}
            step={step}
            setStep={setStep}
            close={close}
            choose={choose}
            controller={controller}
            displayName={displayName}
            summary={summary}
          />,
          document.body,
        )}
    </div>
  );
}

function StatusError({ onRetry }: { onRetry: () => void }) {
  return (
    <p className="presence-status__error" role="alert">
      Não foi possível atualizar seu status.{" "}
      <button type="button" className="presence-status__retry" onClick={onRetry}>
        Tentar novamente
      </button>
    </p>
  );
}

interface StatusPopupProps {
  id: string;
  popupRef: (node: HTMLDivElement | null) => void;
  trigger: HTMLElement | null;
  step: Step;
  setStep: (step: Step) => void;
  close: (returnFocus?: boolean) => void;
  choose: (state: ManualPresenceState, expiresAt: number) => void;
  controller: PresenceSettingsController;
  displayName: string;
  summary: SelfPresenceSummary;
}

/** Matches the stylesheet's bottom-sheet breakpoint. */
const SHEET_QUERY = "(max-width: 640px)";

/**
 * Places the popup above the trigger, inside the viewport. On a narrow screen
 * the stylesheet docks it to the bottom instead, and nothing is computed.
 */
function placePopup(node: HTMLDivElement, trigger: HTMLElement | null) {
  const anchor = trigger?.getBoundingClientRect();
  if (!anchor || window.matchMedia?.(SHEET_QUERY).matches) return;
  const gap = 6;
  const margin = 8;
  const height = node.offsetHeight;
  const above = anchor.top - gap - height >= margin;
  node.style.top = `${above ? anchor.top - gap - height : anchor.bottom + gap}px`;
  const maxLeft = window.innerWidth - node.offsetWidth - margin;
  node.style.left = `${Math.max(margin, Math.min(anchor.left, maxLeft))}px`;
}

function StatusPopup(props: StatusPopupProps) {
  const { id, popupRef, trigger, step, close } = props;
  const [node, setNode] = useState<HTMLDivElement | null>(null);
  const attach = useCallback(
    (element: HTMLDivElement | null) => {
      setNode(element);
      popupRef(element);
    },
    [popupRef],
  );
  useEffect(() => {
    if (node) placePopup(node, trigger);
  }, [node, trigger, step]);

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    // Arrows belong to the menu only on its items: in the date-time field they
    // change the value, and taking them would leave it keyboard-unusable.
    const onItem = event.target instanceof Element && event.target.matches("[role^='menuitem']");
    const keys: Record<string, (() => void) | undefined> = {
      Escape: () => close(true),
      ArrowDown: onItem ? () => moveFocus(node, 1) : undefined,
      ArrowUp: onItem ? () => moveFocus(node, -1) : undefined,
    };
    const action = keys[event.key];
    if (!action) return;
    event.preventDefault();
    action();
  };

  const custom = step.kind === "custom";
  return (
    <div
      ref={attach}
      id={id}
      role={custom ? "dialog" : "menu"}
      aria-label={custom ? "Duração personalizada" : "Status"}
      className="chat-sidebar__actions-menu presence-status__popup"
      onKeyDown={onKeyDown}
    >
      <StepContent {...props} />
    </div>
  );
}

function StepContent(props: StatusPopupProps) {
  const { step } = props;
  if (step.kind === "duration") return <DurationList {...props} state={step.state} />;
  if (step.kind === "custom") return <CustomDuration {...props} state={step.state} />;
  return <StateList {...props} />;
}

function StateList(props: StatusPopupProps) {
  const { controller, close, displayName, summary } = props;
  const [openedAt] = useState(() => Date.now());
  const expiresAt = controller.settings?.expiresAt ?? null;
  return (
    <>
      <div className="presence-status__header" role="none">
        <span className="presence-status__name">{displayName || "Você"}</span>
        <span className="presence-status__current">
          <PresenceDot state={summary.dot} inline />
          {summary.label}
          {expiresAt !== null && ` · ${formatPresenceUntil(expiresAt, openedAt)}`}
        </span>
      </div>
      <span className="chat-sidebar__actions-separator" role="none" />
      {/* Only what the server said it accepts is offered; a stored state is
          shown either way. */}
      {controller.settings?.writable === true ? (
        <StateChoices {...props} />
      ) : (
        <span className="presence-status__unavailable" role="none">
          Alterar o status não está disponível no momento.
        </span>
      )}
      <span className="chat-sidebar__actions-separator" role="none" />
      <Link
        to="/profile"
        role="menuitem"
        tabIndex={-1}
        className="chat-sidebar__actions-item presence-status__item"
        onClick={() => close(false)}
      >
        <span className="chat-sidebar__actions-label">Definir mensagem de status</span>
      </Link>
    </>
  );
}

function StateChoices({ controller, setStep, close }: StatusPopupProps) {
  const current = controller.settings?.state ?? null;
  return (
    <>
      {manualPresenceStates.map((state) => (
        <button
          key={state}
          type="button"
          role="menuitemradio"
          aria-checked={current === state}
          tabIndex={-1}
          className="chat-sidebar__actions-item presence-status__item"
          onClick={() => setStep({ kind: "duration", state })}
        >
          <PresenceDot state={manualDotState[state]} inline />
          <span className="chat-sidebar__actions-label">{manualPresenceLabels[state]}</span>
        </button>
      ))}
      {current && (
        <button
          type="button"
          role="menuitem"
          tabIndex={-1}
          className="chat-sidebar__actions-item presence-status__item"
          onClick={() => {
            controller.clear();
            close(true);
          }}
        >
          <span className="chat-sidebar__actions-label">Redefinir status</span>
        </button>
      )}
    </>
  );
}

function DurationList({
  state,
  setStep,
  choose,
}: StatusPopupProps & { state: ManualPresenceState }) {
  return (
    <>
      <div className="presence-status__header" role="none">
        <span className="presence-status__name">{manualPresenceLabels[state]}</span>
        <span className="presence-status__current">Por quanto tempo?</span>
      </div>
      <span className="chat-sidebar__actions-separator" role="none" />
      {durations.map((duration) => (
        <button
          key={duration}
          type="button"
          role="menuitem"
          tabIndex={-1}
          className="chat-sidebar__actions-item presence-status__item"
          onClick={() => choose(state, presenceExpiry(duration, Date.now()))}
        >
          <span className="chat-sidebar__actions-label">{presenceDurationLabels[duration]}</span>
        </button>
      ))}
      <button
        type="button"
        role="menuitem"
        tabIndex={-1}
        className="chat-sidebar__actions-item presence-status__item"
        onClick={() => setStep({ kind: "custom", state })}
      >
        <span className="chat-sidebar__actions-label">Personalizado…</span>
      </button>
      <span className="chat-sidebar__actions-separator" role="none" />
      <button
        type="button"
        role="menuitem"
        tabIndex={-1}
        className="chat-sidebar__actions-item presence-status__item"
        onClick={() => setStep(statesStep)}
      >
        <span className="chat-sidebar__actions-label">Voltar</span>
      </button>
    </>
  );
}

function CustomDuration({
  state,
  setStep,
  choose,
}: StatusPopupProps & { state: ManualPresenceState }) {
  const inputId = useId();
  const [value, setValue] = useState("");
  const [invalid, setInvalid] = useState(false);
  const submit = () => {
    const expiresAt = parseLocalDateTime(value);
    if (!(expiresAt >= Date.now() + MIN_PRESENCE_DURATION_MS)) {
      setInvalid(true);
      return;
    }
    choose(state, expiresAt);
  };
  return (
    <form
      className="presence-status__custom"
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      <label htmlFor={inputId} className="presence-status__custom-label">
        {manualPresenceLabels[state]} até
      </label>
      <input
        id={inputId}
        type="datetime-local"
        className="presence-status__custom-input"
        value={value}
        aria-invalid={invalid}
        aria-describedby={invalid ? `${inputId}-error` : undefined}
        onChange={(event) => {
          setValue(event.target.value);
          setInvalid(false);
        }}
      />
      {invalid && (
        <span id={`${inputId}-error`} className="presence-status__custom-error">
          Escolha um horário futuro.
        </span>
      )}
      <div className="presence-status__custom-actions">
        <button
          type="button"
          className="presence-status__custom-button"
          onClick={() => setStep({ kind: "duration", state })}
        >
          Voltar
        </button>
        <button
          type="submit"
          className="presence-status__custom-button presence-status__custom-button--primary"
        >
          Aplicar
        </button>
      </div>
    </form>
  );
}
