CREATE INDEX CONCURRENTLY idx_checkpoints_feed_active
    ON checkpoints (created_at DESC, id DESC)
    WHERE deleted_at IS NULL;
