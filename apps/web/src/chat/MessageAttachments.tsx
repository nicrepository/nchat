/**
 * Attachments rendered inside a message (RF-32).
 *
 * The three scan states are the whole component, and each one is drawn from the
 * *server's* status rather than from anything this file decides:
 *
 *   pending_scan  the file exists and has not been ruled on. Name and size, no
 *                 preview, no player, no download — an unapproved file is not
 *                 offered, and the request would be refused anyway;
 *   clean         the approved case. Thumbnail, inline video and download, all
 *                 through the same authenticated components the details panel
 *                 uses, so the eligibility rules are written once;
 *   rejected      blocked. Said plainly, with no action at all.
 *
 * Nothing here is a control. AttachmentThumbnail, AttachmentVideo and the
 * download below all fetch through the authenticated client, and file-service
 * re-checks visibility and scan state on every request. This component decides
 * what to *offer*, never what may be obtained.
 *
 * No URL is ever built from a filename, and no address outlives the click that
 * created it: the download's object URL is revoked in the same task.
 *
 * Images and GIFs (issue #491) are the one exception to "a row per
 * attachment": AttachmentImagePreview draws a large preview above the same
 * name/size/status/Baixar footer every other type shows in its row, and a
 * click on it asks AttachmentViewerHost to open AttachmentLightbox. The viewer
 * is hosted above the timeline rather than here (issue #675) so it survives the
 * virtualization unmounting this message underneath it; this component only
 * ever asks, and the host owns the one viewer that can be open at a time and
 * the focus return that goes with it.
 *
 * Each attachment is also its own lazy-hydration unit (issue #675): far from
 * the viewport it is a shell with no request behind it, and the gate it
 * provides is what every renderer below reads before fetching anything.
 */

import { useState, type Ref } from "react";

import AttachmentAudio from "./AttachmentAudio";
import AttachmentDocumentPreview from "./AttachmentDocumentPreview";
import AttachmentImagePreview, { type AttachmentImageOpenPayload } from "./AttachmentImagePreview";
import AttachmentThumbnail from "./AttachmentThumbnail";
import AttachmentVideo from "./AttachmentVideo";
import AttachmentViewerHost from "./AttachmentViewerHost";
import { useAttachmentViewer } from "./attachmentViewer";
import { AttachmentHydrationContext, useLazyAttachment } from "./lazyAttachment";
import { isImageAttachment } from "./attachmentImageRules";
import { isVoiceMessage } from "./attachmentAudioRules";
import {
  attachmentDownloadFilename,
  saveAttachmentToDisk,
  voiceMessageFilename,
} from "./attachmentDownload";
import { formatFileSize } from "./conversationDetailsDisplay";
import { isPreviewAvailable, type ChannelAttachment } from "./chatTypes";

/** Same mapping the details panel uses, kept local so neither owns the other. */
function fileIconFor(contentType: string): string {
  if (contentType.startsWith("image/")) return "image";
  if (contentType.startsWith("video/")) return "movie";
  if (contentType.startsWith("audio/")) return "graphic_eq";
  if (contentType === "application/pdf") return "picture_as_pdf";
  if (contentType.startsWith("text/")) return "description";
  return "draft";
}

/**
 * Whether this attachment might carry a document preview (PDF today; CSV/
 * XLSX as of task #494's sheet phase).
 *
 * `contentType` here is file-service's *detected* type — net/http.
 * DetectContentType's own sniff, never the filename or what the browser
 * declared — and that detector has no signature for CSV or for any OOXML
 * subtype. It can only ever report `text/plain` for delimited text and
 * `application/zip` for any zip-shaped file (XLSX included, indistinguishable
 * from DOCX/PPTX/ODT/ODP or an arbitrary .zip at this layer). Matching
 * `text/csv`, `officedocument`, `msword`, `ms-excel`, `ms-powerpoint` or
 * `opendocument` — what a real upload's contentType can never be — silently
 * routed every CSV/XLSX attachment through the generic thumbnail branch
 * instead of the document one. See file-service's
 * domain.previewableMIMEs for the server-side half of this same fact.
 *
 * A `text/plain` or `application/zip` attachment is not guaranteed to have a
 * usable preview — the server may still answer "unsupported" for a DOCX, a
 * plain log file, or a generic zip — but it is always routed here so the
 * Visualizar action becomes reachable the moment previewStatus says ready.
 */
