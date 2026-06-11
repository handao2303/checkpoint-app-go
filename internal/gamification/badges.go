package gamification

// badgeStats is the snapshot a badge rule is evaluated against, computed inside
// the check-in transaction.
type badgeStats struct {
	CheckpointCount int
	ProvinceCount   int
	JourneyCount    int
	Hour            int // hour-of-day (0..23) of the check-in, Asia/Ho_Chi_Minh
}

// badgeDef is a badge definition plus the rule that unlocks it.
type badgeDef struct {
	Badge
	rule func(badgeStats) bool
}

// badgeCatalog is the ordered list of all achievements. Adding a badge here is
// all that's needed — evaluation and the /me/badges listing both read it.
var badgeCatalog = []badgeDef{
	{
		Badge: Badge{Key: "first_checkin", Name: "Bước chân đầu tiên", Icon: "🚩",
			Description: "Cắm cờ lần đầu tiên"},
		rule: func(s badgeStats) bool { return s.CheckpointCount >= 1 },
	},
	{
		Badge: Badge{Key: "explorer_10", Name: "Nhà thám hiểm", Icon: "🧭",
			Description: "Cắm cờ ở 10 địa điểm"},
		rule: func(s badgeStats) bool { return s.CheckpointCount >= 10 },
	},
	{
		Badge: Badge{Key: "provinces_5", Name: "Vòng quanh đất nước", Icon: "🗺️",
			Description: "Đặt chân tới 5 tỉnh thành"},
		rule: func(s badgeStats) bool { return s.ProvinceCount >= 5 },
	},
	{
		Badge: Badge{Key: "night_owl", Name: "Cú đêm", Icon: "🦉",
			Description: "Check-in trong khoảng 0–5h sáng"},
		rule: func(s badgeStats) bool { return s.Hour >= 0 && s.Hour < 5 },
	},
	{
		Badge: Badge{Key: "journey_starter", Name: "Kẻ lữ hành", Icon: "🧳",
			Description: "Bắt đầu hành trình đầu tiên"},
		rule: func(s badgeStats) bool { return s.JourneyCount >= 1 },
	},
}

// satisfiedBadges returns the keys of all badges whose rule the stats meet.
func satisfiedBadges(s badgeStats) []string {
	keys := make([]string, 0, len(badgeCatalog))
	for _, b := range badgeCatalog {
		if b.rule(s) {
			keys = append(keys, b.Key)
		}
	}
	return keys
}
