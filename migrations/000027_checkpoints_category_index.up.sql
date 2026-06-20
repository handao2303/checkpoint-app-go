CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_checkpoints_category_active
    ON checkpoints (category)
    WHERE deleted_at IS NULL;
