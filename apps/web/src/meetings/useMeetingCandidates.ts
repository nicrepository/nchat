import { useEffect, useRef, useState } from "react";
import { meetingCandidates, candidateError } from "./meetingApi";
import type { Candidate } from "./meetingTypes";
import type { MeetingSession } from "./meetingSession";

interface CandidatePage {
  items: Candidate[];
  next_cursor?: string;
  status: "loading" | "ready" | "error";
  error?: string;
}
export function useMeetingCandidates(
  session: MeetingSession,
  organizerId: string,
  enabled: boolean,
) {
  const [request, setRequest] = useState({ session, organizerId, query: "", cursor: "", retry: 0 });
  const settled = useRef<typeof request | null>(null);
  const [page, setPage] = useState<CandidatePage>({ items: [], status: "loading" });
  const generation = useRef(0);
  if (request.session !== session || request.organizerId !== organizerId) {
    setRequest({ session, organizerId, query: "", cursor: "", retry: 0 });
    setPage({ items: [], status: "loading" });
  }
  function search(value: string) {
    generation.current++;
    setPage({ items: [], status: "loading" });
    setRequest({ session, organizerId, query: value, cursor: "", retry: 0 });
  }
  useEffect(() => {
    if (!enabled || settled.current === request) return;
    const controller = new AbortController();
    const version = ++generation.current;
    const timer = window.setTimeout(
      () => {
        void meetingCandidates(session.target, request.query, request.cursor, controller.signal)
          .then((result) => {
            if (
              controller.signal.aborted ||
              generation.current !== version ||
              !session.identityConfirmed()
            )
              return;
            settled.current = request;
            const items = result.items.filter((person) => person.user_id !== organizerId);
            setPage({ ...result, items, status: "ready" });
            const current = session.getSnapshot();
            if (!current.seeded && !request.query && !request.cursor) {
              const participants = items.slice(0, 1);
              session.update({
                seeded: true,
                draft: { ...current.draft, participants },
                baseline: { ...current.baseline, participants },
              });
            }
          })
          .catch((error) => {
            if (
              controller.signal.aborted ||
              generation.current !== version ||
              !session.identityConfirmed()
            )
              return;
            settled.current = request;
            setPage({ items: [], status: "error", error: candidateError(error) });
          });
      },
      request.query ? 300 : 0,
    );
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [request, session, organizerId, enabled]);
  function next() {
    if (!page.next_cursor || page.status !== "ready") return;
    setRequest({ ...request, cursor: page.next_cursor });
    setPage({ items: [], status: "loading" });
  }
  function retry() {
    setRequest({ ...request, retry: request.retry + 1 });
    setPage({ items: [], status: "loading" });
  }
  return { query: request.query, search, page, next, retry };
}
