package place

import (
	"strings"
	"testing"
)

func TestFeedQueriesUseIndexFriendlyKeysetPagination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		query      string
		wantCursor bool
	}{
		{name: "public first page", query: feedFirstPageQuery},
		{name: "public cursor page", query: feedCursorPageQuery, wantCursor: true},
		{name: "following first page", query: followingFeedFirstPageQuery},
		{name: "following cursor page", query: followingFeedCursorPageQuery, wantCursor: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hasCursor := strings.Contains(tt.query, "(c.created_at, c.id) < ($1, $2)")
			if hasCursor != tt.wantCursor {
				t.Fatalf("cursor predicate present = %v, want %v", hasCursor, tt.wantCursor)
			}
			if strings.Contains(tt.query, "$3::timestamptz IS NULL") ||
				strings.Contains(tt.query, "OR c.created_at") {
				t.Fatal("query contains nullable OR cursor predicate")
			}
			if !strings.Contains(tt.query, "ORDER BY c.created_at DESC, c.id DESC") {
				t.Fatal("query does not match the feed keyset order")
			}
		})
	}
}
