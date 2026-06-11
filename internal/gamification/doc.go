// Package gamification owns the XP/level system, the digital passport
// (provinces visited), the leaderboard, and badge achievements.
//
// XP awards are recorded as append-only xp_events and applied within the
// caller's transaction (e.g. the check-in tx) via AwardXPTx, so XP, level, and
// the event log stay atomic with the action that earned them. Read models
// (passport, leaderboard, badges) are served by the Service over its own
// queries; the leaderboard is cached like place.Popular.
package gamification
