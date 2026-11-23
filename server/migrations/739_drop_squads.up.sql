-- Drop squads. Clear what pointed at one before the schema goes:
--   - an issue assigned to a squad becomes unassigned;
--   - an autopilot or quick action bound to a squad is bound to that squad's
--     leader instead; a binding whose squad is gone loses its target (the
--     autopilot is deleted, the quick action becomes targetless);
--   - inbox rows, activity and wakeup rules that exist only for a squad are
--     deleted, and an assignee-change activity that named a squad now reads
--     as a change to or from no assignee.

UPDATE issue
SET assignee_type = NULL, assignee_id = NULL
WHERE assignee_type = 'squad';

UPDATE autopilot a
SET assignee_id = s.leader_id
FROM squad s
WHERE a.assignee_type = 'squad'
  AND s.id = a.assignee_id
  AND s.workspace_id = a.workspace_id;

DELETE FROM autopilot WHERE assignee_type = 'squad';

UPDATE quick_action qa
SET assignee_type = 'agent', assignee_id = s.leader_id
FROM squad s
WHERE qa.assignee_type = 'squad'
  AND s.id = qa.assignee_id
  AND s.workspace_id = qa.workspace_id;

UPDATE quick_action
SET assignee_type = '', assignee_id = NULL
WHERE assignee_type = 'squad';

DELETE FROM inbox_item WHERE details->>'mention_via' = 'squad';
DELETE FROM activity_log WHERE action = 'squad_leader_evaluated';
UPDATE activity_log SET details = details - 'from_type' - 'from_id' WHERE details->>'from_type' = 'squad';
UPDATE activity_log SET details = details - 'to_type' - 'to_id' WHERE details->>'to_type' = 'squad';

DELETE FROM issue_wakeup_receipt
WHERE wakeup_id IN (
    SELECT id FROM issue_wakeup WHERE condition @> '{"assignee_type": "squad"}'::jsonb
);
DELETE FROM issue_wakeup WHERE condition @> '{"assignee_type": "squad"}'::jsonb;
DELETE FROM chat_wakeup WHERE condition @> '{"assignee_type": "squad"}'::jsonb;

ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_assignee_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_assignee_type_check
    CHECK (assignee_type IN ('member', 'agent'));

ALTER TABLE quick_action DROP CONSTRAINT IF EXISTS quick_action_assignee_type_check;
ALTER TABLE quick_action ADD CONSTRAINT quick_action_assignee_type_check CHECK (
    (assignee_type = '' AND assignee_id IS NULL) OR
    (assignee_type = 'agent' AND assignee_id IS NOT NULL)
);

ALTER TABLE autopilot DROP COLUMN IF EXISTS assignee_type;
ALTER TABLE autopilot_run DROP COLUMN IF EXISTS squad_id;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS squad_id;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS is_leader_task;

DROP TABLE IF EXISTS squad_member;
DROP TABLE IF EXISTS squad;
