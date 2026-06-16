DROP INDEX IF EXISTS idx_checkpoint_images_one_video;

ALTER TABLE checkpoint_images DROP COLUMN IF EXISTS media_type;
