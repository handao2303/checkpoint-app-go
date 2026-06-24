DROP INDEX IF EXISTS idx_places_created_by;

ALTER TABLE places DROP COLUMN IF EXISTS created_by;
