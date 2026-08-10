package usecase

import (
	"context"
	"log/slog"
	"time"

	"backend-go/internal/domain"
)

type userUsecase struct {
	repo           domain.UserRepository
	eventPublisher domain.EventPublisher
}

// NewUserUsecase creates a new instance of UserUsecase with optional event publisher injection.
func NewUserUsecase(repo domain.UserRepository, eventPublisher domain.EventPublisher) domain.UserUsecase {
	return &userUsecase{
		repo:           repo,
		eventPublisher: eventPublisher,
	}
}

// GetUser retrieves user data by ID after performing business validation.
func (u *userUsecase) GetUser(ctx context.Context, id int) (*domain.User, error) {
	if id <= 0 {
		return nil, domain.ErrInvalidUserID
	}

	user, err := u.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Publish domain event asynchronously / non-blocking
	if u.eventPublisher != nil {
		event := domain.UserAccessedEvent{
			UserID:     user.ID,
			UserName:   user.Name,
			AccessedAt: time.Now(),
			Source:     "database_or_cache",
		}
		go func(evt domain.UserAccessedEvent) {
			// Create a detached background context for async event publishing
			asyncCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			if pubErr := u.eventPublisher.PublishUserAccessed(asyncCtx, evt); pubErr != nil {
				slog.Error("Failed to publish user accessed event in background", "user_id", evt.UserID, "error", pubErr)
			}
		}(event)
	}

	return user, nil
}
