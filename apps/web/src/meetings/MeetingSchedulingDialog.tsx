import { useEffect, useRef, useState, useSyncExternalStore, type SubmitEvent } from "react";
import { createPortal } from "react-dom";
import MeetingFields from "./MeetingFields";
import MeetingParticipants from "./MeetingParticipants";
import {
  meetingBusy,
  meetingDirty,
  meetingLocked,
  type MeetingSession,
  type MeetingSnapshot,
} from "./meetingSession";
import { meetingPayload, validateMeeting, validateMeetingTime } from "./meetingTime";
import type { MeetingDraft, MeetingErrors, MeetingOperation } from "./meetingTypes";
import { trapMeetingFocus } from "./meetingDialogFocus";
import "./MeetingSchedulingDialog.css";

export default function MeetingSchedulingDialog({
  session,
  organizerId,
  onClose,
}: Readonly<{
  session: MeetingSession;
  organizerId: string;
  onClose: (discard: boolean) => void;
}>) {
  const snapshot = useSyncExternalStore(session.subscribe, session.getSnapshot);
  const { draft, operation } = snapshot;
  const busy = meetingBusy(operation);
  const locked = meetingLocked(operation);
  const [errors, setErrors] = useState<MeetingErrors>({});
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    heading.current?.focus({ preventScroll: true });
    return () => element?.close();
  }, []);
  function finishClose(discard: boolean) {
    dialog.current?.close();
    onClose(discard);
  }
  function close() {
    if (busy) return;
    if (confirmDiscard) {
      setConfirmDiscard(false);
      heading.current?.focus({ preventScroll: true });
      return;
    }
    finishClose(operation.stage === "complete");
  }
  function change<K extends keyof MeetingDraft>(key: K, value: MeetingDraft[K]) {
    const nextDraft = { ...session.getSnapshot().draft, [key]: value };
    if (["date", "time", "timezone"].includes(key)) nextDraft.occurrence = "";
    session.update({ draft: nextDraft });
    setErrors((current) => changedFieldErrors(current, key, nextDraft));
  }
  function submit(event: SubmitEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    if (operation.stage === "invite-error") {
      void session.submit();
      return;
    }
    const validation = validateMeeting(draft);
    if (
      !Object.keys(validation).length &&
      new TextEncoder().encode(JSON.stringify(meetingPayload(draft, session.target, organizerId)))
        .length > 65536
    )
      validation.participants = "A seleção excede o tamanho permitido. Remova algumas pessoas.";
    setErrors(validation);
    const first = Object.keys(validation)[0];
    if (first) {
      const field = first === "duration" && draft.duration === "custom" ? "customDuration" : first;
      dialog.current?.querySelector<HTMLElement>(`#meeting-${field}`)?.focus();
      return;
    }
    void session.submit();
  }
  return createPortal(
    <dialog
      ref={dialog}
      className="meeting-dialog"
      onKeyDown={trapMeetingFocus}
      aria-labelledby="meeting-title-heading"
      aria-describedby="meeting-context"
      onCancel={(e) => {
        e.preventDefault();
        close();
      }}
    >
      <header>
        <div>
          <h2 id="meeting-title-heading" ref={heading} tabIndex={-1}>
            Agendar reunião
          </h2>
          <p id="meeting-context">{session.target.name}</p>
        </div>
        <button type="button" aria-label="Fechar agendamento" disabled={busy} onClick={close}>
          Fechar
        </button>
      </header>
      {operation.stage === "complete" ? (
        <div className="meeting-content">
          <p role="status">
            {operation.replayed
              ? "Reunião já registrada, recuperada com segurança."
              : "Reunião criada com sucesso."}{" "}
            Estado: {operation.meeting.status}.
          </p>
          {operation.added !== undefined && (
            <p>
              Convite coletivo concluído. {operation.added} novo(s) participante(s) adicionado(s).
            </p>
          )}
          <p>Identificador da reunião: {operation.meeting.id}</p>
          <button type="button" onClick={() => finishClose(true)}>
            Concluir
          </button>
        </div>
      ) : (
        <form
          noValidate
          onSubmit={submit}
          onBlur={(e) => {
            const key = e.target.getAttribute("name") as keyof MeetingDraft | null;
            if (key)
              setErrors((current) => ({
                ...current,
                [key]: validateMeeting(session.getSnapshot().draft)[key],
              }));
          }}
        >
          <div className="meeting-content">
            <OperationNotice operation={operation} />
            <fieldset disabled={locked} aria-busy={busy}>
              <legend className="meeting-sr-only">Dados do agendamento</legend>
              <MeetingFields draft={draft} errors={errors} change={change} />
              <MeetingParticipants
                session={session}
                organizerId={organizerId}
                draft={draft}
                locked={locked}
              />
              {errors.participants && (
                <p id="meeting-participants-error" role="alert" className="meeting-error">
                  {errors.participants}
                </p>
              )}
            </fieldset>
            {confirmDiscard && (
              <div
                role="alert"
                className="meeting-discard"
                tabIndex={-1}
                ref={(node) => node?.focus()}
              >
                <p>Descartar este rascunho de agendamento?</p>
                <button
                  type="button"
                  onClick={() => {
                    setConfirmDiscard(false);
                    heading.current?.focus({ preventScroll: true });
                  }}
                >
                  Continuar editando
                </button>
                <button type="button" onClick={() => finishClose(true)}>
                  Confirmar descarte
                </button>
              </div>
            )}
          </div>
          <MeetingActions
            snapshot={snapshot}
            onClose={close}
            onDiscard={() => setConfirmDiscard(true)}
            onFinishPartial={session.finishWithoutBulk}
          />
        </form>
      )}
    </dialog>,
    document.body,
  );
}