function isDocumentAttachment(attachment: ChannelAttachment): boolean {
  // The detected type can carry a parameter (e.g. "text/plain;
  // charset=utf-8" is what a real CSV upload's contentType looks like) —
  // stripped here the same way file-service's own NormalizeDetectedMIME
  // does server-side, so the two never disagree about what a bare type is.
  const type = attachment.contentType.split(";")[0].trim().toLowerCase();
  return type === "application/pdf" || type === "text/plain" || type === "application/zip";
}

/**
 * Downloads one approved attachment.
 *
 * The bytes come through the authenticated client, exactly like the preview and
 * the video, because the content route needs an Authorization header that an
 * anchor cannot send. The object URL exists only long enough for the browser to
 * take the blob, and is revoked immediately after: nothing here produces an
 * address that can be shared, bookmarked or replayed.
 *
 * Rendered only for a clean attachment. A failure says so and changes nothing
 * else — the row keeps its name, size and status.
 */
function AttachmentDownloadButton({ attachment }: { attachment: ChannelAttachment }) {
  const [state, setState] = useState<"idle" | "loading" | "failed">("idle");

  const download = async () => {
    if (state === "loading") return;
    setState("loading");
    try {
      await saveAttachmentToDisk(attachment.id, attachmentDownloadFilename(attachment));
      setState("idle");
    } catch {
      // No server text is surfaced: it may carry detail that does not belong
      // in the UI, and every failure means the same thing here.
      setState("failed");
    }
  };

  return (
    <>
      <button
        type="button"
        className="chat-msg-area__attachment-action"
        aria-label={`Baixar ${attachment.filename}`}
        disabled={state === "loading"}
        data-testid={`chat-message-attachment-download-${attachment.id}`}
        onClick={() => void download()}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          download
        </span>
        {state === "loading" ? "Baixando…" : "Baixar"}
      </button>
      {state === "failed" && (
        <span className="chat-msg-area__attachment-note" role="alert">
          Não foi possível baixar o arquivo.
        </span>
      )}
    </>
  );
}

interface MessageAttachmentProps {
  attachment: ChannelAttachment;
  /** The owning message's ISO timestamp — see VoiceMessageAttachment. */
  sentAt: string;
  onOpenImage: (attachment: ChannelAttachment, payload: AttachmentImageOpenPayload) => void;
  onOpenDocument: (attachment: ChannelAttachment, trigger: HTMLButtonElement) => void;
}

interface AttachmentBodyProps extends MessageAttachmentProps {
  /** The lazy-hydration observer's target — see MessageAttachment below. */
  rowRef: Ref<HTMLLIElement>;
}

/**
 * A voice message's compact presentation (issue #670): a player and its scan
 * status, and deliberately nothing else — no filename, so a recording never
 * shows as "recording-1699999999.webm" the way an ordinary attachment shows
 * its name. AttachmentAudio already draws nothing for a rejected recording
 * and a note for one still being scanned, so the status line below only adds
 * the words those two silent states are missing; a clean, playable one needs
 * no second confirmation once the player itself is on screen.
 *
 * The one action it offers is the download (issue #740), drawn inside the
 * player between the time and the speed. It goes through the same
 * authenticated content route every other attachment's Baixar uses — a voice
 * message is an ordinary attachment as far as file-service is concerned, so
 * the conversation check and the malware-scan gate that decide it are the
 * existing ones, unchanged and re-evaluated on the request the click makes.
 */
function VoiceMessageAttachment({
  attachment,
  sentAt,
  rowRef,
}: {
  attachment: ChannelAttachment;
  rowRef: Ref<HTMLLIElement>;
  /**
   * When the message was sent. It is what dates the saved file: an attachment
   * of a message has no createdAt of its own — chat-service dates it by the
   * message — so the name would otherwise be the same for every recording.
   */
  sentAt: string;
}) {
  // Built from ids and metadata the message already carries, so constructing
  // it costs no request: a timeline of recordings still fetches nothing until
  // someone presses a button.
  const download = {
    label: "Baixar mensagem de voz",
    title: "Baixar áudio",
    start: () => saveAttachmentToDisk(attachment.id, voiceMessageFilename(sentAt)),
  };

  return (
    <li
      ref={rowRef}
      className="chat-msg-area__attachment chat-msg-area__attachment--voice"
      data-testid={`chat-message-attachment-${attachment.id}`}
    >
      <span className="chat-msg-area__attachment-icon" aria-hidden="true">
        <span className="material-symbols-outlined">mic</span>
      </span>
      <div className="chat-msg-area__voice-body">
        <AttachmentAudio attachment={attachment} download={download} />
        {attachment.status !== "clean" && (
          <span
            className={`chat-msg-area__attachment-status chat-msg-area__attachment-status--${attachment.status}`}
            data-testid={`chat-message-attachment-status-${attachment.id}`}
          >
            {attachment.status === "pending_scan" && "Verificando mensagem de voz…"}
            {attachment.status === "rejected" && "Bloqueado pela verificação de segurança"}
          </span>
        )}
      </div>
    </li>
  );
}

