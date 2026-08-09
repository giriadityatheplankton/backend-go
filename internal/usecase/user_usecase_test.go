package usecase_test

import (
	"errors"
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

func (m *MockUserRepository) GetByID(id int) (*domain.User, error) {
	args := m.Called(id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func TestGetUser_Success(t *testing.T) {
	mockRepo := new(MockUserRepository)
	expectedUser := &domain.User{
		ID:    1,
		Name:  "Test User",
		Email: "test@example.com",
	}

	mockRepo.On("GetByID", 1).Return(expectedUser, nil)

	uc := usecase.NewUserUsecase(mockRepo)
	user, err := uc.GetUser(1)

	assert.NoError(t, err)
	assert.NotNil(t, user)
	assert.Equal(t, expectedUser.ID, user.ID)
	assert.Equal(t, expectedUser.Name, user.Name)
	assert.Equal(t, expectedUser.Email, user.Email)
	mockRepo.AssertExpectations(t)
}

func TestGetUser_InvalidID(t *testing.T) {
	mockRepo := new(MockUserRepository)

	uc := usecase.NewUserUsecase(mockRepo)
	user, err := uc.GetUser(0)

	assert.Error(t, err)
	assert.Nil(t, user)
	assert.Equal(t, "invalid user ID", err.Error())
	mockRepo.AssertNotCalled(t, "GetByID", mock.Anything)
}

func TestGetUser_NotFound(t *testing.T) {
	mockRepo := new(MockUserRepository)
	mockRepo.On("GetByID", 999).Return(nil, errors.New("user not found"))

	uc := usecase.NewUserUsecase(mockRepo)
	user, err := uc.GetUser(999)

	assert.Error(t, err)
	assert.Nil(t, user)
	assert.Equal(t, "user not found", err.Error())
	mockRepo.AssertExpectations(t)
}
