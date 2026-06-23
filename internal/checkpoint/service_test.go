package checkpoint

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeRepo is a minimal in-memory stand-in for the repository interface.
type fakeRepo struct {
	lastCreate   *CreateParams
	createResult *CreateResult
	createErr    error
	checkpoint   *Checkpoint
	lastUpdate   *UpdateParams
	updateResult *Checkpoint
	updateErr    error
	listResult   []*Checkpoint
	listLimit    int
	listCursor   *CheckpointCursor
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
func (f *fakeRepo) GetDetail(context.Context, string) (*Detail, error) { return nil, ErrNotFound }
func (f *fakeRepo) Update(_ context.Context, _, _ string, p UpdateParams) (*Checkpoint, error) {
	f.lastUpdate = &p
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.updateResult != nil {
		return f.updateResult, nil
	}
	return &Checkpoint{ID: "cp1", Name: p.Name, Note: p.Note, Rating: p.Rating}, nil
}
func (f *fakeRepo) ListByUser(context.Context, string, string, int) ([]*Checkpoint, error) {
	return f.listResult, nil
}
func (f *fakeRepo) ListByUserPage(_ context.Context, _ string, _ string, _ int, cursor *CheckpointCursor) ([]*Checkpoint, error) {
	f.listCursor = cursor
	return f.listResult, nil
}
func (f *fakeRepo) ListUnassignedByUser(context.Context, string, int, *CheckpointCursor) ([]*Checkpoint, error) {
	return nil, nil
}
func (f *fakeRepo) ListGlobal(_ context.Context, limit int, _ BoundsFilter) ([]*Checkpoint, error) {
	f.listLimit = limit
	return f.listResult, nil
}
func (f *fakeRepo) ListInBounds(_ context.Context, _ float64, _ float64, _ float64, _ float64, limit int, _ BoundsFilter) ([]*Checkpoint, error) {
	f.listLimit = limit
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

func TestServiceUpdate_ValidatesAndNormalizesContent(t *testing.T) {
	repo := &fakeRepo{}
	note := "  Quán yên tĩnh  "
	rating := int16(5)

	cp, err := NewService(repo, nil, nil).Update(context.Background(), "cp1", "u1", UpdateInput{
		Name:   "  Cà phê chiều  ",
		Note:   &note,
		Rating: &rating,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cp.Name != "Cà phê chiều" {
		t.Fatalf("updated name = %q", cp.Name)
	}
	if repo.lastUpdate == nil {
		t.Fatal("repo.Update was not called")
	}
	if repo.lastUpdate.Name != "Cà phê chiều" {
		t.Fatalf("Name = %q, want trimmed", repo.lastUpdate.Name)
	}
	if repo.lastUpdate.Note == nil || *repo.lastUpdate.Note != "Quán yên tĩnh" {
		t.Fatalf("Note = %v, want trimmed note", repo.lastUpdate.Note)
	}
	if repo.lastUpdate.Rating == nil || *repo.lastUpdate.Rating != 5 {
		t.Fatalf("Rating = %v, want 5", repo.lastUpdate.Rating)
	}
}

func TestServiceUpdate_Validation(t *testing.T) {
	tests := []struct {
		name string
		in   UpdateInput
	}{
		{"empty name", UpdateInput{Name: "  "}},
		{"rating too low", UpdateInput{Name: "x", Rating: ratingPtr(0)}},
		{"rating too high", UpdateInput{Name: "x", Rating: ratingPtr(6)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			_, err := NewService(repo, nil, nil).Update(context.Background(), "cp1", "u1", tt.in)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if repo.lastUpdate != nil {
				t.Fatal("repo.Update should not be called on invalid input")
			}
		})
	}
}

func TestServiceListMine_PaginatesWithCursor(t *testing.T) {
	t1 := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(-time.Minute)
	t3 := t2.Add(-time.Minute)
	cursor := &CheckpointCursor{CreatedAt: t1.Format(time.RFC3339Nano), ID: "cp0"}
	repo := &fakeRepo{listResult: []*Checkpoint{
		{ID: "cp1", CreatedAt: t1},
		{ID: "cp2", CreatedAt: t2},
		{ID: "cp3", CreatedAt: t3},
	}}

	page, err := NewService(repo, nil, nil).ListMine(context.Background(), "u1", "", 2, cursor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.listCursor != cursor {
		t.Fatal("cursor was not passed to repo")
	}
	if len(page.Checkpoints) != 2 {
		t.Fatalf("checkpoints = %d, want 2", len(page.Checkpoints))
	}
	if page.NextCursor == nil || *page.NextCursor != t2.Format(time.RFC3339Nano)+"|cp2" {
		t.Fatalf("next cursor = %v, want cp2 cursor", page.NextCursor)
	}
}

func TestServiceListGlobal_ClampsLimit(t *testing.T) {
	repo := &fakeRepo{}
	if _, err := NewService(repo, nil, nil).ListGlobal(context.Background(), 9999, BoundsFilter{}); err != nil {
		t.Fatalf("ListGlobal() error = %v", err)
	}
	if repo.listLimit != maxMapLimit {
		t.Fatalf("limit = %d, want %d", repo.listLimit, maxMapLimit)
	}
}

func TestServiceListInBounds_RejectsOutOfRangeBBox(t *testing.T) {
	tests := []struct {
		name                           string
		minLng, minLat, maxLng, maxLat float64
	}{
		{"longitude below min", -181, -10, 10, 10},
		{"longitude above max", -10, -10, 181, 10},
		{"latitude below min", -10, -91, 10, 10},
		{"latitude above max", -10, -10, 10, 91},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}
			_, err := NewService(repo, nil, nil).ListInBounds(
				context.Background(),
				tt.minLng,
				tt.minLat,
				tt.maxLng,
				tt.maxLat,
				300,
				BoundsFilter{},
			)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if repo.listLimit != 0 {
				t.Fatal("repo.ListInBounds should not be called")
			}
		})
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

func TestServiceCreate_StoresTakenAt(t *testing.T) {
	repo := &fakeRepo{}
	takenAt := time.Date(2026, 6, 18, 9, 30, 0, 0, time.UTC)

	if _, err := NewService(repo, nil, nil).Create(
		context.Background(),
		"user1",
		Input{Name: "Cà phê", Lat: 10.8, Lng: 106.6, TakenAt: &takenAt, Images: testImages},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.lastCreate.TakenAt == nil || !repo.lastCreate.TakenAt.Equal(takenAt) {
		t.Fatalf("TakenAt = %v, want %v", repo.lastCreate.TakenAt, takenAt)
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
