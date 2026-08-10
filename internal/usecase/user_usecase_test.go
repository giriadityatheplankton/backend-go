package usecase_test

import (
	"context"
	"testing"

	"backend-go/internal/domain"
	"backend-go/internal/usecase"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockUserRepository is a mock implementation of domain.UserRepository.
type MockUserRepository struct {
	mock.Mock
}

func (m *MockUserRepository) GetByID(ctx context.Context, id int) (*domain.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

// MockEventPublisher is a mock implementation of domain.EventPublisher.
type MockEventPublisher struct {
	mock.Mock
}

func (m *MockEventPublisher) PublishUserAccessed(ctx context.Context, event domain.UserAccessedEvent) error {
	args := m.Called(ctx, event)
	return args.Error(0)
}

func TestGetUser(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		userID        int
		mockSetup     func(mockRepo *MockUserRepository, mockPublisher *MockEventPublisher)
		expectedUser  *domain.User
		expectedErr   error
		expectEvent   bool
	}{
		{
			name:   "Success",
			userID: 1,
			mockSetup: func(mockRepo *MockUserRepository, mockPublisher *MockEventPublisher) {
				mockRepo.On("GetByID", mock.Anything, 1).Return(&domain.User{
					ID:    1,
					Name:  "Test User",
					Email: "test@example.com",
				}, nil)
			},
			expectedUser: &domain.User{
				ID:    1,
				Name:  "Test User",
				Email: "test@example.com",
			},
			expectedErr: nil,
		},
		{
			name:   "Invalid ID",
			userID: 0,
			mockSetup: func(mockRepo *MockUserRepository, mockPublisher *MockEventPublisher) {
				// No calls expected on mockRepo
			},
			expectedUser: nil,
			expectedErr:  domain.ErrInvalidUserID,
		},
		{
			name:   "User Not Found",
			userID: 999,
			mockSetup: func(mockRepo *MockUserRepository, mockPublisher *MockEventPublisher) {
				mockRepo.On("GetByID", mock.Anything, 999).Return(nil, domain.ErrUserNotFound)
			},
			expectedUser: nil,
			expectedErr:  domain.ErrUserNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockUserRepository)
			mockPublisher := new(MockEventPublisher)
			tt.mockSetup(mockRepo, mockPublisher)

			uc := usecase.NewUserUsecase(mockRepo, mockPublisher)
			user, err := uc.GetUser(ctx, tt.userID)

			if tt.expectedErr != nil {
				assert.ErrorIs(t, err, tt.expectedErr)
				assert.Nil(t, user)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedUser, user)
			}

			mockRepo.AssertExpectations(t)
		})
	}
}
