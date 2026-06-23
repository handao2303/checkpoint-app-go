DROP INDEX IF EXISTS idx_journeys_visibility;
ALTER TABLE journeys DROP COLUMN IF EXISTS visibility;
