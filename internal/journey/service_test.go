package journey

import (
	"context"
	"errors"
	"testing"
)

type fakeRepo struct {
	assignedJourneyID string
	assignedUserID    string
	assignedIDs       []string
	assignErr         error
}

func (f *fakeRepo) Create(context.Context, CreateParams) (*Journey, error) {
	return nil, nil
}
func (f *fakeRepo) ListByUser(context.Context, string) ([]*Journey, error) {
	return nil, nil
}
func (f *fakeRepo) GetByID(context.Context, string) (*Journey, error) {
	return nil, nil
}
func (f *fakeRepo) GetDetail(context.Context, string) (*Detail, error) {
	return nil, nil
}
func (f *fakeRepo) Update(context.Context, string, string, *string, *string) (*Journey, error) {
	return nil, nil
}
func (f *fakeRepo) SetActive(context.Context, string, string, bool) (*Journey, error) {
	return nil, nil
}
func (f *fakeRepo) Delete(context.Context, string, string) error {
	return nil
}
func (f *fakeRepo) AssignCheckpoint(context.Context, string, string, string) error {
	return nil
}
func (f *fakeRepo) AssignCheckpoints(_ context.Context, journeyID string, checkpointIDs []string, userID string) error {
	f.assignedJourneyID = journeyID
	f.assignedUserID = userID
	f.assignedIDs = checkpointIDs
	return f.assignErr
}

func TestServiceAssignCheckpointsPreservesOrder(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)

	err := svc.AssignCheckpoints(context.Background(), "journey-1", []string{"cp-2", "cp-1", "cp-3"}, "user-1")
	if err != nil {
		t.Fatalf("AssignCheckpoints error = %v", err)
	}
	if repo.assignedJourneyID != "journey-1" || repo.assignedUserID != "user-1" {
		t.Fatalf("assigned journey/user = %q/%q", repo.assignedJourneyID, repo.assignedUserID)
	}
	want := []string{"cp-2", "cp-1", "cp-3"}
	for i := range want {
		if repo.assignedIDs[i] != want[i] {
			t.Fatalf("assignedIDs[%d] = %q, want %q", i, repo.assignedIDs[i], want[i])
		}
	}
}

func TestServiceAssignCheckpointsRejectsEmpty(t *testing.T) {
	err := NewService(&fakeRepo{}).AssignCheckpoints(context.Background(), "journey-1", nil, "user-1")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("AssignCheckpoints error = %v, want ErrInvalidInput", err)
	}
}

func TestServiceAssignCheckpointsRejectsDuplicate(t *testing.T) {
	err := NewService(&fakeRepo{}).AssignCheckpoints(context.Background(), "journey-1", []string{"cp-1", "cp-1"}, "user-1")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("AssignCheckpoints error = %v, want ErrInvalidInput", err)
	}
}
