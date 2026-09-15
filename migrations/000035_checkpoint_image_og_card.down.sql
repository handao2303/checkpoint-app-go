DROP INDEX IF EXISTS idx_checkpoint_images_og_pending;

ALTER TABLE checkpoint_images
  DROP COLUMN IF EXISTS og_key,
  DROP COLUMN IF EXISTS og_width,
  DROP COLUMN IF EXISTS og_height;
