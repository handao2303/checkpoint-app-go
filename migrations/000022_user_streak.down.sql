ALTER TABLE users
  DROP COLUMN IF EXISTS checkin_streak,
  DROP COLUMN IF EXISTS last_checkin_date;
