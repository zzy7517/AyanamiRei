-- The "new reply" marker of an issue: when its agent last replied, until a
-- member says they have seen the reply.

-- name: SetIssueAgentRepliedAt :one
-- The marker only moves forward, so a report that is not newer than what
-- stands, or that names an issue outside the workspace, returns no row.
UPDATE issue
SET agent_replied_at = $1
WHERE id = $2
  AND workspace_id = $3
  AND (agent_replied_at IS NULL OR agent_replied_at < $1)
RETURNING agent_replied_at;

-- name: GetIssueAgentRepliedAt :one
SELECT agent_replied_at
FROM issue
WHERE id = $1 AND workspace_id = $2;

-- name: ClearIssueAgentRepliedAt :execrows
-- Only an issue that carries a marker counts, so the caller can tell a
-- clearing from a request that found nothing to clear.
UPDATE issue
SET agent_replied_at = NULL
WHERE id = $1 AND workspace_id = $2 AND agent_replied_at IS NOT NULL;
