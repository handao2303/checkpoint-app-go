ALTER TABLE checkpoints ADD COLUMN province_code text;
CREATE INDEX idx_checkpoints_province_code ON checkpoints (user_id, province_code) WHERE (deleted_at IS NULL AND province_code IS NOT NULL);
