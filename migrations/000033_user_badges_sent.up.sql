ALTER TABLE user_badges ADD COLUMN sent BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX idx_user_badges_unsent ON user_badges(user_id, awarded_at)
    WHERE sent = FALSE;
