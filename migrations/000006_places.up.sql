CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE places (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT NOT NULL,
    category      TEXT NOT NULL DEFAULT 'other',
    lat           DOUBLE PRECISION NOT NULL,
    lng           DOUBLE PRECISION NOT NULL,
    location      GEOGRAPHY(Point, 4326)
                  GENERATED ALWAYS AS (ST_SetSRID(ST_MakePoint(lng, lat), 4326)::geography) STORED,
    address       TEXT,
    checkin_count INT NOT NULL DEFAULT 0,   -- denormalized for popular/ranking
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_places_location ON places USING GIST (location);
CREATE INDEX idx_places_name_trgm ON places USING GIN (name gin_trgm_ops);
CREATE INDEX idx_places_checkin_count ON places (checkin_count DESC);

ALTER TABLE checkpoints ADD COLUMN place_id UUID REFERENCES places(id);
CREATE INDEX idx_checkpoints_place ON checkpoints(place_id);
