package gamification

import (
	"slices"
	"testing"
)

func TestSatisfiedBadges(t *testing.T) {
	tests := []struct {
		name  string
		stats badgeStats
		want  []string
	}{
		{"nothing", badgeStats{}, nil},
		{"first checkin", badgeStats{CheckpointCount: 1, Hour: 12}, []string{"first_checkin"}},
		{
			"explorer + first",
			badgeStats{CheckpointCount: 10, Hour: 12},
			[]string{"first_checkin", "explorer_10"},
		},
		{
			"five provinces",
			badgeStats{CheckpointCount: 5, ProvinceCount: 5, Hour: 12},
			[]string{"first_checkin", "provinces_5"},
		},
		{"night owl", badgeStats{CheckpointCount: 1, Hour: 3}, []string{"first_checkin", "night_owl"}},
		{"night owl at midnight", badgeStats{CheckpointCount: 1, Hour: 0}, []string{"first_checkin", "night_owl"}},
		{"night owl before cutoff", badgeStats{CheckpointCount: 1, Hour: 4}, []string{"first_checkin", "night_owl"}},
		{"not night owl at cutoff", badgeStats{CheckpointCount: 1, Hour: 5}, []string{"first_checkin"}},
		{"not night owl without checkin", badgeStats{Hour: 3}, nil},
		{"journey starter", badgeStats{CheckpointCount: 1, JourneyCount: 1, Hour: 12}, []string{"first_checkin", "journey_starter"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := satisfiedBadges(tt.stats)
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			for _, key := range tt.want {
				if !slices.Contains(got, key) {
					t.Errorf("expected badge %q in %v", key, got)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
