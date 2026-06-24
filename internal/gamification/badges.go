package gamification

// badgeStats is the snapshot a badge rule is evaluated against, computed inside
// the check-in transaction.
type badgeStats struct {
	CheckpointCount       int
	ProvinceCount         int
	JourneyCount          int
	Hour                  int // hour-of-day (0..23) of the check-in, Asia/Ho_Chi_Minh
	CafeCheckpointCount   int
	ReactionReceivedCount int
	WeekendCount          int
	ImageCount            int
	CreatedPlaceCount     int
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
		Badge: Badge{Key: "first_checkin", Name: "Bước chân đầu tiên", Icon: "flag", Color: "green",
			Description: "Cắm cờ lần đầu tiên", Level: 1},
		rule: func(s badgeStats) bool { return s.CheckpointCount >= 1 },
	},
	{
		Badge: Badge{Key: "explorer_10", Name: "Nhà thám hiểm", Icon: "compass", Color: "orange",
			Description: "Cắm cờ ở 10 địa điểm", Level: 2},
		rule: func(s badgeStats) bool { return s.CheckpointCount >= 10 },
	},
	{
		Badge: Badge{Key: "provinces_5", Name: "Vòng quanh đất nước", Icon: "map", Color: "gold",
			Description: "Đặt chân tới 5 tỉnh thành", Level: 2},
		rule: func(s badgeStats) bool { return s.ProvinceCount >= 5 },
	},
	{
		Badge: Badge{Key: "night_owl", Name: "Cú đêm", Icon: "moon", Color: "clay",
			Description: "Check-in trong khoảng 0–5h sáng", Level: 2},
		rule: func(s badgeStats) bool { return s.CheckpointCount >= 1 && s.Hour >= 0 && s.Hour < 5 },
	},
	{
		Badge: Badge{Key: "journey_starter", Name: "Kẻ lữ hành", Icon: "luggage", Color: "amber",
			Description: "Bắt đầu hành trình đầu tiên", Level: 1},
		rule: func(s badgeStats) bool { return s.JourneyCount >= 1 },
	},
	{
		Badge: Badge{Key: "coffee_hunter", Name: "Thợ săn cà phê", Icon: "coffee", Color: "brown",
			Description: "Check-in tại 5 quán cafe", Level: 2},
		rule: func(s badgeStats) bool { return s.CafeCheckpointCount >= 5 },
	},
	{
		Badge: Badge{Key: "social_scout", Name: "Trinh sát xã hội", Icon: "users", Color: "blue",
			Description: "Nhận 10 reactions từ người khác", Level: 2},
		rule: func(s badgeStats) bool { return s.ReactionReceivedCount >= 10 },
	},
	{
		Badge: Badge{Key: "city_collector", Name: "Người sưu tập thành phố", Icon: "city", Color: "purple",
			Description: "Mở khóa 3 city stamps", Level: 2},
		rule: func(s badgeStats) bool { return s.ProvinceCount >= 3 },
	},
	{
		Badge: Badge{Key: "weekend_wanderer", Name: "Người lang thang cuối tuần", Icon: "calendar", Color: "pink",
			Description: "Check-in vào 4 cuối tuần khác nhau", Level: 2},
		rule: func(s badgeStats) bool { return s.WeekendCount >= 4 },
	},
	{
		Badge: Badge{Key: "photo_storyteller", Name: "Người kể chuyện bằng hình ảnh", Icon: "camera", Color: "teal",
			Description: "Upload 20 ảnh check-in", Level: 2},
		rule: func(s badgeStats) bool { return s.ImageCount >= 20 },
	},
	{
		Badge: Badge{Key: "hidden_gem_finder", Name: "Người tìm kho báu ẩn", Icon: "gem", Color: "red",
			Description: "Tạo 3 địa điểm mới được duyệt", Level: 2},
		rule: func(s badgeStats) bool { return s.CreatedPlaceCount >= 3 },
	},
	{
		Badge: Badge{Key: "passport_pro", Name: "Chuyên gia hộ chiếu", Icon: "passport", Color: "gold",
			Description: "Mở khóa 10 province/city stamps", Level: 3},
		rule: func(s badgeStats) bool { return s.ProvinceCount >= 10 },
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

// badgeByKey looks up a badge definition by key.
func badgeByKey(key string) (Badge, bool) {
	for _, b := range badgeCatalog {
		if b.Key == key {
			return b.Badge, true
		}
	}
	return Badge{}, false
}
