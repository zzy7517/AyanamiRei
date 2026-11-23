-- name: ListConversationRootOwners :many
-- A root folded into a queued task keeps its owner after the merge repoints
-- trigger_comment_id at the newer comment and moves the root into
-- coalesced_comment_ids (MUL-8416).
-- A delivered supplement establishes ownership even when the root did not
-- start a separate task. Pending or failed delivery does not establish it.
-- Routing does not need execution context, results, or other issue threads.
SELECT DISTINCT t.agent_id
FROM agent_task_queue t
WHERE t.issue_id = $1 AND t.agent_id IS NOT NULL
  AND (t.trigger_comment_id = $2 OR $2 = ANY(t.coalesced_comment_ids) OR EXISTS (
      SELECT 1 FROM task_supplement s
      WHERE s.task_id = t.id AND s.issue_id = t.issue_id
        AND s.comment_id = $2 AND s.status = 'delivered'
  ));
