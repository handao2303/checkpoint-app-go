-- Province (state) of each check-in, resolved via reverse-geocoding. Powers the
-- digital passport (one "stamp" per province visited).
ALTER TABLE checkpoints ADD COLUMN province TEXT;

CREATE INDEX idx_checkpoints_province ON checkpoints(user_id, province)
    WHERE deleted_at IS NULL AND province IS NOT NULL;
