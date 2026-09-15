DROP INDEX IF EXISTS idx_checkpoint_images_thumb_unmeasured;

ALTER TABLE checkpoint_images
  DROP COLUMN IF EXISTS thumb_width,
  DROP COLUMN IF EXISTS thumb_height;
