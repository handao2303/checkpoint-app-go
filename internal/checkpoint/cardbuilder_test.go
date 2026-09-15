package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

type fakeCardRepo struct {
	pending []PendingOGCard
	saved   map[string]OGCard
}

func (f *fakeCardRepo) OGCardsPending(_ context.Context, afterID string, limit int) ([]PendingOGCard, error) {
	var out []PendingOGCard
	for _, p := range f.pending {
		if p.ImageID <= afterID {
			continue
		}
		if _, done := f.saved[p.ImageID]; done {
			continue
		}
		out = append(out, p)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeCardRepo) OGCardsPendingForCheckpoint(context.Context, string) ([]PendingOGCard, error) {
	return f.pending, nil
}

func (f *fakeCardRepo) SetOGCard(_ context.Context, imageID, key string, w, h int) error {
	f.saved[imageID] = OGCard{Key: key, Width: w, Height: h}
	return nil
}

// cardStore serves originals of a given size and captures uploads.
type cardStore struct {
	t       *testing.T
	sources map[string][2]int
	uploads map[string][]byte
}

func (c *cardStore) GetObject(_ context.Context, key string) (io.ReadCloser, string, error) {
	wh, ok := c.sources[key]
	if !ok {
		return nil, "", errors.New("missing object " + key)
	}
	return newReadCloser(encodeJPEG(c.t, wh[0], wh[1])), "image/jpeg", nil
}

func (c *cardStore) Upload(_ context.Context, key, _ string, body io.Reader) (string, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	c.uploads[key] = b
	return key, nil
}

func newBuilder(t *testing.T, sources map[string][2]int, pending []PendingOGCard) (*CardBuilder, *fakeCardRepo, *cardStore) {
	repo := &fakeCardRepo{pending: pending, saved: map[string]OGCard{}}
	store := &cardStore{t: t, sources: sources, uploads: map[string][]byte{}}
	return NewCardBuilder(repo, store), repo, store
}

func TestBackfillCardsRendersUploadsAndRecords(t *testing.T) {
	b, repo, store := newBuilder(t,
		map[string][2]int{"checkpoints/x/a.jpg": {1440, 2560}, "checkpoints/x/b.jpg": {4032, 3024}},
		[]PendingOGCard{
			{ImageID: "i1", ObjectKey: "checkpoints/x/a.jpg"},
			{ImageID: "i2", ObjectKey: "checkpoints/x/b.jpg"},
		})

	stats, err := b.BackfillCards(context.Background(), 1, 0, nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if stats.Built != 2 || stats.Failed != 0 || stats.Skipped != 0 {
		t.Errorf("got %+v, want built=2", stats)
	}
	if len(store.uploads) != 2 {
		t.Errorf("uploaded %d card(s), want 2", len(store.uploads))
	}
	for id, card := range repo.saved {
		if card.Width != OGCardWidth || card.Height != OGCardHeight {
			t.Errorf("%s recorded %dx%d", id, card.Width, card.Height)
		}
		if _, uploaded := store.uploads[card.Key]; !uploaded {
			t.Errorf("%s recorded key %q that was never uploaded", id, card.Key)
		}
	}
}

// A too-small original is an expected outcome, not a failure, and must not stop
// the run or leave a recorded card behind.
func TestBackfillCardsCountsSmallSourcesAsSkipped(t *testing.T) {
	b, repo, store := newBuilder(t,
		map[string][2]int{
			"checkpoints/x/small.jpg": {390, 520},
			"checkpoints/x/big.jpg":   {1920, 2560},
		},
		[]PendingOGCard{
			{ImageID: "i1", ObjectKey: "checkpoints/x/small.jpg"},
			{ImageID: "i2", ObjectKey: "checkpoints/x/big.jpg"},
		})

	stats, err := b.BackfillCards(context.Background(), 10, 0, nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if stats.Skipped != 1 || stats.Built != 1 || stats.Failed != 0 {
		t.Errorf("got %+v, want skipped=1 built=1 failed=0", stats)
	}
	if _, saved := repo.saved["i1"]; saved {
		t.Error("recorded a card for the too-small original")
	}
	if len(store.uploads) != 1 {
		t.Errorf("uploaded %d card(s), want 1", len(store.uploads))
	}
}

func TestBackfillCardsSurvivesMissingOriginal(t *testing.T) {
	b, _, _ := newBuilder(t,
		map[string][2]int{"checkpoints/x/b.jpg": {1920, 2560}},
		[]PendingOGCard{
			{ImageID: "i1", ObjectKey: "checkpoints/x/gone.jpg"},
			{ImageID: "i2", ObjectKey: "checkpoints/x/b.jpg"},
		})

	stats, err := b.BackfillCards(context.Background(), 10, 0, nil)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if stats.Failed != 1 || stats.Built != 1 {
		t.Errorf("got %+v, want failed=1 built=1", stats)
	}
}

func TestEnsureForCheckpointReturnsBuiltCards(t *testing.T) {
	b, _, store := newBuilder(t,
		map[string][2]int{"checkpoints/x/a.jpg": {1440, 2560}},
		[]PendingOGCard{{ImageID: "i1", ObjectKey: "checkpoints/x/a.jpg"}})

	cards, err := b.EnsureForCheckpoint(context.Background(), "cp-1")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	card, ok := cards["i1"]
	if !ok {
		t.Fatal("no card returned for i1")
	}
	if card.Width != OGCardWidth || card.Height != OGCardHeight {
		t.Errorf("card is %dx%d", card.Width, card.Height)
	}
	raw, uploaded := store.uploads[card.Key]
	if !uploaded {
		t.Fatal("card key was not uploaded")
	}
	if !bytes.HasPrefix(raw, []byte{0xFF, 0xD8}) {
		t.Error("uploaded bytes are not a JPEG")
	}
}
