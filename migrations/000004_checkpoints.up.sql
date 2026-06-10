CREATE TABLE checkpoints (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    category   TEXT NOT NULL DEFAULT 'other',   -- cafe | food | view | nature | ...
    note       TEXT,
    rating     SMALLINT CHECK (rating BETWEEN 1 AND 5),
    lat        DOUBLE PRECISION NOT NULL,
    lng        DOUBLE PRECISION NOT NULL,
    -- geography derived from lng/lat: reads use lat/lng, spatial queries use location
    location   GEOGRAPHY(Point, 4326)
               GENERATED ALWAYS AS (ST_SetSRID(ST_MakePoint(lng, lat), 4326)::geography) STORED,
    address    TEXT,
    xp_awarded INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_checkpoints_location ON checkpoints USING GIST (location);
CREATE INDEX idx_checkpoints_user ON checkpoints(user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_checkpoints_created ON checkpoints(created_at DESC);

CREATE TABLE checkpoint_images (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    checkpoint_id UUID NOT NULL REFERENCES checkpoints(id) ON DELETE CASCADE,
    url           TEXT NOT NULL,
    sort_order    INT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_checkpoint_images_cp ON checkpoint_images(checkpoint_id);
