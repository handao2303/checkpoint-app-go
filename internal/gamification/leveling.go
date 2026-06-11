package gamification

import "math"

// MaxLevel caps progression.
const MaxLevel = 50

// LevelForXP maps total XP to a level on a gently rising curve: level n starts
// at (n-1)^2 * 100 XP. Capped at MaxLevel.
//
//	0 XP    -> level 1
//	100 XP  -> level 2
//	400 XP  -> level 3
//	900 XP  -> level 4
func LevelForXP(xp int) int {
	if xp <= 0 {
		return 1
	}
	level := int(math.Sqrt(float64(xp)/100)) + 1
	if level > MaxLevel {
		return MaxLevel
	}
	return level
}

// XPForLevel is the total XP at which a level begins: (level-1)^2 * 100.
func XPForLevel(level int) int {
	if level <= 1 {
		return 0
	}
	if level > MaxLevel {
		level = MaxLevel
	}
	n := level - 1
	return n * n * 100
}

// Progress describes a user's position within their current level band.
type Progress struct {
	Level         int     `json:"level"`
	XPToNext      int     `json:"xp_to_next"`     // XP remaining to reach the next level (0 at max)
	LevelProgress float64 `json:"level_progress"` // 0..1 within the current level band
}

// XPProgress computes the level and progress toward the next level from total XP.
func XPProgress(xp int) Progress {
	if xp < 0 {
		xp = 0
	}
	level := LevelForXP(xp)
	if level >= MaxLevel {
		return Progress{Level: MaxLevel, XPToNext: 0, LevelProgress: 1}
	}
	cur := XPForLevel(level)
	next := XPForLevel(level + 1)
	band := next - cur
	ratio := 0.0
	if band > 0 {
		ratio = float64(xp-cur) / float64(band)
	}
	return Progress{Level: level, XPToNext: next - xp, LevelProgress: ratio}
}
