-- Serves the workspace teardown and project deletion sweeps of issue_project_scope.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_project_scope_project
    ON issue_project_scope (workspace_id, project_id);
