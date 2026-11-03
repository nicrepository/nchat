import { Temporal } from "@js-temporal/polyfill";
import type {
  MeetingDraft,
  MeetingErrors,
  MeetingTarget,
  CreateMeetingPayload,
} from "./meetingTypes";

export function normalizeMeetingText(value: string): string {
  let start = 0;
  let end = value.length;
  while (start < end && /\p{White_Space}/u.test(value[start])) start++;
  while (end > start && /\p{White_Space}/u.test(value[end - 1])) end--;
  return value.slice(start, end);
}

export const durationPresets = [
  ["15", "15 min"],
  ["30", "30 min"],
  ["45", "45 min"],
  ["60", "1 h"],
  ["custom", "Personalizado"],
] as const;

export function initialMeetingDraft(
  target: MeetingTarget,
  now = Temporal.Now.zonedDateTimeISO(),
): MeetingDraft {
  const start = now.add({ minutes: 30 }).round({ smallestUnit: "minute", roundingMode: "ceil" });
  return {
    title: target.direct ? `Reunião com ${target.name}` : target.name,
    description: "",
    date: start.toPlainDate().toString(),
    time: start.toPlainTime().toString({ smallestUnit: "minute" }),
    timezone: now.timeZoneId,
    duration: "30",
    customDuration: "",
    occurrence: "",
    joinPolicy: "invited_only",
    participants: [],
    inviteAll: false,
  };
}

export function validTimezone(value: string): boolean {
  if (!value || value === "Local" || value.length > 64 || /^[+-]/.test(value)) return false;
  try {
    return new Temporal.ZonedDateTime(0n, value).timeZoneId === value;
  } catch {
    return false;
  }
}

export function startChoices(draft: MeetingDraft): Temporal.ZonedDateTime[] {
  const civil = Temporal.PlainDateTime.from(`${draft.date}T${draft.time}`, { overflow: "reject" });
  const earlier = civil.toZonedDateTime(normalizeMeetingText(draft.timezone), {
    disambiguation: "earlier",
  });
  const later = civil.toZonedDateTime(normalizeMeetingText(draft.timezone), {
    disambiguation: "later",
  });
  if (!earlier.toPlainDateTime().equals(civil) || !later.toPlainDateTime().equals(civil)) return [];
  return earlier.epochNanoseconds === later.epochNanoseconds ? [earlier] : [earlier, later];
}

function validDate(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value) || value.startsWith("0000")) return false;
  try {
    Temporal.PlainDate.from(value, { overflow: "reject" });
    return true;
  } catch {
    return false;
  }
}

function textErrors(draft: MeetingDraft): MeetingErrors {
  const errors: MeetingErrors = {};
  const title = normalizeMeetingText(draft.title);
  if (!title || [...title].length > 120 || /[\p{Cc}\uD800-\uDFFF]/u.test(title))
    errors.title = "Informe um título de 1 a 120 caracteres, sem caracteres de controle.";
  if (
    [...normalizeMeetingText(draft.description)].length > 5000 ||
    /[^\P{Cc}\n\r\t]|[\uD800-\uDFFF]/u.test(normalizeMeetingText(draft.description))
  )
    errors.description =
      "Use até 5000 caracteres, sem controles além de quebras de linha e tabulação.";
  if (
    !["invited_only", "conversation_members", "workspace_members_with_link"].includes(
      draft.joinPolicy,
    )
  )
    errors.joinPolicy = "Selecione uma política de entrada válida.";
  return errors;
}

export function meetingInterval(draft: MeetingDraft): {
  starts_at: string;
  ends_at: string;
  endLabel: string;
} {
  const choices = startChoices(draft);
  if (!choices.length) throw new RangeError("gap");
  if (choices.length === 2 && !draft.occurrence) throw new RangeError("overlap");
  const start = choices[draft.occurrence === "later" ? choices.length - 1 : 0];
  const minutes = Number(draft.duration === "custom" ? draft.customDuration : draft.duration);
  if (!Number.isSafeInteger(minutes) || minutes <= 0) throw new RangeError("duration");
  if (start.toInstant().toString() === "0001-01-01T00:00:00Z") throw new RangeError("zero");
  const end = start.add({ minutes });
  if (Math.max(end.year, end.withTimeZone("UTC").year) > 9999) throw new RangeError("range");
  return {
    starts_at: start.toInstant().toString(),
    ends_at: end.toInstant().toString(),
    endLabel: `${end.toPlainDate()} ${end.toPlainTime().toString({ smallestUnit: "minute" })} (${end.offset})`,
  };
}

export function validateMeeting(draft: MeetingDraft): MeetingErrors {
  return { ...textErrors(draft), ...validateMeetingTime(draft) };
}

export function validateMeetingTime(draft: MeetingDraft): MeetingErrors {
  const errors: MeetingErrors = {};
  if (!validDate(draft.date)) errors.date = "Informe uma data válida entre os anos 0001 e 9999.";
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(draft.time)) errors.time = "Informe um horário válido.";
  if (!validTimezone(normalizeMeetingText(draft.timezone)))
    errors.timezone = "Informe uma timezone IANA válida, como America/Sao_Paulo.";
  const minutes = Number(draft.duration === "custom" ? draft.customDuration : draft.duration);
  if (!Number.isSafeInteger(minutes) || minutes <= 0)
    errors.duration = "Informe uma duração positiva em minutos inteiros.";
  if (Object.keys(errors).length) return errors;
  return { ...errors, ...intervalErrors(draft) };
}

function intervalErrors(draft: MeetingDraft): MeetingErrors {
  const choices = startChoices(draft);
  if (!choices.length)
    return {
      time: "Este horário não existe nesta timezone devido à mudança de horário. Escolha outro horário.",
    };
  if (choices.length === 2 && !draft.occurrence)
    return { occurrence: "Este horário ocorre duas vezes. Escolha a ocorrência pelo offset." };
  try {
    meetingInterval(draft);
    return {};
  } catch (error) {
    if (error instanceof RangeError && error.message === "zero")
      return { date: "O instante inicial é o valor zero e não é aceito pelo servidor." };
    return { duration: "A duração ultrapassa o intervalo de datas suportado (ano 9999)." };
  }
}

export function meetingPayload(
  draft: MeetingDraft,
  target: MeetingTarget,
  organizerId: string,
): CreateMeetingPayload {
  const { starts_at, ends_at } = meetingInterval(draft);
  const ids = [...new Set(draft.participants.map((p) => p.user_id))]
    .filter((id) => id !== organizerId)
    .sort((first, second) => first.localeCompare(second, "en"));
  return {
    title: normalizeMeetingText(draft.title),
    description: normalizeMeetingText(draft.description),
    starts_at,
    ends_at,
    timezone: normalizeMeetingText(draft.timezone),
    join_policy: draft.joinPolicy,
    conversation: { kind: target.kind, id: target.id },
    participants: ids.map((user_id) => ({ user_id, role: "attendee" })),
  };
}
