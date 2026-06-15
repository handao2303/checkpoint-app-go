package checkpoint

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRepositoryViewCount(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	repo := &Repository{redis: client}
	if err := server.Set(viewKey("cp-1"), "42"); err != nil {
		t.Fatalf("seed view count: %v", err)
	}

	count := <-repo.viewCount(context.Background(), "cp-1")
	if count == nil || *count != 42 {
		t.Fatalf("viewCount() = %v, want 42", count)
	}
}

func TestRepositoryViewCountMiss(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	repo := &Repository{redis: client}
	if count := <-repo.viewCount(context.Background(), "missing"); count != nil {
		t.Fatalf("viewCount() = %d, want nil", *count)
	}
}
