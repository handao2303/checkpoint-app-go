package gamification

import (
	"reflect"

	"github.com/redis/go-redis/v9"
)

const leaderboardTopCacheKey = "leaderboard:top:v1"

func usableRedis(client redis.Cmdable) redis.Cmdable {
	if client == nil {
		return nil
	}
	value := reflect.ValueOf(client)
	if value.Kind() == reflect.Ptr && value.IsNil() {
		return nil
	}
	return client
}
