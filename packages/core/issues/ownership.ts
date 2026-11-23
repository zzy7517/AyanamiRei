import type { Agent, IssueAssigneeType } from "../types";

/**
 * Whether an issue is the viewer's own work: assigned to an agent they own.
 * Everything else they watch they are only a bystander on, which is where the
 * inbox offers "unsubscribe & archive".
 */
export function isOwnIssue(
  issue: { assignee_type?: IssueAssigneeType | null; assignee_id?: string | null },
  userId: string | null | undefined,
  agents: readonly Pick<Agent, "id" | "owner_id">[],
): boolean {
  if (!userId || issue.assignee_type !== "agent" || !issue.assignee_id) return false;
  return agents.some((agent) => agent.id === issue.assignee_id && agent.owner_id === userId);
}
