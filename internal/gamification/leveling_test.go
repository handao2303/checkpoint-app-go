package gamification

import "testing"

func TestLevelForXP(t *testing.T) {
	tests := []struct {
		name string
		xp   int
		want int
	}{
		{"zero", 0, 1},
		{"negative clamps to 1", -50, 1},
		{"just below level 2", 99, 1},
		{"exactly level 2", 100, 2},
		{"just below level 3", 399, 2},
		{"exactly level 3", 400, 3},
		{"level 4", 900, 4},
		{"huge caps at max", 100_000_000, MaxLevel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LevelForXP(tt.xp); got != tt.want {
				t.Errorf("LevelForXP(%d) = %d, want %d", tt.xp, got, tt.want)
			}
		})
	}
}

func TestXPProgress(t *testing.T) {
	tests := []struct {
		name        string
		xp          int
		wantLevel   int
		wantToNext  int
		wantRatioLo float64 // inclusive lower bound for LevelProgress
		wantRatioHi float64 // inclusive upper bound
	}{
		{"start of level 1", 0, 1, 100, 0, 0},
		{"mid level 1", 50, 1, 50, 0.49, 0.51},
		{"start of level 2", 100, 2, 300, 0, 0},
		{"max level", 100_000_000, MaxLevel, 0, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := XPProgress(tt.xp)
			if p.Level != tt.wantLevel {
				t.Errorf("Level = %d, want %d", p.Level, tt.wantLevel)
			}
			if p.XPToNext != tt.wantToNext {
				t.Errorf("XPToNext = %d, want %d", p.XPToNext, tt.wantToNext)
			}
			if p.LevelProgress < tt.wantRatioLo || p.LevelProgress > tt.wantRatioHi {
				t.Errorf("LevelProgress = %f, want in [%f, %f]", p.LevelProgress, tt.wantRatioLo, tt.wantRatioHi)
			}
		})
	}
}
