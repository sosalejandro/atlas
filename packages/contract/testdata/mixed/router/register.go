package router

import (
	"context"
	"net/http"

	"example.com/mixed/handlers"
)

type API interface{}

type Operation struct {
	OperationID   string
	Method        string
	Path          string
	DefaultStatus int
	Summary       string
	Tags          []string
}

func Register[I, O any](api API, op Operation, h func(context.Context, *I) (*O, error)) {}

// Install wires the Huma operations.
func Install(api API, h *handlers.AuthHandler) {
	Register(api, Operation{
		OperationID:   "loginUser",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/login",
		DefaultStatus: http.StatusOK,
		Summary:       "Log a user in",
		Tags:          []string{"auth"},
	}, h.Login)
}
