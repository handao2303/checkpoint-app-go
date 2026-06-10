// Package place models canonical locations (a "place") that many users check
// into. A checkpoint is matched to an existing nearby place with a similar name
// (pg_trgm) or a new place is created. Discovery (search, nearby, detail, feed,
// popular) reads from places.
package place