/**
 * One attachment, and the lazy-hydration gate that decides when it may fetch
 * anything (issue #675).
 *
 * The gate is provided here rather than passed down: every renderer below —
 * image preview, document card, thumbnail, video poster — reads it from context
 * through its own hook, so a new attachment type inherits the policy without
 * threading a prop through it.
 */
function MessageAttachment(props: MessageAttachmentProps) {
  const { ref, gate } = useLazyAttachment();
  return (
    <AttachmentHydrationContext.Provider value={gate}>
      <AttachmentBody {...props} rowRef={ref} />
    </AttachmentHydrationContext.Provider>
  );
}

function AttachmentBody({
  attachment,
  sentAt,
  onOpenImage,
  onOpenDocument,
  rowRef,
}: AttachmentBodyProps) {
  if (isVoiceMessage(attachment)) {
    return <VoiceMessageAttachment attachment={attachment} sentAt={sentAt} rowRef={rowRef} />;
  }
  const icon = (
    <span className="chat-msg-area__attachment-icon" aria-hidden="true">
      <span className="material-symbols-outlined">{fileIconFor(attachment.contentType)}</span>
    </span>
  );
  const meta = (
    <span className="chat-msg-area__attachment-text">
      {/* A filename is text. It is never a URL and never markup. */}
      <span className="chat-msg-area__attachment-name">{attachment.filename}</span>
      <span className="chat-msg-area__attachment-meta">
        {formatFileSize(attachment.size)}
        <span
          className={`chat-msg-area__attachment-status chat-msg-area__attachment-status--${attachment.status}`}
          data-testid={`chat-message-attachment-status-${attachment.id}`}
        >
          {attachment.status === "pending_scan" && "Verificando arquivo…"}
          {attachment.status === "clean" && "Verificado"}
          {attachment.status === "rejected" && "Bloqueado pela verificação de segurança"}
        </span>
      </span>
    </span>
  );
  const download = attachment.status === "clean" && (
    <AttachmentDownloadButton attachment={attachment} />
  );

  // Image and GIF attachments (RF-32/#491) get a large preview above the same
  // name/size/status/Baixar footer every other type shows beside its
  // thumbnail — everything else keeps the row layout untouched, PDF's own
  // server preview included.
  if (isImageAttachment(attachment)) {
    return (
      <li
        ref={rowRef}
        className="chat-msg-area__attachment"
        data-testid={`chat-message-attachment-${attachment.id}`}
      >
        <AttachmentImagePreview
          attachment={attachment}
          fallback={icon}
          onOpen={(payload) => onOpenImage(attachment, payload)}
        />
        <div className="chat-msg-area__attachment-row">
          {meta}
          {download}
        </div>
      </li>
    );
  }

  // Documents (PDF today; other office formats once a later phase renders
  // them) get the large WhatsApp-style card: a big first-page preview above
  // the row, an explicit Visualizar action beside Baixar. A file still being
  // scanned or rejected keeps the plain icon row below unchanged — the same
  // split AttachmentImagePreview draws for raster types, applied to
  // documents instead of replacing that branch.
  if (isDocumentAttachment(attachment)) {
    return (
      <li
        ref={rowRef}
        className="chat-msg-area__attachment"
        data-testid={`chat-message-attachment-${attachment.id}`}
      >
        {attachment.status === "clean" && (
          <AttachmentDocumentPreview
            attachment={attachment}
            onOpen={(trigger) => onOpenDocument(attachment, trigger)}
          />
        )}
        <div className="chat-msg-area__attachment-row">
          {icon}
          {meta}
        </div>
        {attachment.status === "clean" && (
          <div className="chat-msg-area__attachment-actions">
            {isPreviewAvailable(attachment.previewStatus) && (
              <button
                type="button"
                className="chat-msg-area__attachment-action"
                aria-label={`Visualizar ${attachment.filename}`}
                onClick={(event) => onOpenDocument(attachment, event.currentTarget)}
              >
                <span className="material-symbols-outlined" aria-hidden="true">
                  visibility
                </span>
                Visualizar
              </button>
            )}
            <AttachmentDownloadButton attachment={attachment} />
          </div>
        )}
      </li>
    );
  }

  return (
    <li
      ref={rowRef}
      className="chat-msg-area__attachment"
      data-testid={`chat-message-attachment-${attachment.id}`}
    >
      <div className="chat-msg-area__attachment-row">
        <AttachmentThumbnail attachment={attachment} fallback={icon} />
        {meta}
        {download}
      </div>
      {/* Draws a player only for a clean, playable video, and nothing at all
          otherwise — including for a file still being scanned. */}
      <AttachmentVideo attachment={attachment} />
      {/* Same contract for an ordinary audio file (issue #670) — never for a
          voice message, which VoiceMessageAttachment already handled above. */}
      <AttachmentAudio attachment={attachment} />
    </li>
  );
}

