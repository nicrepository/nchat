/**
 * GroupAvatar — the single visual identity of a group (issue #1026).
 *
 * Automático: the initials of the group's *current* name, so a rename
 * recomputes them and nothing derived is ever stored. Emoji: the one catalogued
 * sequence the group chose. Both sit on the same fixed neutral background —
 * never a colour picked from the name, the id or a hash — and a rounded square
 * keeps a group distinguishable from a person (round UserAvatar) without colour.
 *
 * People keep UserAvatar/Blobatar (#1016) and channels have no avatar; this
 * component is for groups only. Decorative: the group's name is always beside
 * it, so it is hidden from assistive technology. The emoji is a React text
 * node, never markup.
 */

import "./GroupAvatar.css";
import { initialsFrom } from "./messageDisplay";

export type GroupAvatarSize = "sm" | "md" | "lg";

interface GroupAvatarProps {
  name: string;
  emoji?: string;
  size?: GroupAvatarSize;
}

export default function GroupAvatar({ name, emoji, size = "sm" }: GroupAvatarProps) {
  return (
    <span
      className={`group-avatar group-avatar--${size}`}
      data-mode={emoji ? "emoji" : "auto"}
      aria-hidden="true"
    >
      {emoji || initialsFrom(name)}
    </span>
  );
}
