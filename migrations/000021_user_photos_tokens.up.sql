CREATE TABLE user_photos_tokens (
    user_id       UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    access_token  TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    token_type    TEXT NOT NULL DEFAULT 'Bearer',
    expires_at    TIMESTAMPTZ NOT NULL,
    album_id      TEXT,
    connected_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
