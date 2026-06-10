package checkpoint

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
		{"huge caps at max", 100_000_000, maxLevel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := levelForXP(tt.xp); got != tt.want {
				t.Errorf("levelForXP(%d) = %d, want %d", tt.xp, got, tt.want)
			}
		})
	}
}
