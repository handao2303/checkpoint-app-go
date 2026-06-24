DROP INDEX IF EXISTS idx_user_badges_unsent;

ALTER TABLE user_badges DROP COLUMN IF EXISTS sent;
