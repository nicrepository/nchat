import { Blobatar } from "@blobatar/react";
import { useState } from "react";

import { safeAvatarUrl } from "./avatarUrl";
import { avatarSeed } from "./avatarSeed";
import { initialsFrom } from "./messageDisplay";
import PresenceDot from "./PresenceDot";
import type { PresenceState } from "./presence";
import "./UserAvatar.css";

export type UserAvatarSize = "sm" | "md" | "lg";

export interface UserAvatarProps {
  userId: string;
  workspaceId: string;
  displayName: string;
  avatarUrl?: string | null;
  presence?: PresenceState;
  size?: UserAvatarSize;
  imageClassName?: string;
  alt?: string;
  presenceRingColor?: string;
  /** Hover text for the presence dot when there is more than the state's word. */
  presenceTitle?: string;
}

/**
 * The shared image-or-Blobatar state machine for user identities.
 *
 * Blobatar v2 is the explicitly selected visual generation. It renders a
 * static data URI locally; no seed, identifier, or generated SVG is sent to or
 * persisted by NChat or a third party.
 */
export function UserAvatar({
  userId,
  workspaceId,
  displayName,
  avatarUrl,
  presence,
  size = "md",
  imageClassName,
  alt = "",
  presenceRingColor,
  presenceTitle,
}: UserAvatarProps) {
  const usableAvatarUrl = safeAvatarUrl(avatarUrl);
  const imageClasses = ["user-avatar__image", imageClassName].filter(Boolean).join(" ");
  const [failedUrl, setFailedUrl] = useState<string | null>(null);
  const [trackedUrl, setTrackedUrl] = useState(avatarUrl);

  if (avatarUrl !== trackedUrl) {
    setTrackedUrl(avatarUrl);
    setFailedUrl(null);
  }

  const content =
    usableAvatarUrl && failedUrl !== avatarUrl ? (
      <img
        className={imageClasses}
        src={usableAvatarUrl}
        alt={alt}
        referrerPolicy="no-referrer"
        onError={() => setFailedUrl(avatarUrl ?? null)}
      />
    ) : userId && workspaceId ? (
      <Blobatar
        name={avatarSeed(workspaceId, userId)}
        background="circle"
        className={imageClasses}
        alt={alt}
      />
    ) : (
      <>{displayName ? initialsFrom(displayName) : ""}</>
    );

  return (
    <>
      {content}
      {presence ? (
        <PresenceDot
          state={presence}
          size={size}
          ringColor={presenceRingColor}
          title={presenceTitle}
        />
      ) : null}
    </>
  );
}
