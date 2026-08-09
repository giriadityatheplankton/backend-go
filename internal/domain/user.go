package domain

// User represents the user data model.
type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// UserRepository defines the data access contract for User.
type UserRepository interface {
	GetByID(id int) (*User, error)
}

// UserUsecase defines the business logic contract for User.
type UserUsecase interface {
	GetUser(id int) (*User, error)
}