function MeetingActions({
  snapshot,
  onClose,
  onDiscard,
  onFinishPartial,
}: Readonly<{
  snapshot: MeetingSnapshot;
  onClose: () => void;
  onDiscard: () => void;
  onFinishPartial: () => void;
}>) {
  const { operation } = snapshot;
  const busy = meetingBusy(operation);
  const locked = meetingLocked(operation);
  const dirty = meetingDirty(snapshot);
  return (
    <footer>
      <p className="meeting-hint">
        {dirty
          ? "Rascunho com alterações. Fechar preserva os dados nesta sessão."
          : "O rascunho fica disponível nesta sessão."}
      </p>
      <div className="meeting-actions">
        {!locked && dirty && (
          <button type="button" onClick={onDiscard}>
            Descartar rascunho
          </button>
        )}
        {operation.stage === "invite-error" && (
          <button type="button" onClick={onFinishPartial}>
            Finalizar com a seleção original
          </button>
        )}
        <button type="button" disabled={busy} onClick={onClose}>
          Continuar depois
        </button>
        <button
          className="meeting-primary"
          type="submit"
          disabled={busy || !snapshot.seeded}
          aria-busy={busy}
        >
          {submitLabel(operation)}
        </button>
      </div>
    </footer>
  );
}

function OperationNotice({ operation }: Readonly<{ operation: MeetingOperation }>) {
  return (
    <>
      {operation.stage === "invite-error" && (
        <div role="alert">
          <p>A reunião foi criada. O convite coletivo ainda não foi confirmado.</p>
          <p>Identificador da reunião: {operation.meeting.id}</p>
          <p>{operation.message}</p>
        </div>
      )}
      {operation.stage === "create-error" && (
        <p role="alert">
          {operation.message}
          {operation.uncertain &&
            " Os dados estão preservados e bloqueados até confirmar esta tentativa."}
        </p>
      )}
      {operation.stage === "inviting" && (
        <p role="status">Reunião criada. Convidando membros elegíveis…</p>
      )}
    </>
  );
}

function submitLabel(operation: MeetingOperation): string {
  if (meetingBusy(operation)) return "Agendando…";
  if (operation.stage === "invite-error") return "Tentar convite coletivo novamente";
  return "Agendar reunião";
}

const temporalFields: readonly (keyof MeetingDraft)[] = [
  "date",
  "time",
  "timezone",
  "duration",
  "customDuration",
  "occurrence",
];

function changedFieldErrors(
  errors: MeetingErrors,
  field: keyof MeetingDraft,
  draft: MeetingDraft,
): MeetingErrors {
  const errorField = field === "customDuration" ? "duration" : field;
  const next = { ...errors, [errorField]: undefined };
  if (!temporalFields.includes(field) || !temporalFields.some((key) => errors[key])) return next;
  const validation = validateMeetingTime(draft);
  for (const key of temporalFields) {
    if (errors[key]) next[key] = validation[key];
  }
  return next;
}
