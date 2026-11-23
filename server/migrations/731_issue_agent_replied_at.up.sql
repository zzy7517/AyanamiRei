-- When the agent of an issue last replied, for the "new reply" marker of the
-- task list. Null once a member has seen the reply. It is read with the
-- issue row and written by issue id, so it has no index.
ALTER TABLE issue ADD COLUMN IF NOT EXISTS agent_replied_at TIMESTAMPTZ;
