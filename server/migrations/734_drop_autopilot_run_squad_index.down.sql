CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_autopilot_run_squad_id
    ON autopilot_run (squad_id)
    WHERE squad_id IS NOT NULL;
