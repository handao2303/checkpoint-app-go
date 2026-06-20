package identity

import (
	"context"
	"testing"
	"time"
)

type fakeIdentityRepo struct {
	usersByID       map[string]*User
	usersByUsername map[string]*User
	followers       []PublicUserSummary
	followingUsers  []PublicUserSummary
	followersUserID string
	followingUserID string
}

func (f *fakeIdentityRepo) GetByID(_ context.Context, id string) (*User, error) {
	if u := f.usersByID[id]; u != nil {
		return u, nil
	}
	return nil, ErrUserNotFound
}

func (f *fakeIdentityRepo) GetByIDs(context.Context, []string) ([]*User, error) {
	return nil, nil
}

func (f *fakeIdentityRepo) GetBySupabaseID(context.Context, string) (*User, error) {
	return nil, ErrUserNotFound
}

func (f *fakeIdentityRepo) GetByUsername(_ context.Context, username string) (*User, error) {
	if u := f.usersByUsername[username]; u != nil {
		return u, nil
	}
	return nil, ErrUserNotFound
}

func (f *fakeIdentityRepo) CreateUser(context.Context, CreateUserParams) (*User, error) {
	return nil, nil
}

func (f *fakeIdentityRepo) UpdateProfile(context.Context, string, *string, *string, *string) (*User, error) {
	return nil, nil
}

func (f *fakeIdentityRepo) UpdateAvatar(context.Context, string, string) (*User, error) {
	return nil, nil
}

func (f *fakeIdentityRepo) UpdateAuthIdentity(context.Context, string, *string, bool) (*User, error) {
	return nil, nil
}

func (f *fakeIdentityRepo) CreateRefreshToken(context.Context, string, string, time.Time) error {
	return nil
}

func (f *fakeIdentityRepo) GetRefreshToken(context.Context, string) (*RefreshToken, error) {
	return nil, ErrRefreshTokenNotFound
}

func (f *fakeIdentityRepo) RevokeRefreshToken(context.Context, string) error {
	return nil
}

func (f *fakeIdentityRepo) Follow(context.Context, string, string) error {
	return nil
}

func (f *fakeIdentityRepo) Unfollow(context.Context, string, string) error {
	return nil
}

func (f *fakeIdentityRepo) GetFolloweeIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

func (f *fakeIdentityRepo) GetFollowers(_ context.Context, userID string) ([]PublicUserSummary, error) {
	f.followersUserID = userID
	return f.followers, nil
}

func (f *fakeIdentityRepo) GetFollowingUsers(_ context.Context, userID string) ([]PublicUserSummary, error) {
	f.followingUserID = userID
	return f.followingUsers, nil
}

func TestServiceGetFollowersResolvesUsername(t *testing.T) {
	target := &User{ID: "11111111-1111-1111-1111-111111111111", Username: "target"}
	repo := &fakeIdentityRepo{
		usersByID:       map[string]*User{target.ID: target},
		usersByUsername: map[string]*User{target.Username: target},
		followers: []PublicUserSummary{
			{ID: "follower-new", Username: "newest"},
			{ID: "follower-old", Username: "oldest"},
		},
	}

	got, err := NewService(repo, nil, nil, nil).GetFollowers(context.Background(), "target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.followersUserID != target.ID {
		t.Fatalf("followers queried for %q, want %q", repo.followersUserID, target.ID)
	}
	if len(got) != 2 || got[0].Username != "newest" || got[1].Username != "oldest" {
		t.Fatalf("followers = %#v, want repo order preserved", got)
	}
}

func TestServiceGetFollowingUsersResolvesID(t *testing.T) {
	target := &User{ID: "22222222-2222-2222-2222-222222222222", Username: "target"}
	repo := &fakeIdentityRepo{
		usersByID:       map[string]*User{target.ID: target},
		usersByUsername: map[string]*User{target.Username: target},
		followingUsers: []PublicUserSummary{
			{ID: "followee-new", Username: "newest"},
			{ID: "followee-old", Username: "oldest"},
		},
	}

	got, err := NewService(repo, nil, nil, nil).GetFollowingUsers(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.followingUserID != target.ID {
		t.Fatalf("following queried for %q, want %q", repo.followingUserID, target.ID)
	}
	if len(got) != 2 || got[0].Username != "newest" || got[1].Username != "oldest" {
		t.Fatalf("following = %#v, want repo order preserved", got)
	}
}
