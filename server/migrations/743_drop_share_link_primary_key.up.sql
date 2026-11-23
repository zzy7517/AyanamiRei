-- Dropping the constraint drops the index 329 attached, so 744 finds nothing
-- to drop on a normally migrated database.
ALTER TABLE workspace_share_link DROP CONSTRAINT IF EXISTS workspace_share_link_pkey;
