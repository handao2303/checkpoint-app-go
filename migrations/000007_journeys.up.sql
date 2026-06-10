CREATE TABLE journeys (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT,
    is_active   BOOLEAN NOT NULL DEFAULT false,   -- the user's "currently travelling" journey
    started_at  TIMESTAMPTZ,
    ended_at    TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX idx_journeys_user ON journeys(user_id) WHERE deleted_at IS NULL;
-- at most one active journey per user
CREATE UNIQUE INDEX idx_journeys_one_active ON journeys(user_id) WHERE is_active AND deleted_at IS NULL;

ALTER TABLE checkpoints ADD COLUMN journey_id UUID REFERENCES journeys(id);
CREATE INDEX idx_checkpoints_journey ON checkpoints(journey_id);
