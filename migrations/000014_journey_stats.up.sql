ALTER TABLE journeys
  ADD COLUMN checkpoint_count INT NOT NULL DEFAULT 0,
  ADD COLUMN total_xp         INT NOT NULL DEFAULT 0;

UPDATE journeys j
   SET checkpoint_count = (SELECT COUNT(*)
                             FROM checkpoints c
                            WHERE c.journey_id = j.id AND c.deleted_at IS NULL),
       total_xp         = (SELECT COALESCE(SUM(xp_awarded), 0)
                             FROM checkpoints c
                            WHERE c.journey_id = j.id AND c.deleted_at IS NULL);
