-- Pixel dimensions of the generated thumbnail (checkpoint_images.thumbnail_key).
-- Needed so share pages can emit og:image:width / og:image:height: Messenger (and
-- Facebook on a first, uncached scrape) often renders no preview image at all
-- when the dimensions are absent, because it will not block on fetching them.
--
-- NULL means "not measured yet" — backfilled by cmd/backfill-thumbdims and
-- filled in lazily for anything that command misses.
ALTER TABLE checkpoint_images
  ADD COLUMN thumb_width  INT,
  ADD COLUMN thumb_height INT;

-- Partial index drives the backfill scan (rows that still need measuring) and
-- stays tiny once the backfill has run.
CREATE INDEX idx_checkpoint_images_thumb_unmeasured
  ON checkpoint_images(id)
  WHERE thumbnail_key IS NOT NULL AND thumb_width IS NULL;
