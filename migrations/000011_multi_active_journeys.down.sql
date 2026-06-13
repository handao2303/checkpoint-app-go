DROP INDEX IF EXISTS idx_journeys_active;

-- Restore the one-active-journey constraint. NOTE: this fails if any user
-- currently has more than one active journey — deactivate extras first.
CREATE UNIQUE INDEX idx_journeys_one_active ON journeys(user_id)
    WHERE is_active AND deleted_at IS NULL;
