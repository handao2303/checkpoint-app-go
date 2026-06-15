CREATE INDEX CONCURRENTLY idx_checkpoints_user_feed_active
    ON checkpoints (user_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;
