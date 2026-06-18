package gamification

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRepositoryInvalidateLeaderboardDeletesRedisKey(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	server.Set(leaderboardTopCacheKey, "cached")

	repo := NewRepository(nil, "", client)
	repo.invalidateLeaderboard(ctx)

	if server.Exists(leaderboardTopCacheKey) {
		t.Fatal("expected leaderboard cache key to be deleted")
	}
}

func TestRepositoryInvalidateLeaderboardIgnoresRedisFailure(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() {
		_ = client.Close()
	})
	repo := NewRepository(nil, "", client)

	repo.invalidateLeaderboard(context.Background())
}
