-- Achievements the user has unlocked. Badge definitions live in code
-- (gamification.badgeCatalog); this table records who earned what, when.
CREATE TABLE user_badges (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    badge_key  TEXT NOT NULL,
    awarded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, badge_key)
);

CREATE INDEX idx_user_badges_user ON user_badges(user_id);
