-- Dedicated social-preview card, rendered from the original at 1200x630 (the
-- 1.91:1 ratio Facebook and Messenger ask for).
--
-- Thumbnails cannot serve this purpose: they are capped at a 640px longest edge
-- for the feed, so a portrait photo yields a 360-480px-wide thumbnail, below the
-- ~600px Messenger needs to render a large card. Widening thumbnails instead
-- would make every feed image heavier, so the two concerns get separate objects:
-- thumbnail_key stays small for the app, og_key is generated only for sharing.
--
-- NULL og_key means "no card" — either not generated yet, or the original is too
-- small to crop one from without inventing detail. Share pages fall back to the
-- thumbnail in that case.
ALTER TABLE checkpoint_images
  ADD COLUMN og_key    TEXT,
  ADD COLUMN og_width  INT,
  ADD COLUMN og_height INT;

-- Drives the card backfill scan: rows with a usable original and no card yet.
CREATE INDEX idx_checkpoint_images_og_pending
  ON checkpoint_images(id)
  WHERE og_key IS NULL;
