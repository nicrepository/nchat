import type { ReactNode } from "react";
import { durationPresets, meetingInterval, startChoices } from "./meetingTime";
import type { MeetingDraft, MeetingErrors } from "./meetingTypes";

export function MeetingField({
  name,
  label,
  error,
  children,
}: Readonly<{
  name: keyof MeetingDraft;
  label: string;
  error?: string;
  children: ReactNode;
}>) {
  return (
    <div className="meeting-field">
      <label htmlFor={`meeting-${name}`}>{label}</label>
      {children}
      {error && (
        <p className="meeting-error" id={`meeting-${name}-error`}>
          {error}
        </p>
      )}
    </div>
  );
}
function fieldA11y(name: keyof MeetingDraft, errors: MeetingErrors) {
  return {
    id: `meeting-${name}`,
    name,
    "aria-invalid": Boolean(errors[name]),
    "aria-describedby": errors[name] ? `meeting-${name}-error` : undefined,
  };
}

export default function MeetingFields({
  draft,
  errors,
  change,
}: Readonly<{
  draft: MeetingDraft;
  errors: MeetingErrors;
  change: <K extends keyof MeetingDraft>(key: K, value: MeetingDraft[K]) => void;
}>) {
  const a11y = (key: keyof MeetingDraft) => fieldA11y(key, errors);
  let choices: ReturnType<typeof startChoices> = [];
  let endLabel = "";
  try {
    choices = startChoices(draft);
    endLabel = meetingInterval(draft).endLabel;
  } catch {
    /* Inline validation explains incomplete or invalid civil inputs. */
  }
  return (
    <>
      <MeetingField name="title" label="Título (obrigatório)" error={errors.title}>
        <input
          {...a11y("title")}
          required
          value={draft.title}
          onChange={(e) => change("title", e.target.value)}
        />
      </MeetingField>
      <div className="meeting-grid">
        <MeetingField name="date" label="Data (obrigatória)" error={errors.date}>
          <input
            {...a11y("date")}
            type="date"
            required
            min="0001-01-01"
            max="9999-12-31"
            value={draft.date}
            onChange={(e) => change("date", e.target.value)}
          />
        </MeetingField>
        <MeetingField name="time" label="Hora (obrigatória)" error={errors.time}>
          <input
            {...a11y("time")}
            type="time"
            required
            value={draft.time}
            onChange={(e) => change("time", e.target.value)}
          />
        </MeetingField>
      </div>
      <MeetingField name="timezone" label="Timezone IANA (obrigatória)" error={errors.timezone}>
        <input
          {...a11y("timezone")}
          required
          list="meeting-timezones"
          value={draft.timezone}
          onChange={(e) => change("timezone", e.target.value)}
        />
        <datalist id="meeting-timezones">
          {["America/Sao_Paulo", "America/New_York", "Europe/Lisbon", "UTC"].map((zone) => (
            <option key={zone} value={zone} />
          ))}
        </datalist>
      </MeetingField>
      {choices.length === 2 && (
        <MeetingField
          name="occurrence"
          label="Ocorrência do horário (obrigatória)"
          error={errors.occurrence}
        >
          <select
            {...a11y("occurrence")}
            value={draft.occurrence}
            onChange={(e) => change("occurrence", e.target.value as MeetingDraft["occurrence"])}
          >
            <option value="">Escolha uma ocorrência</option>
            <option value="earlier">Primeira ocorrência ({choices[0].offset})</option>
            <option value="later">Segunda ocorrência ({choices[1].offset})</option>
          </select>
        </MeetingField>
      )}
      <MeetingField name="duration" label="Duração" error={errors.duration}>
        <select
          {...fieldA11y("duration", draft.duration === "custom" ? {} : errors)}
          value={draft.duration}
          onChange={(e) => change("duration", e.target.value)}
        >
          {durationPresets.map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
        {draft.duration === "custom" && (
          <>
            <label htmlFor="meeting-customDuration">Duração personalizada em minutos</label>
            <input
              id="meeting-customDuration"
              name="duration"
              type="number"
              min="1"
              step="1"
              inputMode="numeric"
              value={draft.customDuration}
              aria-invalid={Boolean(errors.duration)}
              aria-describedby={errors.duration ? "meeting-duration-error" : undefined}
              onChange={(e) => change("customDuration", e.target.value)}
            />
          </>
        )}
      </MeetingField>
      {endLabel && (
        <p role="status" className="meeting-hint">
          Término: {endLabel} · {draft.timezone}
        </p>
      )}
      <MeetingField name="description" label="Descrição (opcional)" error={errors.description}>
        <textarea
          {...a11y("description")}
          rows={3}
          value={draft.description}
          onChange={(e) => change("description", e.target.value)}
        />
      </MeetingField>
      <MeetingField name="joinPolicy" label="Quem pode entrar" error={errors.joinPolicy}>
        <select
          {...a11y("joinPolicy")}
          value={draft.joinPolicy}
          onChange={(e) => change("joinPolicy", e.target.value as MeetingDraft["joinPolicy"])}
        >
          <option value="invited_only">Somente pessoas convidadas</option>
          <option value="conversation_members">Pessoas com acesso à conversa</option>
          <option value="workspace_members_with_link">
            Membros do workspace com o link (exceto guests)
          </option>
        </select>
        <p className="meeting-hint">A política de entrada não convida pessoas automaticamente.</p>
      </MeetingField>
    </>
  );
}
