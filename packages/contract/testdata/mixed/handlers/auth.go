package handlers

import (
	"context"
	"net/http"
)

// LoginInput is the request body for POST /api/v1/auth/login.
type LoginInput struct {
	Body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
}

// LoginOutput is the response shape.
type LoginOutput struct {
	Body struct {
		Token string `json:"token"`
	}
}

// AuthHandler hosts the auth endpoints.
type AuthHandler struct{}

// NewAuthHandler constructs an AuthHandler.
func NewAuthHandler() *AuthHandler { return &AuthHandler{} }

// @atlas:contract auth.login
//
// Login is the Huma handler for POST /api/v1/auth/login.
func (h *AuthHandler) Login(ctx context.Context, in *LoginInput) (*LoginOutput, error) {
	return &LoginOutput{}, nil
}

// Stub helpers — keep them in the symbol set so the merge pass has at
// least one un-merged KindFunc to keep around.
func (h *AuthHandler) computeToken(email string) string { return "" }

// Use http to silence the imports check.
var _ = http.MethodPost
