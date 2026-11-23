-- One row per issue, project and base branch; adding a pair to an existing
-- list relies on it.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_issue_project_scope_pair
    ON issue_project_scope (issue_id, project_id, base_branch);
