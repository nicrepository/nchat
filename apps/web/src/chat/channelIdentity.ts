/**
 * A channel's accessible name (issue #1024). ChannelIcon is decorative, so the
 * row or heading around it says "privado" in words — privacy never rests on
 * the lock alone, and every surface says it the same way.
 */
export function channelAccessibleName(name: string, isPrivate: boolean): string {
  return `Canal ${isPrivate ? "privado " : ""}${name}`;
}
