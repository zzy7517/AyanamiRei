-- Restores the squad schema of 084-090, 096 and 127. Squad rows and the
-- assignments, bindings and history that pointed at them are not restored.
-- No foreign keys (repo rule): the application removed dependent rows itself.

CREATE TABLE squad (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    leader_id UUID NOT NULL,
    creator_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    avatar_url TEXT,
    archived_at TIMESTAMPTZ,
    archived_by UUID,
    instructions TEXT NOT NULL DEFAULT ''
);

CREATE TABLE squad_member (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    squad_id UUID NOT NULL,
    member_type TEXT NOT NULL CHECK (member_type IN ('agent', 'member')),
    member_id UUID NOT NULL,
    role TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(squad_id, member_type, member_id)
);

ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_assignee_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_assignee_type_check
    CHECK (assignee_type IN ('member', 'agent', 'squad'));

ALTER TABLE quick_action DROP CONSTRAINT IF EXISTS quick_action_assignee_type_check;
ALTER TABLE quick_action ADD CONSTRAINT quick_action_assignee_type_check CHECK (
    (assignee_type = '' AND assignee_id IS NULL) OR
    (assignee_type IN ('agent', 'squad') AND assignee_id IS NOT NULL)
);

ALTER TABLE autopilot
    ADD COLUMN assignee_type TEXT NOT NULL DEFAULT 'agent'
        CHECK (assignee_type IN ('agent', 'squad'));
ALTER TABLE autopilot_run ADD COLUMN squad_id UUID;
ALTER TABLE agent_task_queue ADD COLUMN squad_id UUID NULL;
ALTER TABLE agent_task_queue ADD COLUMN is_leader_task BOOLEAN NOT NULL DEFAULT FALSE;
