CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_places_category_checkin
    ON places (category, checkin_count DESC);
