/**
 * The current user's role in a workspace, read from the cached member list.
 * Mirrors `useCurrentMember` in `packages/core/permissions`.
 */
import type { MemberRole, MemberWithUser } from "@multica/core/types";

export function memberRoleOf(
  members: readonly MemberWithUser[] | undefined,
  userId: string | null | undefined,
): MemberRole | null {
  if (!members || !userId) return null;
  return members.find((m) => m.user_id === userId)?.role ?? null;
}
