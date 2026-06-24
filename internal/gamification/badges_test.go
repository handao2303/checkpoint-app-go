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
			[]string{"first_checkin", "provinces_5", "city_collector"},
		},
		{"night owl", badgeStats{CheckpointCount: 1, Hour: 3}, []string{"first_checkin", "night_owl"}},
		{"night owl at midnight", badgeStats{CheckpointCount: 1, Hour: 0}, []string{"first_checkin", "night_owl"}},
		{"night owl before cutoff", badgeStats{CheckpointCount: 1, Hour: 4}, []string{"first_checkin", "night_owl"}},
		{"not night owl at cutoff", badgeStats{CheckpointCount: 1, Hour: 5}, []string{"first_checkin"}},
		{"not night owl without checkin", badgeStats{Hour: 3}, nil},
		{"journey starter", badgeStats{CheckpointCount: 1, JourneyCount: 1, Hour: 12}, []string{"first_checkin", "journey_starter"}},
		{"coffee hunter", badgeStats{CheckpointCount: 5, CafeCheckpointCount: 5, Hour: 12}, []string{"first_checkin", "coffee_hunter"}},
		{"social scout", badgeStats{CheckpointCount: 1, ReactionReceivedCount: 10, Hour: 12}, []string{"first_checkin", "social_scout"}},
		{"city collector", badgeStats{CheckpointCount: 3, ProvinceCount: 3, Hour: 12}, []string{"first_checkin", "city_collector"}},
		{"weekend wanderer", badgeStats{CheckpointCount: 4, WeekendCount: 4, Hour: 12}, []string{"first_checkin", "weekend_wanderer"}},
		{"photo storyteller", badgeStats{CheckpointCount: 1, ImageCount: 20, Hour: 12}, []string{"first_checkin", "photo_storyteller"}},
		{"hidden gem finder", badgeStats{CheckpointCount: 1, CreatedPlaceCount: 3, Hour: 12}, []string{"first_checkin", "hidden_gem_finder"}},
		{"passport pro", badgeStats{CheckpointCount: 10, ProvinceCount: 10, Hour: 12}, []string{"first_checkin", "explorer_10", "provinces_5", "city_collector", "passport_pro"}},
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
