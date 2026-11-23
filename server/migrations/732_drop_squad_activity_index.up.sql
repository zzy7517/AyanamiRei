-- Only squad leader evaluations wrote the rows this partial index covered.
DROP INDEX CONCURRENTLY IF EXISTS idx_activity_log_squad_no_action_task;
