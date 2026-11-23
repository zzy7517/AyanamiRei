-- A no-op on a normally migrated database: 329 renamed this index to
-- workspace_share_link_pkey and 743 dropped it with the constraint. Its own
-- migration exists so the rollback rebuilds it concurrently (as 328 built it)
-- before 743 attaches it again.
DROP INDEX CONCURRENTLY IF EXISTS workspace_share_link_pkey_uidx;
