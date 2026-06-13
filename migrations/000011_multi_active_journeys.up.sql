-- Allow multiple active journeys per user: a check-in now picks which active
-- journey to record under (dropdown), instead of there being a single active one.
DROP INDEX IF EXISTS idx_journeys_one_active;

-- Helps list a user's active journeys for the check-in picker.
CREATE INDEX idx_journeys_active ON journeys(user_id, updated_at DESC)
    WHERE is_active AND deleted_at IS NULL;
