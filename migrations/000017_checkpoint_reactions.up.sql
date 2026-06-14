CREATE TABLE checkpoint_reactions (
  checkpoint_id UUID NOT NULL REFERENCES checkpoints(id) ON DELETE CASCADE,
  user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  reaction      TEXT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (checkpoint_id, user_id)
);

CREATE INDEX idx_checkpoint_reactions_cp ON checkpoint_reactions(checkpoint_id);
