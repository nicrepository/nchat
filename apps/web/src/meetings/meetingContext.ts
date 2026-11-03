import { createContext, useContext } from "react";
export type OpenMeetingScheduling = (
  kind: "channel" | "dm",
  id: string,
  trigger?: HTMLElement | null,
) => void;
export const MeetingSchedulingContext = createContext<OpenMeetingScheduling | undefined>(undefined);
export function useMeetingScheduling() {
  return useContext(MeetingSchedulingContext);
}
