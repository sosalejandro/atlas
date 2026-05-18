package handlers

import "context"

// AuthHandler exposes login + register operations.
type AuthHandler struct{}

// NewAuthHandler constructs the handler.
func NewAuthHandler() *AuthHandler { return &AuthHandler{} }

// @atlas:contract auth.login
//
// Login authenticates a user via email + password.
func (h *AuthHandler) Login(ctx context.Context, email, pw string) error {
	return nil
}

// @testreg auth.register
//
// Register creates a new account.
func (h *AuthHandler) Register(ctx context.Context, email, pw string) error {
	return nil
}

// NoAnnotation is a plain func — no FeatureID expected.
func (h *AuthHandler) NoAnnotation(ctx context.Context) error { return nil }
