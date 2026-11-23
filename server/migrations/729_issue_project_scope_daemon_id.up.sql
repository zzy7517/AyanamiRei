-- The daemon that last reported about a tree. A tree is worked on by the daemon
-- its daemon_id names or, while that is NULL, by the daemon of its project's
-- first local_directory.
ALTER TABLE issue_project_scope ADD COLUMN IF NOT EXISTS daemon_id TEXT;
