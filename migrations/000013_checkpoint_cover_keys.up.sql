ALTER TABLE checkpoints
  ADD COLUMN image_key     TEXT,
  ADD COLUMN thumbnail_key TEXT;

UPDATE checkpoints c
   SET image_key     = (SELECT ci.object_key
                          FROM checkpoint_images ci
                         WHERE ci.checkpoint_id = c.id
                         ORDER BY ci.sort_order, ci.created_at LIMIT 1),
       thumbnail_key = (SELECT COALESCE(ci.thumbnail_key, ci.object_key)
                          FROM checkpoint_images ci
                         WHERE ci.checkpoint_id = c.id
                         ORDER BY ci.sort_order, ci.created_at LIMIT 1)
 WHERE EXISTS (SELECT 1 FROM checkpoint_images ci WHERE ci.checkpoint_id = c.id);
