/**
 * ChannelIcon — the one visual identity a channel has (issue #1024).
 *
 * A channel never has an avatar: no image, emoji, initials or colour derived
 * from its name or id. Its identity is its visibility and nothing else — `#`
 * for a public channel, `#` plus a lock for a private one — so a rename changes
 * the text beside it and never this icon.
 *
 * Always decorative (aria-hidden): the control or heading around it owns the
 * accessible name, built with channelAccessibleName (./channelIdentity) so
 * privacy is announced in words and never depends on the lock alone.
 *
 * One glyph, not a lock laid over a `#`: the private `#` is drawn with its
 * bottom-right corner left open for the lock, so the pair reads on any row
 * background (selected, hovered or not) without a cut-out colour to keep in
 * sync with it.
 */
export default function ChannelIcon({
  isPrivate,
  className,
}: {
  isPrivate: boolean;
  className?: string;
}) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
      width="20"
      height="20"
      data-channel-icon={isPrivate ? "private" : "public"}
    >
      <line x1="10" y1="4" x2="8" y2="20" />
      <line x1="4" y1="9" x2="20" y2="9" />
      {isPrivate ? (
        <>
          <line x1="16" y1="4" x2="15.25" y2="10" />
          <line x1="3" y1="15" x2="12" y2="15" />
          <g data-channel-lock="">
            <rect x="14" y="16" width="9" height="7" rx="1.5" />
            <path d="M16 16v-2a2.5 2.5 0 0 1 5 0v2" />
          </g>
        </>
      ) : (
        <>
          <line x1="16" y1="4" x2="14" y2="20" />
          <line x1="3" y1="15" x2="19" y2="15" />
        </>
      )}
    </svg>
  );
}
