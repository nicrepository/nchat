/** Stable, non-personal seed shared by every surface that represents a user. */
export function avatarSeed(workspaceId: string, userId: string): string {
  return `nchat:${workspaceId}:${userId}`;
}
