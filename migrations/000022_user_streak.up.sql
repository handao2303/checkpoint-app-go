ALTER TABLE users
  ADD COLUMN checkin_streak    INT  NOT NULL DEFAULT 0,
  ADD COLUMN last_checkin_date DATE;
