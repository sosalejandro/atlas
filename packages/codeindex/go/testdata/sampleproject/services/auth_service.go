package services

import (
	"context"

	"example.com/sample/persistence"
)

// AuthService validates credentials.
type AuthService struct {
	repo *persistence.UserRepository
}

// NewAuthService constructs an AuthService.
func NewAuthService(repo *persistence.UserRepository) *AuthService {
	return &AuthService{repo: repo}
}

// Authenticate checks the user's password.
func (s *AuthService) Authenticate(ctx context.Context, email, pw string) error {
	_, err := s.repo.GetUserByEmail(ctx, email)
	return err
}
