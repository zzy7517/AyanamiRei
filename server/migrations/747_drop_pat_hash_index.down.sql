CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_pat_token_hash
    ON personal_access_token(token_hash);
