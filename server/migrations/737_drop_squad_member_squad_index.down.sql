CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_squad_member_squad
    ON squad_member(squad_id);
