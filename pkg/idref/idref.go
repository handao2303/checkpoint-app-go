// Package idref distinguishes a stable user id (UUID) from a mutable handle
// such as a username, so lookups can accept either form. Usernames can change,
// so callers should prefer the id; the username path remains for backward
// compatibility with existing links.
package idref

import "regexp"

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is a canonical UUID. Usernames are [a-z0-9_]{3,20}
// and never contain dashes, so a match means the caller passed an id.
func IsUUID(s string) bool {
	return uuidRe.MatchString(s)
}
