package checkpoint

import "math"

const maxLevel = 50

// levelForXP maps total XP to a level on a gently rising curve: each level needs
// quadratically more XP (level n starts at (n-1)^2 * 100 XP). Capped at maxLevel.
//
//	0 XP    -> level 1
//	100 XP  -> level 2
//	400 XP  -> level 3
//	900 XP  -> level 4
func levelForXP(xp int) int {
	if xp <= 0 {
		return 1
	}
	level := int(math.Sqrt(float64(xp)/100)) + 1
	if level > maxLevel {
		return maxLevel
	}
	return level
}
