-- name: ListAssigneeRunsForHandoff :many
-- Preview and explicit stop must cover every unfinished run of the previous
-- assignee, including stage handoffs still waiting for a pending queue slot.
SELECT id, agent_id, status, started_at, created_at
FROM agent_task_queue
WHERE issue_id = @issue_id
  AND agent_id = @assignee_id
  AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
ORDER BY created_at DESC, id DESC;
