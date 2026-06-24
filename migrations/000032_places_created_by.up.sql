ALTER TABLE places ADD COLUMN created_by UUID REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX idx_places_created_by ON places(created_by) WHERE created_by IS NOT NULL;
