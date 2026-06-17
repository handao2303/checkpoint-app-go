DROP INDEX IF EXISTS idx_checkpoints_journey_order;

ALTER TABLE checkpoints DROP COLUMN IF EXISTS journey_order;
