CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "postgis";

CREATE TABLE users (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    supabase_id      TEXT UNIQUE NOT NULL,        -- link to Supabase Auth user
    email            TEXT UNIQUE NOT NULL,
    username         TEXT UNIQUE NOT NULL,
    display_name     TEXT,
    avatar_url       TEXT,                         -- Cloudflare R2 URL
    bio              TEXT,
    role             TEXT NOT NULL DEFAULT 'user', -- 'user' | 'admin'

    -- denormalized stats for fast profile/leaderboard reads
    total_xp         INT NOT NULL DEFAULT 0,
    level            INT NOT NULL DEFAULT 1,
    checkpoint_count INT NOT NULL DEFAULT 0,
    journey_count    INT NOT NULL DEFAULT 0,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at       TIMESTAMPTZ                   -- soft delete
);

CREATE INDEX idx_users_supabase_id ON users(supabase_id);
CREATE INDEX idx_users_username ON users(username);
CREATE INDEX idx_users_deleted_at ON users(deleted_at) WHERE deleted_at IS NULL;
