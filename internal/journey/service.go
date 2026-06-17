package journey

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidInput is returned for failed validation.
var ErrInvalidInput = errors.New("invalid input")

// repository is the data layer the service depends on (consumer-side interface).
type repository interface {
	Create(ctx context.Context, p CreateParams) (*Journey, error)
	ListByUser(ctx context.Context, userID string) ([]*Journey, error)
	GetByID(ctx context.Context, id string) (*Journey, error)
	GetDetail(ctx context.Context, id string) (*Detail, error)
	Update(ctx context.Context, id, userID string, title, description *string) (*Journey, error)
	SetActive(ctx context.Context, id, userID string, active bool) (*Journey, error)
	Delete(ctx context.Context, id, userID string) error
	AssignCheckpoint(ctx context.Context, journeyID, checkpointID, userID string) error
	AssignCheckpoints(ctx context.Context, journeyID string, checkpointIDs []string, userID string) error
}

// Service holds the journey business logic.
type Service struct {
	repo repository
}

// NewService builds the journey service.
func NewService(repo repository) *Service {
	return &Service{repo: repo}
}

// Create validates and creates a journey.
func (s *Service) Create(ctx context.Context, userID, title string, description *string) (*Journey, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("%w: title is required", ErrInvalidInput)
	}
	return s.repo.Create(ctx, CreateParams{UserID: userID, Title: title, Description: trimPtr(description)})
}

// ListMine returns the caller's journeys.
func (s *Service) ListMine(ctx context.Context, userID string) ([]*Journey, error) {
	return s.repo.ListByUser(ctx, userID)
}

// GetDetail returns a journey with stats and stops.
func (s *Service) GetDetail(ctx context.Context, id string) (*Detail, error) {
	return s.repo.GetDetail(ctx, id)
}

// Update applies a partial update (title/description and/or active state).
func (s *Service) Update(ctx context.Context, id, userID string, in UpdateInput) (*Journey, error) {
	var j *Journey
	var err error

	if in.Title != nil || in.Description != nil {
		title := in.Title
		if title != nil {
			t := strings.TrimSpace(*title)
			if t == "" {
				return nil, fmt.Errorf("%w: title cannot be empty", ErrInvalidInput)
			}
			title = &t
		}
		if j, err = s.repo.Update(ctx, id, userID, title, trimPtr(in.Description)); err != nil {
			return nil, err
		}
	}
	if in.IsActive != nil {
		if j, err = s.repo.SetActive(ctx, id, userID, *in.IsActive); err != nil {
			return nil, err
		}
	}
	if j == nil {
		return s.repo.GetByID(ctx, id)
	}
	return j, nil
}

// Delete removes an owned journey.
func (s *Service) Delete(ctx context.Context, id, userID string) error {
	return s.repo.Delete(ctx, id, userID)
}

// AssignCheckpoint attaches a checkpoint to a journey (both owned by the caller).
func (s *Service) AssignCheckpoint(ctx context.Context, journeyID, checkpointID, userID string) error {
	return s.repo.AssignCheckpoint(ctx, journeyID, checkpointID, userID)
}

// AssignCheckpoints attaches caller-owned checkpoints to a journey in the
// submitted order.
func (s *Service) AssignCheckpoints(ctx context.Context, journeyID string, checkpointIDs []string, userID string) error {
	if len(checkpointIDs) == 0 {
		return fmt.Errorf("%w: checkpoint_ids is required", ErrInvalidInput)
	}
	seen := make(map[string]struct{}, len(checkpointIDs))
	normalized := make([]string, 0, len(checkpointIDs))
	for _, id := range checkpointIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("%w: checkpoint id is required", ErrInvalidInput)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: duplicate checkpoint id", ErrInvalidInput)
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return s.repo.AssignCheckpoints(ctx, journeyID, normalized, userID)
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}
