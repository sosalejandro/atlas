package handlers

import (
	"context"

	"example.com/sample/services"
)

// AuthHandler is a fake HTTP handler.
type AuthHandler struct {
	svc *services.AuthService
}

// NewAuthHandler constructs an AuthHandler.
func NewAuthHandler(svc *services.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// @api POST /api/v1/auth/login
//
// Login handles login requests.
func (h *AuthHandler) Login(ctx context.Context, email, pw string) error {
	return h.svc.Authenticate(ctx, email, pw)
}
