/**
 * Inline playback for an ordinary audio-file attachment (issue #670).
 *
 * Sibling of AttachmentVideo: draws a player only for a clean, playable audio
 * file, and nothing at all for anything else — including one still being
 * scanned, which gets an explanatory note instead so the row does not read as
 * broken. A voice message never reaches this component: MessageAttachments
 * routes those to the compact bubble instead, before this is rendered — see
 * isVoiceMessage / MessageAttachments.tsx.
 *
 * Unlike AttachmentVideo this does not fetch on render: the bytes are armed
 * only once the user presses Play, so a history full of audio files does not
 * pull every one of them into memory just because it scrolled into view.
 */

import { useState } from "react";

import AudioPlayer, { type AudioDownloadAction } from "./AudioPlayer";
import { canPlayAudioInline, isAudioAttachment } from "./attachmentAudioRules";
import { useAttachmentBlobUrl } from "./useAttachmentBlobUrl";
import { fetchAttachmentContent } from "./filesApi";
import type { ChannelAttachment } from "./chatTypes";

export default function AttachmentAudio({
  attachment,
  download,
}: {
  attachment: ChannelAttachment;
  /**
   * Passed straight through to the player, which decides where the control
   * sits. Only the voice bubble supplies one (issue #740): an ordinary audio
   * file's row already carries its own Baixar action.
   */
  download?: AudioDownloadAction;
}) {
  const [armed, setArmed] = useState(false);
  const eligible = canPlayAudioInline(attachment) && armed;
  const { url, failed } = useAttachmentBlobUrl(attachment.id, eligible, fetchAttachmentContent);

  if (!isAudioAttachment(attachment)) return null;
  if (attachment.status === "pending_scan") {
    return (
      <p className="chat-details__file-video-note" data-testid="chat-details-audio-pending">
        Em análise. A reprodução fica disponível após a verificação.
      </p>
    );
  }
  if (!canPlayAudioInline(attachment)) return null;

  return (
    <AudioPlayer
      label={`Áudio: ${attachment.filename}`}
      src={url}
      loading={armed && url === null && !failed}
      failed={failed}
      onRequestLoad={() => setArmed(true)}
      durationHint={attachment.durationMs ? attachment.durationMs / 1000 : undefined}
      download={download}
      testIdPrefix={`chat-audio-${attachment.id}`}
    />
  );
}
