ALTER TABLE checkpoints ADD COLUMN IF NOT EXISTS journey_order INT;

CREATE INDEX IF NOT EXISTS idx_checkpoints_journey_order
    ON checkpoints (journey_id, journey_order, created_at)
    WHERE deleted_at IS NULL;
