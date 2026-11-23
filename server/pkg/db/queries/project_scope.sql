-- The "project + branch" pairs a task (issue) works on, each with the tree
-- the daemon reports for it.

-- name: ListIssueProjectScopes :many
SELECT * FROM issue_project_scope
WHERE issue_id = $1 AND workspace_id = $2
ORDER BY position ASC, created_at ASC;

-- name: GetIssueProjectScope :one
SELECT * FROM issue_project_scope
WHERE id = $1 AND issue_id = $2 AND workspace_id = $3;

-- name: LockIssueForProjectScopeWrite :one
-- Serializes two writers replacing the same task's list, and waits for the
-- task's deletion (LockIssueForDelete) without blocking task enqueue.
SELECT id FROM issue
WHERE id = $1 AND workspace_id = $2
FOR NO KEY UPDATE;

-- name: UpsertIssueProjectScope :one
-- A pair the new list keeps keeps its tree's reported state; only the order
-- moves. A pair new to the task starts pending.
INSERT INTO issue_project_scope (workspace_id, issue_id, project_id, base_branch, position)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (issue_id, project_id, base_branch) DO UPDATE
SET position = EXCLUDED.position
RETURNING *;

-- name: DeleteIssueProjectScopesReadyGuarded :execrows
-- Leaves a tree alone that became ready after the writer read the list: a
-- ready tree exists on disk and its removal has to be refused, not raced.
DELETE FROM issue_project_scope
WHERE id = ANY(sqlc.arg('ids')::uuid[])
  AND issue_id = sqlc.arg('issue_id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND state <> 'ready';

-- name: UpdateIssueProjectScopeTree :one
-- The from_states guard is what makes an invalid transition a no-op the
-- caller can tell apart from a missing tree.
UPDATE issue_project_scope
SET state = $1,
    path = $2,
    task_branch = $3,
    error = $4,
    updated_at = now(),
    -- The daemon that reported stays on the row; a NULL keeps the stored one.
    daemon_id = COALESCE(sqlc.narg('daemon_id'), daemon_id)
WHERE id = $5
  AND issue_id = $6
  AND workspace_id = $7
  AND state = ANY(sqlc.arg('from_states')::text[])
RETURNING *;

-- name: DeleteIssueProjectScopesByProject :execrows
DELETE FROM issue_project_scope
WHERE project_id = $1 AND workspace_id = $2;

-- name: GetIssueProjectScopeWorkspace :one
-- The daemon routes name an issue by id alone; its workspace is what gates them.
SELECT workspace_id
FROM issue
WHERE id = $1;
