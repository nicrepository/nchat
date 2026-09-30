import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";

/** Photo when the server published one, initials otherwise. Decorative. */
export default function SearchAvatar({
  seed,
  name,
  url,
}: {
  seed: string;
  name: string;
  url?: string | null;
}) {
  return (
    <span
      className={`global-search__avatar global-search__avatar--${avatarColorFor(seed)}`}
      aria-hidden="true"
    >
      {url ? <img src={url} alt="" referrerPolicy="no-referrer" /> : initialsFrom(name)}
    </span>
  );
}