export default function MessageAttachments(props: {
  attachments: ChannelAttachment[] | undefined;
  /** The owning message's ISO timestamp. See VoiceMessageAttachment. */
  sentAt: string;
}) {
  // The host steps aside when the timeline already provides one above the list,
  // which is what keeps an open viewer alive after virtualization unmounts this
  // message (issue #675). Rendered on its own, this is still self-sufficient.
  return (
    <AttachmentViewerHost>
      <MessageAttachmentList {...props} />
    </AttachmentViewerHost>
  );
}

function MessageAttachmentList({
  attachments,
  sentAt,
}: {
  attachments: ChannelAttachment[] | undefined;
  sentAt: string;
}) {
  const viewer = useAttachmentViewer();
  const [expandedRuns, setExpandedRuns] = useState<Set<number>>(() => new Set());

  if (!attachments || attachments.length === 0) return null;

  const segments: Array<
    | { kind: "images"; attachments: ChannelAttachment[] }
    | { kind: "file"; attachment: ChannelAttachment }
  > = [];
  for (const attachment of attachments) {
    if (isImageAttachment(attachment)) {
      const last = segments.at(-1);
      if (last?.kind === "images") last.attachments.push(attachment);
      else segments.push({ kind: "images", attachments: [attachment] });
    } else {
      segments.push({ kind: "file", attachment });
    }
  }

  const openImage = (openedAttachment: ChannelAttachment, payload: AttachmentImageOpenPayload) =>
    viewer.openImage(openedAttachment, payload);
  const openDocument = (openedAttachment: ChannelAttachment, trigger: HTMLButtonElement) =>
    viewer.openDocument(openedAttachment, trigger);

  return (
    <>
      <ul className="chat-msg-area__attachments" aria-label="Anexos da mensagem">
        {segments.map((segment, index) => {
          if (segment.kind === "file") {
            return (
              <MessageAttachment
                key={segment.attachment.id}
                attachment={segment.attachment}
                sentAt={sentAt}
                onOpenImage={openImage}
                onOpenDocument={openDocument}
              />
            );
          }
          const expanded = expandedRuns.has(index);
          const visible = expanded ? segment.attachments : segment.attachments.slice(0, 4);
          const hidden = segment.attachments.length - visible.length;
          return (
            <li key={`images-${segment.attachments[0].id}`} className="chat-msg-area__image-run">
              <ul
                className={`chat-msg-area__image-grid chat-msg-area__image-grid--${Math.min(segment.attachments.length, 4)}`}
                data-testid="chat-message-image-grid"
                data-count={segment.attachments.length}
                aria-label={`${segment.attachments.length} imagens anexadas`}
              >
                {visible.map((attachment) => (
                  <MessageAttachment
                    key={attachment.id}
                    attachment={attachment}
                    sentAt={sentAt}
                    onOpenImage={openImage}
                    onOpenDocument={openDocument}
                  />
                ))}
              </ul>
              {hidden > 0 && (
                <button
                  type="button"
                  className="chat-msg-area__image-grid-more"
                  aria-label={`Mostrar mais ${hidden} ${hidden === 1 ? "imagem" : "imagens"}`}
                  onClick={() =>
                    setExpandedRuns((current) => {
                      const next = new Set(current);
                      next.add(index);
                      return next;
                    })
                  }
                >
                  +{hidden}
                </button>
              )}
            </li>
          );
        })}
      </ul>
    </>
  );
}
