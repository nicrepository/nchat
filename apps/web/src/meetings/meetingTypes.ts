export interface MeetingTarget {
  kind: "channel" | "dm";
  id: string;
  name: string;
  direct: boolean;
}
export type JoinPolicy = "invited_only" | "conversation_members" | "workspace_members_with_link";
export interface Candidate {
  user_id: string;
  display_name: string;
}
export interface MeetingDraft {
  title: string;
  description: string;
  date: string;
  time: string;
  timezone: string;
  duration: string;
  customDuration: string;
  occurrence: "" | "earlier" | "later";
  joinPolicy: JoinPolicy;
  participants: Candidate[];
  inviteAll: boolean;
}
export interface CreateMeetingPayload {
  title: string;
  description: string;
  starts_at: string;
  ends_at: string;
  timezone: string;
  join_policy: JoinPolicy;
  conversation: { kind: "channel" | "dm"; id: string };
  participants: { user_id: string; role: "attendee" }[];
}
export interface MeetingResult {
  id: string;
  status: "scheduled" | "in_progress" | "ended" | "cancelled";
  version: number;
  starts_at: string;
  ends_at: string;
  timezone: string;
}
export type MeetingErrors = Partial<Record<keyof MeetingDraft, string>>;
export type MeetingOperation =
  | { stage: "editing" }
  | { stage: "creating" }
  | { stage: "create-error"; message: string; uncertain: boolean }
  | { stage: "inviting"; meeting: MeetingResult; replayed: boolean }
  | { stage: "invite-error"; meeting: MeetingResult; replayed: boolean; message: string }
  | { stage: "complete"; meeting: MeetingResult; replayed: boolean; added?: number };
