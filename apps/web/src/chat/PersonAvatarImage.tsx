import { useState } from "react";

interface PersonAvatarImageProps {
  /** Optional picture. Initials render when absent or when loading fails. */
  src?: string;
  initials: string;
  imgClassName: string;
  /**
   * Accessible text for the image. "" (the default) is correct whenever an
   * adjacent element already names this person — the common case, since
   * every call site pairs this with a visible name nearby. Pass the full
   * display name only when the avatar is the sole visible identity for that
   * person, per issue #612's accessibility rule: initials must never be the
   * only identity exposed to screen readers, but a redundant name here would
   * double-announce it when a caption already exists.
   */
  alt?: string;
}

/**
 * Image-or-initials fallback shared by every avatar in the app (issue #612):
 * personalized image when usable, initials on load failure or when no image
 * is set, never a broken-image glyph. Extracted from ChatSidebar's local
 * `Avatar` component, which now delegates here instead of keeping its own
 * copy of this state machine.
 *
 * The caller owns the wrapping element (size, background color, shape) —
 * this renders only the image-or-text content, so three call sites with
 * three different wrapper markups do not have to agree on one.
 */
export function PersonAvatarImage({
  src,
  initials,
  imgClassName,
  alt = "",
}: PersonAvatarImageProps) {
  // A load failure is scoped to the URL that was current when it happened,
  // so a change of src must clear it — otherwise an A -> B -> A cycle would
  // never retry A. State is reset during render (guarded so it only runs
  // when src actually changes) rather than in an effect, matching
  // ChatSidebar's original implementation.
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const [trackedSrc, setTrackedSrc] = useState(src);
  if (src !== trackedSrc) {
    setTrackedSrc(src);
    setFailedSrc(null);
  }
  const showImage = Boolean(src) && failedSrc !== src;

  return showImage ? (
    <img
      className={imgClassName}
      src={src}
      alt={alt}
      referrerPolicy="no-referrer"
      onError={() => setFailedSrc(src ?? null)}
    />
  ) : (
    <>{initials}</>
  );
}
