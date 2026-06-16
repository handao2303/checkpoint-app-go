ALTER TABLE checkpoint_images
  ADD COLUMN media_type TEXT NOT NULL DEFAULT 'image'
  CHECK (media_type IN ('image', 'video'));

CREATE UNIQUE INDEX idx_checkpoint_images_one_video
  ON checkpoint_images(checkpoint_id)
  WHERE media_type = 'video';
