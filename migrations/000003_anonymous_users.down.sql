ALTER TABLE users DROP COLUMN IF EXISTS is_anonymous;
-- Note: fails if any anonymous (NULL-email) users exist; delete them first.
ALTER TABLE users ALTER COLUMN email SET NOT NULL;
