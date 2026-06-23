ALTER TABLE checkpoints ADD COLUMN request_hash TEXT;

CREATE UNIQUE INDEX idx_checkpoints_user_request_hash
  ON checkpoints(user_id, request_hash)
  WHERE request_hash IS NOT NULL AND deleted_at IS NULL;
