/**
 * The two message dialogs (moved out of ChatMessageArea, issue #834). Both
 * render through a portal, so their position in the tree costs the
 * conversation column no layout.
 */

import type { Channel, DMConversation, Message } from "../../chatTypes";
import ForwardMessageDialog, { type ForwardSourceContext } from "../../ForwardMessageDialog";
import ReferenceDestinationDialog from "./ReferenceDestinationDialog";

export default function ConversationDialogs({
  kind,
  targetId,
  channels,
  dms,
  referenceSource,
  forwardSource,
  onCloseReference,
  onSelectReferenceDestination,
  onCloseForward,
}: {
  kind: "channel" | "dm";
  targetId: string;
  channels: Channel[];
  dms: DMConversation[];
  referenceSource: Message | null;
  forwardSource: ForwardSourceContext | null;
  onCloseReference: () => void;
  onSelectReferenceDestination: (target: { kind: "channel" | "dm"; id: string }) => void;
  onCloseForward: () => void;
}) {
  return (
    <>
      {referenceSource && (
        <ReferenceDestinationDialog
          current={{ kind, id: targetId }}
          channels={channels}
          dms={dms}
          onClose={onCloseReference}
          onSelect={onSelectReferenceDestination}
        />
      )}
      {forwardSource && kind === "channel" && (
        <ForwardMessageDialog
          source={forwardSource}
          channels={channels}
          onClose={onCloseForward}
          onSuccess={onCloseForward}
        />
      )}
    </>
  );
}
