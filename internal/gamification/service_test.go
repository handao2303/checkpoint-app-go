package gamification

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type fakeLeaderboardReader struct {
	top       []LeaderboardEntry
	me        *LeaderboardEntry
	topErr    error
	rankErr   error
	topCalls  int
	rankCalls int
}

func (f *fakeLeaderboardReader) Passport(context.Context, string) (*Passport, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeLeaderboardReader) TopByXP(context.Context, int) ([]LeaderboardEntry, error) {
	f.topCalls++
	return f.top, f.topErr
}

func (f *fakeLeaderboardReader) RankOf(context.Context, string) (*LeaderboardEntry, error) {
	f.rankCalls++
	return f.me, f.rankErr
}

func (f *fakeLeaderboardReader) Badges(context.Context, string) ([]UserBadge, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeLeaderboardReader) ClaimUnsentBadges(context.Context, string) ([]Badge, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeLeaderboardReader) UserIDByUsername(context.Context, string) (string, error) {
	return "mock-user-id", nil
}

func testRedisClient(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	return server, client
}

func leaderboardFixtures() ([]LeaderboardEntry, *LeaderboardEntry) {
	entries := []LeaderboardEntry{
		{Rank: 1, UserID: "u1", Username: "minh", TotalXP: 12560, Level: 32},
		{Rank: 2, UserID: "u2", Username: "nhi", TotalXP: 9870, Level: 29},
	}
	me := &LeaderboardEntry{Rank: 7, UserID: "me", Username: "toi", TotalXP: 3210, Level: 18}
	return entries, me
}

func TestLeaderboardRedisHitSkipsTopQuery(t *testing.T) {
	ctx := context.Background()
	server, client := testRedisClient(t)
	entries, me := leaderboardFixtures()
	payload, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	server.Set(leaderboardTopCacheKey, string(payload))

	repo := &fakeLeaderboardReader{me: me}
	got, err := NewService(repo, client).Leaderboard(ctx, me.UserID)
	if err != nil {
		t.Fatalf("Leaderboard() error = %v", err)
	}
	if repo.topCalls != 0 {
		t.Fatalf("TopByXP calls = %d, want 0", repo.topCalls)
	}
	if repo.rankCalls != 1 {
		t.Fatalf("RankOf calls = %d, want 1", repo.rankCalls)
	}
	if len(got.Entries) != len(entries) || got.Entries[0].UserID != entries[0].UserID {
		t.Fatalf("entries = %#v, want cached %#v", got.Entries, entries)
	}
}

func TestLeaderboardRedisMissQueriesOnceAndWritesTTL(t *testing.T) {
	ctx := context.Background()
	server, client := testRedisClient(t)
	entries, me := leaderboardFixtures()
	repo := &fakeLeaderboardReader{top: entries, me: me}

	got, err := NewService(repo, client).Leaderboard(ctx, me.UserID)
	if err != nil {
		t.Fatalf("Leaderboard() error = %v", err)
	}
	if repo.topCalls != 1 {
		t.Fatalf("TopByXP calls = %d, want 1", repo.topCalls)
	}
	if len(got.Entries) != len(entries) {
		t.Fatalf("entries len = %d, want %d", len(got.Entries), len(entries))
	}
	if !server.Exists(leaderboardTopCacheKey) {
		t.Fatal("expected redis leaderboard cache key to be written")
	}
	if ttl := server.TTL(leaderboardTopCacheKey); ttl <= 0 || ttl > leaderboardTTL {
		t.Fatalf("redis ttl = %v, want within (0, %v]", ttl, leaderboardTTL)
	}
}

func TestLeaderboardMalformedRedisFallsBackToDBAndRefreshes(t *testing.T) {
	ctx := context.Background()
	server, client := testRedisClient(t)
	server.Set(leaderboardTopCacheKey, "not-json")
	entries, me := leaderboardFixtures()
	repo := &fakeLeaderboardReader{top: entries, me: me}

	got, err := NewService(repo, client).Leaderboard(ctx, me.UserID)
	if err != nil {
		t.Fatalf("Leaderboard() error = %v", err)
	}
	if repo.topCalls != 1 {
		t.Fatalf("TopByXP calls = %d, want 1", repo.topCalls)
	}
	if got.Entries[0].UserID != entries[0].UserID {
		t.Fatalf("entries = %#v, want refreshed DB entries", got.Entries)
	}
	raw, err := server.Get(leaderboardTopCacheKey)
	if err != nil {
		t.Fatalf("redis cache missing after refresh: %v", err)
	}
	var cached []LeaderboardEntry
	if err := json.Unmarshal([]byte(raw), &cached); err != nil {
		t.Fatalf("redis cache was not refreshed with valid JSON: %v", err)
	}
}

func TestLeaderboardRedisUnavailableFallsBackToDB(t *testing.T) {
	ctx := context.Background()
	entries, me := leaderboardFixtures()
	repo := &fakeLeaderboardReader{top: entries, me: me}
	client := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:0",
		DialTimeout:  10 * time.Millisecond,
		ReadTimeout:  10 * time.Millisecond,
		WriteTimeout: 10 * time.Millisecond,
	})
	t.Cleanup(func() {
		_ = client.Close()
	})

	got, err := NewService(repo, client).Leaderboard(ctx, me.UserID)
	if err != nil {
		t.Fatalf("Leaderboard() error = %v", err)
	}
	if repo.topCalls != 1 {
		t.Fatalf("TopByXP calls = %d, want 1", repo.topCalls)
	}
	if len(got.Entries) != len(entries) {
		t.Fatalf("entries len = %d, want %d", len(got.Entries), len(entries))
	}
}

func TestLeaderboardAlwaysLoadsCallerRank(t *testing.T) {
	ctx := context.Background()
	server, client := testRedisClient(t)
	entries, me := leaderboardFixtures()
	payload, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	server.Set(leaderboardTopCacheKey, string(payload))
	repo := &fakeLeaderboardReader{me: me}
	service := NewService(repo, client)

	if _, err := service.Leaderboard(ctx, me.UserID); err != nil {
		t.Fatalf("first Leaderboard() error = %v", err)
	}
	if _, err := service.Leaderboard(ctx, me.UserID); err != nil {
		t.Fatalf("second Leaderboard() error = %v", err)
	}
	if repo.topCalls != 0 {
		t.Fatalf("TopByXP calls = %d, want 0", repo.topCalls)
	}
	if repo.rankCalls != 2 {
		t.Fatalf("RankOf calls = %d, want 2", repo.rankCalls)
	}
}
