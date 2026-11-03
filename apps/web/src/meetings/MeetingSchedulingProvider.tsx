import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useLocation } from "react-router";
import { restoreOpenerFocus } from "../chat/restoreOpenerFocus";
import { useLatestRef } from "../chat/messages/useLatestRef";
import { onAuthChange, getSessionBoundaryGeneration } from "../lib/authSession";
import type { Channel, DMConversation } from "../chat/chatTypes";
import { createMeetingSession, type MeetingSession } from "./meetingSession";
import { MeetingSchedulingContext, type OpenMeetingScheduling } from "./meetingContext";
import MeetingSchedulingDialog from "./MeetingSchedulingDialog";

interface ActiveMeeting {
  session: MeetingSession;
  pathname: string;
  owner: string;
}

export default function MeetingSchedulingProvider({
  userId,
  workspaceId,
  channels,
  dms,
  children,
  identityPending = () => false,
  confirmingIdentity = false,
  restoreFocus = restoreMeetingOpener,
}: Readonly<{
  restoreFocus?: (opener: HTMLElement | null) => void;
  identityPending?: () => boolean;
  confirmingIdentity?: boolean;
  userId: string;
  workspaceId: string;
  channels: Channel[];
  dms: DMConversation[];
  children: ReactNode;
}>) {
  const { pathname } = useLocation();
  const sessions = useRef(new Map<string, MeetingSession>());
  const owner = `${userId}:${workspaceId}`;
  const currentOwner = useLatestRef(owner);
  const [active, setActive] = useState<ActiveMeeting | null>(null);
  const previousActive = useRef(active);
  useEffect(() => {
    if (active) {
      previousActive.current = active;
      return;
    }
    const previous = previousActive.current;
    previousActive.current = null;
    if (!previous) return;
    if (!isCurrentMeetingContext(previous, owner, pathname)) return;
    const target = previous.session.target;
    if (findConversation(target.kind, target.id, channels, dms)) return;
    restoreFocus(null);
  }, [active, owner, pathname, channels, dms, restoreFocus]);
  const opener = useRef<HTMLElement | null>(null);
  const [lastPathname, setLastPathname] = useState(pathname);
  const targetAvailable =
    !active ||
    Boolean(findConversation(active.session.target.kind, active.session.target.id, channels, dms));
  if (!targetAvailable) setActive(null);
  if (lastPathname !== pathname) {
    setLastPathname(pathname);
    setActive(null);
  }
  useEffect(
    () =>
      onAuthChange((change) => {
        if (change === "refresh") return;
        sessions.current.clear();
        setActive(null);
      }),
    [],
  );
  useEffect(() => {
    const current = sessions.current;
    return () => current.clear();
  }, [owner]);
  const open: OpenMeetingScheduling = useCallback(
    (kind, id, trigger) => {
      if (!userId || !workspaceId || identityPending()) return;
      const conversation = findConversation(kind, id, channels, dms);
      if (!conversation) return;
      const key = `${kind}:${id}`;
      let session = sessions.current.get(key);
      if (!session) {
        const direct = kind === "dm" && (conversation as DMConversation).type === "1:1";
        const boundary = getSessionBoundaryGeneration();
        session = createMeetingSession(
          { kind, id, name: conversation.name, direct },
          userId,
          () =>
            !identityPending() &&
            currentOwner.current === owner &&
            getSessionBoundaryGeneration() === boundary,
        );
        sessions.current.set(key, session);
      }
      opener.current =
        trigger ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
      setActive({ session, pathname, owner });
    },
    [userId, workspaceId, channels, dms, owner, pathname, identityPending, currentOwner],
  );
  function close(discard: boolean) {
    if (discard && active)
      sessions.current.delete(`${active.session.target.kind}:${active.session.target.id}`);
    setActive(null);
    if (isCurrentMeetingContext(active, owner, pathname)) restoreFocus(opener.current);
  }
  const visible = isCurrentMeetingContext(active, owner, pathname) && !confirmingIdentity;
  return (
    <MeetingSchedulingContext.Provider value={open}>
      {children}
      {visible && (
        <MeetingSchedulingDialog
          key={`${active.session.target.kind}:${active.session.target.id}`}
          session={active.session}
          organizerId={userId}
          onClose={close}
        />
      )}
    </MeetingSchedulingContext.Provider>
  );
}

function findConversation(
  kind: "channel" | "dm",
  id: string,
  channels: Channel[],
  dms: DMConversation[],
) {
  return (kind === "channel" ? channels : dms).find((item) => item.id === id);
}

function restoreMeetingOpener(opener: HTMLElement | null) {
  restoreOpenerFocus(opener, null);
}

function isCurrentMeetingContext(
  active: ActiveMeeting | null,
  owner: string,
  pathname: string,
): active is ActiveMeeting {
  return active?.owner === owner && active.pathname === pathname;
}
