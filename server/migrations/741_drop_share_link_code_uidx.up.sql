-- Share links are dropped in 745. Each concurrently built index goes first,
-- in its own migration, so a rollback can rebuild it the same way (740-744
-- mirror the build order of 328-331 and 419).
DROP INDEX CONCURRENTLY IF EXISTS workspace_share_link_code_uidx;
