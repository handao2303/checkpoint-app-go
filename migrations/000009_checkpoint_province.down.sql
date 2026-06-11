DROP INDEX IF EXISTS idx_checkpoints_province;
ALTER TABLE checkpoints DROP COLUMN IF EXISTS province;
