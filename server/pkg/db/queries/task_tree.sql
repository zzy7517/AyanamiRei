-- The task-tree lifecycle, read by the daemon that builds and removes trees.

-- name: ListRuntimeWorkspacesByDaemon :many
-- The workspaces a daemon has runtime rows in: what scopes its cross-workspace
-- reads.
SELECT DISTINCT workspace_id FROM agent_runtime
WHERE daemon_id = $1 AND workspace_id IS NOT NULL;

-- The local_directory join leaves out a project the daemon holds no folder
-- for, and DISTINCT ON keeps the first of a project's folders on this daemon
-- (position, then creation). The workspace and issue columns are what a task
-- key is built from.

-- name: ListIssueProjectScopeTreeWork :many
-- The trees a daemon has to act on: pending ones to create, deleted ones to
-- remove from disk. A pending tree goes to a daemon that may build it: one
-- with no daemon recorded yet, or the one that held it before a rebuild. A
-- deleted tree goes only to the daemon that built it.
SELECT DISTINCT ON (t.id)
    t.*,
    w.issue_prefix,
    w.name AS workspace_name,
    i.number AS issue_number,
    p.title AS project_name,
    COALESCE(pr.resource_ref->>'local_path', '')::text AS local_path,
    CASE WHEN t.state = 'pending' THEN 'create' ELSE 'remove' END AS action
FROM issue_project_scope t
JOIN issue i ON i.id = t.issue_id AND i.workspace_id = t.workspace_id
JOIN workspace w ON w.id = t.workspace_id
JOIN project p ON p.id = t.project_id AND p.workspace_id = t.workspace_id
JOIN project_resource pr ON pr.project_id = t.project_id AND pr.workspace_id = t.workspace_id
    AND pr.resource_type = 'local_directory'
    AND pr.resource_ref->>'daemon_id' = sqlc.arg('daemon_id')::text
WHERE t.workspace_id = ANY(sqlc.arg('workspace_ids')::uuid[])
  AND t.state IN ('pending', 'deleted')
  AND (
        (t.state = 'pending' AND (t.daemon_id IS NULL OR t.daemon_id = sqlc.arg('daemon_id')::text))
     OR (t.state = 'deleted' AND t.daemon_id = sqlc.arg('daemon_id')::text)
      )
ORDER BY t.id, pr.position, pr.created_at;

-- name: ListWorkspaceSettingsByIds :many
-- The settings of the given workspaces, for a read that spans the workspaces
-- a daemon acts in.
SELECT id, settings FROM workspace
WHERE id = ANY(sqlc.arg('workspace_ids')::uuid[]);

-- name: ListIssueProjectScopeMatureTrees :many
-- Ready trees of tasks completed before completed_before, for the daemon to
-- remove. A workspace keeps a completed task's trees for its own number of
-- days, so the caller asks with the latest cutoff among its workspaces and
-- keeps each tree to its own workspace's. A tree is on the machine of the
-- daemon that built it, so only that daemon removes it.
SELECT DISTINCT ON (t.id)
    t.*,
    w.issue_prefix,
    w.name AS workspace_name,
    i.number AS issue_number,
    i.completed_at,
    p.title AS project_name,
    COALESCE(pr.resource_ref->>'local_path', '')::text AS local_path,
    'remove'::text AS action
FROM issue_project_scope t
JOIN issue i ON i.id = t.issue_id AND i.workspace_id = t.workspace_id
JOIN workspace w ON w.id = t.workspace_id
JOIN project p ON p.id = t.project_id AND p.workspace_id = t.workspace_id
JOIN project_resource pr ON pr.project_id = t.project_id AND pr.workspace_id = t.workspace_id
    AND pr.resource_type = 'local_directory'
    AND pr.resource_ref->>'daemon_id' = sqlc.arg('daemon_id')::text
WHERE t.workspace_id = ANY(sqlc.arg('workspace_ids')::uuid[])
  AND t.state = 'ready'
  AND i.completed_at IS NOT NULL
  AND i.completed_at < sqlc.arg('completed_before')::timestamptz
  AND t.daemon_id = sqlc.arg('daemon_id')::text
ORDER BY t.id, pr.position, pr.created_at;
