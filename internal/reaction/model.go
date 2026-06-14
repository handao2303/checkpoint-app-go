package reaction

import "errors"

// ErrInvalidReaction is returned when a reaction key isn't in the allowed set.
var ErrInvalidReaction = errors.New("invalid reaction")

// AllowedReactions is the fixed set of reaction keys the client may send. They
// map 1:1 to the self-hosted SVG reaction icon set on the web client.
var AllowedReactions = map[string]bool{
	"like": true,
	"love": true,
	"fire": true,
	"haha": true,
	"sad":  true,
}

// Summary is the reaction state of a checkpoint: per-type counts, the grand
// total, and the caller's own reaction (nil when they haven't reacted).
type Summary struct {
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
	Mine   *string        `json:"mine"`
}
