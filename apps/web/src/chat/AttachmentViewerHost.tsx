/**
 * Where attachment viewers live (issue #675).
 *
 * A lightbox or a document viewer opened from a message used to be rendered by
 * that message's own attachment list. That was fine while the whole history
 * stayed mounted; with the timeline virtualized it is not — the message can be
 * unmounted while the viewer is still open, and the viewer would go with it.
 *
 * So the viewers are hosted here instead, above the list, and a message only
 * ever *asks* for one. Everything the viewer needs travels in the request —
 * attachment metadata, the bytes already on screen, the button to return focus
 * to — so nothing it shows depends on the item that opened it still existing.
 *
 * The host collapses when there is already one above it: MessageAttachments
 * wraps itself in a host so it keeps working when rendered on its own, and that
 * inner host steps aside as soon as the timeline provides the real one. There
 * is therefore exactly one viewer on screen at a time, wherever the host sits.
 *
 * Authorization is untouched: a viewer fetches through the same authenticated
 * client the card did, and file-service re-checks membership and the scan gate
 * on every request. Hosting it higher up changes what stays mounted, never what
 * may be read.
 */

import { useContext, useMemo, useState, type ReactNode } from "react";

import AttachmentLightbox from "./AttachmentLightbox";
import DocumentPreviewViewer from "./DocumentPreviewViewer";
import { AttachmentViewerContext, type AttachmentViewerApi } from "./attachmentViewer";
import type { ChannelAttachment } from "./chatTypes";

interface OpenLightbox {
  attachment: ChannelAttachment;
  trigger: HTMLButtonElement;
  blob: Blob;
  isOriginal: boolean;
}

interface OpenDocument {
  attachment: ChannelAttachment;
  trigger: HTMLButtonElement;
}

/**
 * Returns focus where it came from, or to a predictable place when that control
 * no longer exists.
 *
 * The trigger is a real element captured at open time; virtualization can have
 * unmounted it since, and focusing a detached node silently sends focus to
 * `<body>` — which is precisely the "foco desaparece" the issue forbids.
 */
function returnFocus(trigger: HTMLElement, fallback?: () => void): void {
  if (trigger.isConnected) {
    trigger.focus();
    return;
  }
  fallback?.();
}

export default function AttachmentViewerHost({
  children,
  onFocusFallback,
}: {
  children: ReactNode;
  /** Where focus goes when the control that opened a viewer is gone. */
  onFocusFallback?: () => void;
}) {
  const ambient = useContext(AttachmentViewerContext);
  const [lightbox, setLightbox] = useState<OpenLightbox | null>(null);
  const [documentViewer, setDocumentViewer] = useState<OpenDocument | null>(null);

  const api = useMemo<AttachmentViewerApi>(
    () => ({
      openImage: (attachment, payload) =>
        setLightbox({
          attachment,
          trigger: payload.trigger,
          blob: payload.blob,
          isOriginal: payload.isOriginal,
        }),
      openDocument: (attachment, trigger) => setDocumentViewer({ attachment, trigger }),
    }),
    [],
  );

  // An outer host already owns the viewers; this one is only in the way.
  if (ambient) return <>{children}</>;

  return (
    <AttachmentViewerContext.Provider value={api}>
      {children}
      {lightbox && (
        <AttachmentLightbox
          attachment={lightbox.attachment}
          inlineBlob={lightbox.blob}
          inlineIsOriginal={lightbox.isOriginal}
          onClose={() => {
            returnFocus(lightbox.trigger, onFocusFallback);
            setLightbox(null);
          }}
        />
      )}
      {documentViewer && (
        <DocumentPreviewViewer
          attachment={documentViewer.attachment}
          onClose={() => {
            returnFocus(documentViewer.trigger, onFocusFallback);
            setDocumentViewer(null);
          }}
        />
      )}
    </AttachmentViewerContext.Provider>
  );
}
