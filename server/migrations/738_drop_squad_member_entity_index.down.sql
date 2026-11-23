CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_squad_member_entity
    ON squad_member(member_type, member_id);
