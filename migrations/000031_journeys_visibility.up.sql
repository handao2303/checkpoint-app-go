-- Journey visibility controls who sees a journey in a user's profile list.
-- only_me (default) | followers | public. Existing rows fall back to the default
-- so they stay private until the owner opts in. Note: the journey detail endpoint
-- (GET /journeys/{id}) stays link-shareable regardless of this column; visibility
-- only governs the per-user list (GET /users/{id}/journeys).
ALTER TABLE journeys
  ADD COLUMN visibility TEXT NOT NULL DEFAULT 'only_me'
  CHECK (visibility IN ('only_me', 'followers', 'public'));

CREATE INDEX idx_journeys_visibility ON journeys(user_id, visibility)
  WHERE deleted_at IS NULL;
