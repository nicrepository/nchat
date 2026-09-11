/**
 * The call bars above the timeline (moved out of ChatMessageArea, issue #834).
 *
 * Mutually exclusive by construction: a 1:1 DM has no resource-call room
 * (resourceCallKind is null for one), so the resource bar is never set where
 * the direct bar is. Rendering both here is what makes that a single, checkable
 * statement instead of two conditions in the page's JSX.
 */

import ActiveDirectCallBar, {
  type ActiveDirectCallBarProps,
} from "../../calls/ActiveDirectCallBar";
import ActiveResourceCallBar, {
  type ActiveResourceCallBarProps,
} from "../../calls/ActiveResourceCallBar";

export default function ConversationCallBars({
  resourceCall,
  directCall,
}: {
  /**
   * #657: shown on discovery (available) or participation (participating-local
   * or participating-info). Null when there is nothing to announce.
   */
  resourceCall: ActiveResourceCallBarProps | null;
  /** #673: the direct 1:1 counterpart of the bar above. */
  directCall: ActiveDirectCallBarProps | null;
}) {
  return (
    <>
      {resourceCall && <ActiveResourceCallBar {...resourceCall} />}
      {directCall && <ActiveDirectCallBar {...directCall} />}
    </>
  );
}
