-- Anonymous users have no email, so email becomes nullable.
-- (Postgres treats NULLs as distinct, so the UNIQUE constraint still holds.)
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users ADD COLUMN is_anonymous BOOLEAN NOT NULL DEFAULT false;
