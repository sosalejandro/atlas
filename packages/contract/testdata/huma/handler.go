package huma

import (
	"context"
	"net/http"
)

// Local stubs so the fixture parses without external deps. The contract
// extractor only needs the file to be syntactically valid Go.
type API interface{}

type Operation struct {
	OperationID   string
	Method        string
	Path          string
	DefaultStatus int
	Summary       string
	Description   string
	Tags          []string
}

func Register[I, O any](api API, op Operation, handler func(context.Context, *I) (*O, error)) {}

type LoginInput struct {
	Body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
}
type LoginOutput struct {
	Body struct {
		Token string `json:"token"`
	}
}

type SubscriptionsInput struct {
	ID string `path:"subscriptionId"`
}
type SubscriptionsOutput struct {
	Body struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
}

type Handler struct{}

// Login is the Huma handler for POST /api/v1/auth/login.
func (h *Handler) Login(ctx context.Context, in *LoginInput) (*LoginOutput, error) {
	return &LoginOutput{}, nil
}

// GetSubscription is the Huma handler for GET /api/v1/platform/subscriptions/{id}.
func (h *Handler) GetSubscription(ctx context.Context, in *SubscriptionsInput) (*SubscriptionsOutput, error) {
	return &SubscriptionsOutput{}, nil
}

// Register installs the Huma operations on the provided API.
func (h *Handler) RegisterRoutes(api API) {
	// @atlas:contract platform.auth.login
	Register(api, Operation{
		OperationID:   "loginUser",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/login",
		DefaultStatus: http.StatusOK,
		Summary:       "Log a user in",
		Tags:          []string{"auth", "platform"},
	}, h.Login)

	// No annotation — should produce a contract with FeatureID == nil.
	Register(api, Operation{
		OperationID: "getPlatformSubscription",
		Method:      "GET",
		Path:        "/api/v1/platform/subscriptions/{subscriptionId}",
		Summary:     "Get a subscription by id",
		Tags:        []string{"platform", "subscriptions"},
	}, h.GetSubscription)
}
