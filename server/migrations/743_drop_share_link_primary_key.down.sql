-- The index is built by the down direction of 744, which always runs first.
-- A rollback from a database that applied 743 but not 744 has no such index,
-- because 743 dropped it with the constraint: build it by hand first
-- (CREATE UNIQUE INDEX CONCURRENTLY workspace_share_link_pkey_uidx
-- ON workspace_share_link (id);).
ALTER TABLE workspace_share_link
    ADD CONSTRAINT workspace_share_link_pkey PRIMARY KEY USING INDEX workspace_share_link_pkey_uidx;
