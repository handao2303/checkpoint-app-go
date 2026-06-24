package reaction

import "context"

// Service holds the reaction business logic.
type Service struct {
	repo *Repository
}

// NewService builds a reaction service.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// Set validates the reaction key, records the caller's reaction, and returns the
// updated summary. Returns ErrInvalidReaction for an unknown key.
func (s *Service) Set(ctx context.Context, checkpointID, userID, reaction string) (*Summary, error) {
	if !AllowedReactions[reaction] {
		return nil, ErrInvalidReaction
	}
	if err := s.repo.Set(ctx, checkpointID, userID, reaction); err != nil {
		return nil, err
	}
	if err := s.repo.AwardSocialScoutIfEligible(ctx, checkpointID, userID); err != nil {
		return nil, err
	}
	return s.repo.Summary(ctx, checkpointID, userID)
}

// Remove clears the caller's reaction and returns the updated summary.
func (s *Service) Remove(ctx context.Context, checkpointID, userID string) (*Summary, error) {
	if err := s.repo.Remove(ctx, checkpointID, userID); err != nil {
		return nil, err
	}
	return s.repo.Summary(ctx, checkpointID, userID)
}

// Summary returns the reaction state for a checkpoint.
func (s *Service) Summary(ctx context.Context, checkpointID, userID string) (*Summary, error) {
	return s.repo.Summary(ctx, checkpointID, userID)
}
