DROP INDEX IF EXISTS idx_checkpoints_province_code;
ALTER TABLE checkpoints DROP COLUMN IF EXISTS province_code;
