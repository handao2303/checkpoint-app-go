package checkpoint

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

// fakeRepo is a minimal in-memory stand-in for the repository interface.
type fakeRepo struct {
	lastCreate   *CreateParams
	createResult *CreateResult
	createErr    error
	checkpoint   *Checkpoint
	addedKeys    []ImageKeys
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
func (f *fakeRepo) GetByID(context.Context, string) (*Checkpoint, error) {
	if f.checkpoint != nil {
		return f.checkpoint, nil
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) GetDetail(context.Context, string) (*Detail, error)             { return nil, ErrNotFound }
func (f *fakeRepo) ListByUser(context.Context, string, string, int) ([]*Checkpoint, error) { return nil, nil }
func (f *fakeRepo) ListUnassignedByUser(context.Context, string, int, *CheckpointCursor) ([]*Checkpoint, error) {
	return nil, nil
}
func (f *fakeRepo) ListInBounds(context.Context, float64, float64, float64, float64, int) ([]*Checkpoint, error) {
	return nil, nil
}
func (f *fakeRepo) ListNearby(context.Context, float64, float64, float64, int) ([]*Checkpoint, error) {
	return nil, nil
}
func (f *fakeRepo) AddImages(_ context.Context, _ string, keys []ImageKeys, _ int) ([]Image, error) {
	f.addedKeys = keys
	return nil, nil
}
func (f *fakeRepo) CountImages(context.Context, string) (int, error) { return 0, nil }
func (f *fakeRepo) Delete(context.Context, string, string) ([]string, error) {
	return f.deleteKeys, f.deleteErr
}
func (f *fakeRepo) IncrementView(context.Context, string) (int, error) { return 0, nil }

// fakeStorage records deleted keys (uploads are unused in these tests).
type fakeStorage struct {
	deleted             []string
	failThumbnailUpload bool
}

func (f *fakeStorage) Upload(_ context.Context, key, _ string, _ io.Reader) (string, error) {
	if f.failThumbnailUpload && strings.Contains(key, "/thumbnails/") {
		return "", errors.New("thumbnail upload failed")
	}
	return key, nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeStorage) PresignUpload(key string, _ int) string {
	return "https://r2.example.com/" + key + "?presigned=1"
}

func ratingPtr(v int16) *int16   { return &v }
func stringPtr(v string) *string { return &v }

// testImages is a minimal valid Images slice for tests that don't care about images.
var testImages = []ImageKeys{{ObjectKey: "checkpoints/test/abc12345.jpg", Type: MediaTypeImage}}

func TestServiceCreate_AwardsCheckinXP(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo, nil, nil)

	res, err := svc.Create(context.Background(), "user1", Input{Name: "Cà phê", Lat: 10.8, Lng: 106.6, Images: testImages})
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

func TestServiceCreate_StoresClientAddress(t *testing.T) {
	repo := &fakeRepo{}
	addr := "  Phường Bến Nghé, Quận 1  "
	prov := "Thành phố Hồ Chí Minh"

	if _, err := NewService(repo, nil, nil).Create(
		context.Background(),
		"user1",
		Input{Name: "Cà phê", Lat: 10.8, Lng: 106.6, Address: &addr, Province: &prov, Images: testImages},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.lastCreate.Address == nil || *repo.lastCreate.Address != "Phường Bến Nghé, Quận 1" {
		t.Fatalf("Address = %v, want trimmed value", repo.lastCreate.Address)
	}
	if repo.lastCreate.Province == nil || *repo.lastCreate.Province != prov {
		t.Fatalf("Province = %v, want %q", repo.lastCreate.Province, prov)
	}
}

func TestServiceCreate_StoresClientProvinceCode(t *testing.T) {
	repo := &fakeRepo{}
	prov := "Thành phố Hồ Chí Minh"
	code := "VN-SG"

	if _, err := NewService(repo, nil, nil).Create(
		context.Background(),
		"user1",
		Input{Name: "Cà phê", Lat: 10.8, Lng: 106.6, Province: &prov, ProvinceCode: &code, Images: testImages},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.lastCreate.Province == nil || *repo.lastCreate.Province != prov {
		t.Fatalf("Province = %v, want %q", repo.lastCreate.Province, prov)
	}
	if repo.lastCreate.ProvinceCode == nil || *repo.lastCreate.ProvinceCode != code {
		t.Fatalf("ProvinceCode = %v, want %q", repo.lastCreate.ProvinceCode, code)
	}
}

func TestServiceCreate_HandlesBlankAddress(t *testing.T) {
	repo := &fakeRepo{}
	blank := "   "
	if _, err := NewService(repo, nil, nil).Create(
		context.Background(),
		"user1",
		Input{Name: "Cà phê", Lat: 10.8, Lng: 106.6, Address: &blank, Images: testImages},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastCreate.Address != nil {
		t.Fatalf("Address = %q, want nil for blank input", *repo.lastCreate.Address)
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
			_, err := NewService(repo, nil, nil).Create(context.Background(), "u", tt.in)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("expected ErrInvalidInput, got %v", err)
			}
			if repo.lastCreate != nil {
				t.Error("repo.Create should not be called on invalid input")
			}
		})
	}
}

func TestServiceCreate_AcceptsThreeImagesAndOneVideo(t *testing.T) {
	repo := &fakeRepo{}
	images := []ImageKeys{
		{ObjectKey: "checkpoints/test/a.jpg", Type: MediaTypeImage},
		{ObjectKey: "checkpoints/test/b.jpg", Type: MediaTypeImage},
		{ObjectKey: "checkpoints/test/c.jpg", Type: MediaTypeImage},
		{ObjectKey: "checkpoints/test/d.mp4", ThumbnailKey: stringPtr("checkpoints/test/thumbnails/d.jpg"), Type: MediaTypeVideo},
	}
	if _, err := NewService(repo, nil, nil).Create(
		context.Background(), "u", Input{Name: "Quán", Lat: 10, Lng: 106, Images: images}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.lastCreate.Images) != 4 {
		t.Fatalf("Images = %d, want 4", len(repo.lastCreate.Images))
	}
	if repo.lastCreate.Images[3].Type != MediaTypeVideo {
		t.Fatalf("video type = %q, want %q", repo.lastCreate.Images[3].Type, MediaTypeVideo)
	}
}

func TestServiceCreate_DefaultsMissingMediaTypeToImage(t *testing.T) {
	repo := &fakeRepo{}
	if _, err := NewService(repo, nil, nil).Create(
		context.Background(), "u", Input{Name: "Quán", Lat: 10, Lng: 106, Images: []ImageKeys{{ObjectKey: "checkpoints/test/a.jpg"}}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastCreate.Images[0].Type != MediaTypeImage {
		t.Fatalf("Type = %q, want %q", repo.lastCreate.Images[0].Type, MediaTypeImage)
	}
}

func TestServiceCreate_RejectsMediaLimitsAndTypes(t *testing.T) {
	tests := []struct {
		name   string
		images []ImageKeys
	}{
		{
			name: "four images",
			images: []ImageKeys{
				{ObjectKey: "checkpoints/test/a.jpg"},
				{ObjectKey: "checkpoints/test/b.jpg"},
				{ObjectKey: "checkpoints/test/c.jpg"},
				{ObjectKey: "checkpoints/test/d.jpg"},
			},
		},
		{
			name: "two videos",
			images: []ImageKeys{
				{ObjectKey: "checkpoints/test/a.mp4", Type: MediaTypeVideo},
				{ObjectKey: "checkpoints/test/b.mp4", Type: MediaTypeVideo},
			},
		},
		{
			name:   "unknown type",
			images: []ImageKeys{{ObjectKey: "checkpoints/test/a.jpg", Type: "gif"}},
		},
		{
			name:   "video key must be mp4",
			images: []ImageKeys{{ObjectKey: "checkpoints/test/a.jpg", Type: MediaTypeVideo}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			_, err := NewService(repo, nil, nil).Create(
				context.Background(), "u", Input{Name: "Quán", Lat: 10, Lng: 106, Images: tt.images})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if repo.lastCreate != nil {
				t.Fatal("repo.Create should not be called")
			}
		})
	}
}

func TestServicePrepareSlots_MediaExtensions(t *testing.T) {
	svc := NewService(nil, &fakeStorage{}, nil)
	slots, err := svc.PrepareSlots(context.Background(), "u", 2, []string{MediaTypeImage, MediaTypeVideo})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(slots[0].ObjectKey, ".jpg") {
		t.Fatalf("image key = %q, want .jpg", slots[0].ObjectKey)
	}
	if !strings.HasSuffix(slots[1].ObjectKey, ".mp4") {
		t.Fatalf("video key = %q, want .mp4", slots[1].ObjectKey)
	}
	if !strings.HasSuffix(slots[1].ThumbnailKey, ".jpg") {
		t.Fatalf("thumbnail key = %q, want .jpg", slots[1].ThumbnailKey)
	}
}

func TestServiceCreate_ForwardsPlaceID(t *testing.T) {
	repo := &fakeRepo{}
	pid := "place-123"
	_, err := NewService(repo, nil, nil).Create(
		context.Background(), "u1", Input{Name: "Quán", Lat: 10, Lng: 106, PlaceID: &pid, Images: testImages})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastCreate.PlaceID == nil || *repo.lastCreate.PlaceID != pid {
		t.Errorf("PlaceID not forwarded: %v", repo.lastCreate.PlaceID)
	}
}

type fakeJourneys struct{ owns bool }

func (f *fakeJourneys) BelongsToUser(context.Context, string, string) (bool, error) {
	return f.owns, nil
}

func TestServiceCreate_AttachesOwnedJourney(t *testing.T) {
	repo := &fakeRepo{}
	jid := "journey-1"
	_, err := NewService(repo, nil, &fakeJourneys{owns: true}).Create(
		context.Background(), "u1", Input{Name: "Quán", Lat: 10, Lng: 106, JourneyID: &jid, Images: testImages})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastCreate.JourneyID == nil || *repo.lastCreate.JourneyID != jid {
		t.Errorf("JourneyID = %v, want %q", repo.lastCreate.JourneyID, jid)
	}
}

func TestServiceCreate_RejectsForeignJourney(t *testing.T) {
	repo := &fakeRepo{}
	jid := "someone-elses-journey"
	_, err := NewService(repo, nil, &fakeJourneys{owns: false}).Create(
		context.Background(), "u1", Input{Name: "Quán", Lat: 10, Lng: 106, JourneyID: &jid, Images: testImages})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.lastCreate.JourneyID != nil {
		t.Errorf("JourneyID = %v, want nil (not the user's journey)", repo.lastCreate.JourneyID)
	}
}

func TestServiceDelete_CleansUpStorage(t *testing.T) {
	repo := &fakeRepo{deleteKeys: []string{
		"checkpoints/cp1/a.jpg",
		"https://cdn.example.com/legacy-full-url.jpg", // legacy row — must be skipped
		"checkpoints/cp1/b.jpg",
	}}
	storage := &fakeStorage{}
	svc := NewService(repo, storage, nil)

	if err := svc.Delete(context.Background(), "cp1", "user1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"checkpoints/cp1/a.jpg", "checkpoints/cp1/b.jpg"}
	if !slices.Equal(storage.deleted, want) {
		t.Errorf("deleted keys = %v, want %v", storage.deleted, want)
	}
}

func TestServiceAddImages_ThumbnailFailureKeepsOriginal(t *testing.T) {
	repo := &fakeRepo{checkpoint: &Checkpoint{ID: "cp1", UserID: "user1"}}
	storage := &fakeStorage{failThumbnailUpload: true}
	svc := NewService(repo, storage, nil)

	_, err := svc.AddImages(context.Background(), "cp1", "user1", []ImageUpload{{
		ContentType:          "image/jpeg",
		Body:                 strings.NewReader("original"),
		ThumbnailContentType: "image/jpeg",
		ThumbnailBody:        strings.NewReader("thumbnail"),
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.addedKeys) != 1 || repo.addedKeys[0].ObjectKey == "" {
		t.Fatalf("added keys = %#v, want one original", repo.addedKeys)
	}
	if repo.addedKeys[0].ThumbnailKey != nil {
		t.Fatalf("thumbnail key = %v, want nil after best-effort failure", repo.addedKeys[0].ThumbnailKey)
	}
}

func TestServiceDelete_NotFoundPropagates(t *testing.T) {
	repo := &fakeRepo{deleteErr: ErrNotFound}
	storage := &fakeStorage{}
	svc := NewService(repo, storage, nil)

	if err := svc.Delete(context.Background(), "cp1", "user1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(storage.deleted) != 0 {
		t.Errorf("storage.deleted = %v, want empty (no cleanup on failure)", storage.deleted)
	}
}
