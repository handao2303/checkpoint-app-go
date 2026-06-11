package checkpoint

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
)

// fakeRepo is a minimal in-memory stand-in for the repository interface.
type fakeRepo struct {
	lastCreate   *CreateParams
	createResult *CreateResult
	createErr    error
	deleteKeys   []string
	deleteErr    error
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
func (f *fakeRepo) Delete(context.Context, string, string) ([]string, error) {
	return f.deleteKeys, f.deleteErr
}

// fakeStorage records deleted keys (uploads are unused in these tests).
type fakeStorage struct {
	deleted []string
}

func (f *fakeStorage) Upload(_ context.Context, key, _ string, _ io.Reader) (string, error) {
	return key, nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func ratingPtr(v int16) *int16   { return &v }
func stringPtr(v string) *string { return &v }

type fakeGeocoder struct {
	calls    int
	address  string
	province string
	err      error
}

func (f *fakeGeocoder) Reverse(context.Context, float64, float64) (string, error) {
	f.calls++
	return f.address, f.err
}

func (f *fakeGeocoder) Province(context.Context, float64, float64) (string, error) {
	return f.province, f.err
}

func TestServiceCreate_AwardsCheckinXP(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, nil, nil, nil)

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

func TestServiceCreate_ResolvesAddress(t *testing.T) {
	repo := &fakeRepo{}
	geocoder := &fakeGeocoder{address: "123 Đường Nguyễn Huệ, Quận 1"}

	if _, err := NewService(repo, nil, geocoder, nil).Create(
		context.Background(),
		"user1",
		Input{Name: "Cà phê", Lat: 10.8, Lng: 106.6},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if geocoder.calls != 1 {
		t.Fatalf("geocoder calls = %d, want 1", geocoder.calls)
	}
	if repo.lastCreate.Address == nil || *repo.lastCreate.Address != geocoder.address {
		t.Fatalf("Address = %v, want %q", repo.lastCreate.Address, geocoder.address)
	}
}

func TestServiceCreate_KeepsExplicitAddress(t *testing.T) {
	repo := &fakeRepo{}
	geocoder := &fakeGeocoder{address: "resolved address"}

	if _, err := NewService(repo, nil, geocoder, nil).Create(
		context.Background(),
		"user1",
		Input{Name: "Cà phê", Lat: 10.8, Lng: 106.6, Address: stringPtr("  custom address  ")},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if geocoder.calls != 0 {
		t.Fatalf("geocoder calls = %d, want 0", geocoder.calls)
	}
	if repo.lastCreate.Address == nil || *repo.lastCreate.Address != "custom address" {
		t.Fatalf("Address = %v, want %q", repo.lastCreate.Address, "custom address")
	}
}

func TestServiceCreate_GeocoderFailureIsNonFatal(t *testing.T) {
	repo := &fakeRepo{}
	geocoder := &fakeGeocoder{err: errors.New("provider unavailable")}

	if _, err := NewService(repo, nil, geocoder, nil).Create(
		context.Background(),
		"user1",
		Input{Name: "Cà phê", Lat: 10.8, Lng: 106.6},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.lastCreate.Address != nil {
		t.Fatalf("Address = %q, want nil", *repo.lastCreate.Address)
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
			geocoder := &fakeGeocoder{address: "resolved address"}
			_, err := NewService(repo, nil, geocoder, nil).Create(context.Background(), "u", tt.in)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("expected ErrInvalidInput, got %v", err)
			}
			if geocoder.calls != 0 {
				t.Errorf("geocoder calls = %d, want 0", geocoder.calls)
			}
			if repo.lastCreate != nil {
				t.Error("repo.Create should not be called on invalid input")
			}
		})
	}
}

func TestServiceCreate_ForwardsPlaceID(t *testing.T) {
	repo := &fakeRepo{}
	pid := "place-123"
	_, err := NewService(repo, nil, nil, nil).Create(
		context.Background(), "u1", Input{Name: "Quán", Lat: 10, Lng: 106, PlaceID: &pid})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastCreate.PlaceID == nil || *repo.lastCreate.PlaceID != pid {
		t.Errorf("PlaceID not forwarded: %v", repo.lastCreate.PlaceID)
	}
}

type fakeJourneys struct{ id *string }

func (f *fakeJourneys) ActiveJourneyID(context.Context, string) (*string, error) { return f.id, nil }

func TestServiceCreate_UsesActiveJourney(t *testing.T) {
	repo := &fakeRepo{}
	jid := "journey-1"
	_, err := NewService(repo, nil, nil, &fakeJourneys{id: &jid}).Create(
		context.Background(), "u1", Input{Name: "Quán", Lat: 10, Lng: 106})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastCreate.JourneyID == nil || *repo.lastCreate.JourneyID != jid {
		t.Errorf("JourneyID = %v, want %q", repo.lastCreate.JourneyID, jid)
	}
}

func TestServiceDelete_CleansUpStorage(t *testing.T) {
	repo := &fakeRepo{deleteKeys: []string{
		"checkpoints/cp1/a.jpg",
		"https://cdn.example.com/legacy-full-url.jpg", // legacy row — must be skipped
		"checkpoints/cp1/b.jpg",
	}}
	storage := &fakeStorage{}
	svc := NewService(repo, storage, nil, nil)

	if err := svc.Delete(context.Background(), "cp1", "user1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"checkpoints/cp1/a.jpg", "checkpoints/cp1/b.jpg"}
	if !slices.Equal(storage.deleted, want) {
		t.Errorf("deleted keys = %v, want %v", storage.deleted, want)
	}
}

func TestServiceDelete_NotFoundPropagates(t *testing.T) {
	repo := &fakeRepo{deleteErr: ErrNotFound}
	storage := &fakeStorage{}
	svc := NewService(repo, storage, nil, nil)

	if err := svc.Delete(context.Background(), "cp1", "user1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(storage.deleted) != 0 {
		t.Errorf("storage.deleted = %v, want empty (no cleanup on failure)", storage.deleted)
	}
}
