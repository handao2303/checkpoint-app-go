DROP INDEX IF EXISTS idx_checkpoints_user_request_hash;

ALTER TABLE checkpoints DROP COLUMN IF EXISTS request_hash;
