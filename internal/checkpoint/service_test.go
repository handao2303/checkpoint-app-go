package checkpoint

import (
	"context"
	"errors"
	"testing"
)

// fakeRepo is a minimal in-memory stand-in for the repository interface.
type fakeRepo struct {
	lastCreate   *CreateParams
	createResult *CreateResult
	createErr    error
}

func (f *fakeRepo) Create(_ context.Context, p CreateParams) (*CreateResult, error) {
	f.lastCreate = &p
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createResult != nil {
		return f.createResult, nil
	}
	return &CreateResult{Checkpoint: &Checkpoint{ID: "cp1"}, XPAwarded: p.XPReward, TotalXP: p.XPReward, Level: 1}, nil
}
func (f *fakeRepo) GetByID(context.Context, string) (*Checkpoint, error)           { return nil, ErrNotFound }
func (f *fakeRepo) GetDetail(context.Context, string) (*Detail, error)             { return nil, ErrNotFound }
func (f *fakeRepo) ListByUser(context.Context, string, int) ([]*Checkpoint, error) { return nil, nil }
func (f *fakeRepo) ListInBounds(context.Context, float64, float64, float64, float64, int) ([]*Checkpoint, error) {
	return nil, nil
}
func (f *fakeRepo) ListNearby(context.Context, float64, float64, float64, int) ([]*Checkpoint, error) {
	return nil, nil
}
func (f *fakeRepo) AddImages(context.Context, string, []string, int) ([]Image, error) {
	return nil, nil
}
func (f *fakeRepo) CountImages(context.Context, string) (int, error) { return 0, nil }
func (f *fakeRepo) Delete(context.Context, string, string) error     { return nil }

func ratingPtr(v int16) *int16 { return &v }

func TestServiceCreate_AwardsCheckinXP(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, nil)

	res, err := svc.Create(context.Background(), "user1", Input{Name: "Cà phê", Lat: 10.8, Lng: 106.6})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastCreate.XPReward != checkinXP {
		t.Errorf("XPReward = %d, want %d", repo.lastCreate.XPReward, checkinXP)
	}
	if repo.lastCreate.Category != "other" {
		t.Errorf("Category default = %q, want %q", repo.lastCreate.Category, "other")
	}
	if res.XPAwarded != checkinXP {
		t.Errorf("result XPAwarded = %d, want %d", res.XPAwarded, checkinXP)
	}
}

func TestServiceCreate_Validation(t *testing.T) {
	tests := []struct {
		name string
		in   Input
	}{
		{"empty name", Input{Name: "  ", Lat: 10, Lng: 106}},
		{"lat too high", Input{Name: "x", Lat: 91, Lng: 106}},
		{"lng too low", Input{Name: "x", Lat: 10, Lng: -181}},
		{"rating out of range", Input{Name: "x", Lat: 10, Lng: 106, Rating: ratingPtr(6)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			_, err := NewService(repo, nil).Create(context.Background(), "u", tt.in)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("expected ErrInvalidInput, got %v", err)
			}
			if repo.lastCreate != nil {
				t.Error("repo.Create should not be called on invalid input")
			}
		})
	}
}
