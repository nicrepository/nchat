/**
 * How many messages this reader has not read (#880 item 11).
 *
 * The number on the floating control, and the only thing that decides whether
 * the control offers the unread boundary at all — so it has to mean *unread*,
 * not "messages below the fold". A reader who scrolls up three hundred
 * messages into history has read all three hundred of them, and a badge
 * counting what is underneath them would say 300 where the truth is 0.
 *
 * Two sources, both of them the read cursor's rather than the viewport's
 * (#492/#687): the unread count the conversation was opened with, and the
 * messages that have arrived behind the reader since. It goes back to zero
 * when the tail is confirmed, which is the same event that sends the read
 * receipt — so the badge and the server's idea of "read" change together
 * instead of drifting apart.
 */

import { useCallback, useState } from "react";

export interface UnreadCountState {
  /** Real unread messages, as the read cursor would count them. */
  count: number;
  /** A message arrived while the reader was away from the tail. */
  countArrival: () => void;
  /** The tail was confirmed: the read cursor is catching up. */
  clear: () => void;
}

export function useUnreadCount(unreadCountAtOpen: number): UnreadCountState {
  // Frozen on the first render of this conversation's timeline, which is what
  // "as of opening it" means: the sidebar's own count keeps moving (a realtime
  // arrival grows it too), and adding a live value to the arrivals counted here
  // would count each of them twice. The timeline unmounts on every conversation
  // switch, so the next conversation freezes its own.
  const [atOpen] = useState(unreadCountAtOpen);
  const [arrived, setArrived] = useState(0);
  const [cleared, setCleared] = useState(false);

  const countArrival = useCallback(() => setArrived((count) => count + 1), []);
  const clear = useCallback(() => {
    setCleared(true);
    setArrived(0);
  }, []);

  return { count: (cleared ? 0 : atOpen) + arrived, countArrival, clear };
}
