-- Append-only log of XP awards (audit + future analytics / streaks).
CREATE TABLE xp_events (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type       TEXT NOT NULL,            -- 'checkin' | ... (see gamification.Event*)
    xp         INT  NOT NULL,
    ref_id     UUID,                     -- the entity that earned it (e.g. checkpoint id)
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_xp_events_user ON xp_events(user_id, created_at DESC);
