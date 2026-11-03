import { describe, expect, it } from "vitest";
import { Temporal } from "@js-temporal/polyfill";
import {
  initialMeetingDraft,
  meetingInterval,
  meetingPayload,
  startChoices,
  validTimezone,
  validateMeeting,
} from "./meetingTime";
import type { MeetingDraft, MeetingTarget } from "./meetingTypes";
const target: MeetingTarget = { kind: "channel", id: "channel", name: "Equipe", direct: false };
const draft = (patch: Partial<MeetingDraft> = {}): MeetingDraft => ({
  ...initialMeetingDraft(
    target,
    Temporal.ZonedDateTime.from("2026-10-30T10:00-03:00[America/Sao_Paulo]"),
  ),
  ...patch,
});

it("keeps timezone casing and absolute RFC3339 years compatible with the server", () => {
  expect(validTimezone("america/sao_paulo")).toBe(false);
  expect(validTimezone("utc")).toBe(false);
  expect(validTimezone("US/Eastern")).toBe(true);
  expect(validTimezone("A".repeat(65))).toBe(false);
  expect(
    validateMeeting(
      draft({ date: "9999-12-31", time: "20:00", timezone: "America/Sao_Paulo", duration: "60" }),
    ).duration,
  ).toBeTruthy();
});

describe("meeting civil time and payload", () => {
  it("defaults to 30 minutes and contextual titles", () => {
    expect(draft()).toMatchObject({
      title: "Equipe",
      duration: "30",
      joinPolicy: "invited_only",
      participants: [],
      time: "10:30",
    });
    expect(initialMeetingDraft({ ...target, direct: true, name: "Ana" }).title).toBe(
      "Reunião com Ana",
    );
  });
  it.each(["15", "30", "45", "60"])("converts preset %s to elapsed minutes", (duration) => {
    const interval = meetingInterval(draft({ duration }));
    expect(Date.parse(interval.ends_at) - Date.parse(interval.starts_at)).toBe(
      Number(duration) * 60000,
    );
    expect(interval.starts_at).toBe("2026-10-30T13:30:00Z");
  });
  it("keeps custom duration independent from presets and crosses midnight", () => {
    const input = draft({ time: "23:45", duration: "custom", customDuration: "90" });
    expect(meetingInterval(input)).toMatchObject({
      ends_at: "2026-10-31T04:15:00Z",
      endLabel: "2026-10-31 01:15 (-03:00)",
    });
    expect(meetingInterval({ ...input, duration: "15" }).ends_at).toBe("2026-10-31T03:00:00Z");
  });
  it.each(["0", "-1", "1.5", "", "NaN", "Infinity", "9007199254740992"])(
    "rejects invalid custom minutes %s",
    (customDuration) => {
      expect(validateMeeting(draft({ duration: "custom", customDuration })).duration).toBeTruthy();
      expect(() => meetingInterval(draft({ duration: "custom", customDuration }))).toThrow();
    },
  );
  it.each(["2026-02-29", "2026-04-31", "0000-01-01", "10000-01-01", "bad", ""])(
    "rejects invalid civil date %s",
    (date) => expect(validateMeeting(draft({ date })).date).toBeTruthy(),
  );
  it.each(["24:00", "12:60", "9:00", "", "23:59:60"])("rejects invalid civil time %s", (time) =>
    expect(validateMeeting(draft({ time })).time).toBeTruthy(),
  );
  it.each(["America/Sao_Paulo", "America/New_York", "Europe/Lisbon", "UTC"])(
    "accepts IANA timezone %s",
    (timezone) => expect(validTimezone(timezone)).toBe(true),
  );
  it.each(["", "Local", "+03:00", "-03:00", "Mars/Base"])("rejects timezone %s", (timezone) => {
    expect(validTimezone(timezone)).toBe(false);
    expect(validateMeeting(draft({ timezone })).timezone).toBeTruthy();
  });
  it("rejects nonexistent DST time without shifting the clock", () => {
    const input = draft({ timezone: "America/New_York", date: "2026-03-08", time: "02:30" });
    expect(startChoices(input)).toHaveLength(0);
    expect(validateMeeting(input).time).toContain("não existe");
    expect(() => meetingInterval(input)).toThrow("gap");
  });
  it("requires explicit DST overlap occurrence", () => {
    const input = draft({ timezone: "America/New_York", date: "2026-11-01", time: "01:30" });
    expect(startChoices(input)).toHaveLength(2);
    expect(validateMeeting(input).occurrence).toContain("duas vezes");
    expect(() => meetingInterval(input)).toThrow("overlap");
    expect(meetingInterval({ ...input, occurrence: "earlier" }).starts_at).toBe(
      "2026-11-01T05:30:00Z",
    );
    expect(meetingInterval({ ...input, occurrence: "later" }).starts_at).toBe(
      "2026-11-01T06:30:00Z",
    );
  });
  it("adds elapsed duration through DST changes", () => {
    const interval = meetingInterval(
      draft({ timezone: "America/New_York", date: "2026-03-08", time: "01:45" }),
    );
    expect(interval.ends_at).toBe("2026-03-08T07:15:00Z");
    expect(interval.endLabel).toContain("03:15 (-04:00)");
  });
  it("rejects the backend zero instant", () =>
    expect(
      validateMeeting(draft({ date: "0001-01-01", time: "00:00", timezone: "UTC" })).date,
    ).toContain("valor zero"));
  it("validates boundary dates, past times and overflow without arbitrary duration cap", () => {
    expect(validateMeeting(draft({ date: "0001-01-01", timezone: "UTC" }))).toEqual({});
    expect(
      validateMeeting(draft({ date: "9999-12-31", time: "23:50", duration: "30" })).duration,
    ).toBeTruthy();
    expect(validateMeeting(draft({ duration: "custom", customDuration: "100000" }))).toEqual({});
  });
  it.each(["", "   ", "a".repeat(121), "hello\u0000world"])("rejects invalid title", (title) =>
    expect(validateMeeting(draft({ title })).title).toBeTruthy(),
  );
  it("matches Go whitespace normalization and rejects unpaired UTF16 surrogates", () => {
    expect(meetingPayload(draft({ title: "\u0085 Planejar \u0085" }), target, "me").title).toBe(
      "Planejar",
    );
    expect(meetingPayload(draft({ title: "\uFEFFPlanejar\uFEFF" }), target, "me").title).toBe(
      "\uFEFFPlanejar\uFEFF",
    );
    expect(validateMeeting(draft({ title: "Bad\uD800" })).title).toBeTruthy();
    expect(validateMeeting(draft({ description: "Bad\uDFFF" })).description).toBeTruthy();
  });
  it("counts Unicode code points instead of UTF16 and accepts multiline description", () => {
    expect(
      validateMeeting(draft({ title: "😀".repeat(120), description: "Oi\n\tTudo bem?\r\n" })),
    ).toEqual({});
    expect(validateMeeting(draft({ title: "😀".repeat(121) })).title).toBeTruthy();
    expect(validateMeeting(draft({ description: "😀".repeat(5001) })).description).toBeTruthy();
    expect(validateMeeting(draft({ description: "Oi\u0001" })).description).toBeTruthy();
  });
  it.each(["invited_only", "conversation_members", "workspace_members_with_link"] as const)(
    "preserves valid join policy %s",
    (joinPolicy) => {
      expect(validateMeeting(draft({ joinPolicy }))).toEqual({});
      expect(meetingPayload(draft({ joinPolicy }), target, "me").join_policy).toBe(joinPolicy);
    },
  );
  it("rejects an unknown join policy", () =>
    expect(
      validateMeeting(draft({ joinPolicy: "public" as MeetingDraft["joinPolicy"] })).joinPolicy,
    ).toBeTruthy());
  it("sends only allowed fields, deduplicates IDs, excludes organizer and maps group to DM", () => {
    const payload = meetingPayload(
      draft({
        title: "  Planejamento  ",
        description: "  Pauta  ",
        participants: [
          { user_id: "me", display_name: "Eu" },
          { user_id: "z", display_name: "Z" },
          { user_id: "z", display_name: "Outra" },
        ],
      }),
      { ...target, kind: "dm" },
      "me",
    );
    expect(payload).toEqual({
      title: "Planejamento",
      description: "Pauta",
      starts_at: "2026-10-30T13:30:00Z",
      ends_at: "2026-10-30T14:00:00Z",
      timezone: "America/Sao_Paulo",
      join_policy: "invited_only",
      conversation: { kind: "dm", id: "channel" },
      participants: [{ user_id: "z", role: "attendee" }],
    });
  });
});
