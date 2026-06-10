-- Store R2 object keys (not full URLs). The public URL is built at read time
-- from R2_PUBLIC_URL, so changing the CDN/bucket needs no data migration.
ALTER TABLE users RENAME COLUMN avatar_url TO avatar_key;
ALTER TABLE checkpoint_images RENAME COLUMN url TO object_key;
