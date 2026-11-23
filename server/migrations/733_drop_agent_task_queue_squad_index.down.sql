CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_task_queue_squad_id_idx
    ON agent_task_queue (squad_id)
    WHERE squad_id IS NOT NULL;
