package persistence

import "context"

// User is a fake user model.
type User struct {
	Email string
}

// UserRepository fetches users from the database.
type UserRepository struct{}

// NewUserRepository constructs a UserRepository.
func NewUserRepository() *UserRepository {
	return &UserRepository{}
}

// GetUserByEmail retrieves a user by their email.
func (r *UserRepository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return &User{Email: email}, nil
}
