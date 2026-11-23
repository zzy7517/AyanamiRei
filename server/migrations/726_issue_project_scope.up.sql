-- The "project + branch" pairs a task (issue) works on: the ones its author
-- chose for it. base_branch is what the task tree for the pair is created
-- from; the remaining columns are what the daemon reports about that tree,
-- and the server only stores what it is told.
CREATE TABLE IF NOT EXISTS issue_project_scope (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    issue_id     UUID NOT NULL,
    project_id   UUID NOT NULL,
    base_branch  TEXT NOT NULL,
    position     INTEGER NOT NULL DEFAULT 0,
    path         TEXT NOT NULL DEFAULT '',
    task_branch  TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'ready', 'failed', 'deleted')),
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
