// Package apiclient is the outward-facing client for the order API.
package apiclient

import (
	"context"
	"errors"
	"net/http"
)

// Client talks to an orderd instance.
type Client struct {
	base string
	http *http.Client
}

// New returns a client pointed at base.
func New(base string) *Client {
	return &Client{base: base, http: http.DefaultClient}
}

// CreateOrder posts a new order.
func (c *Client) CreateOrder(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("apiclient: id is required")
	}
	return c.post(ctx, "/api/v1/orders", id)
}

// post is the unexported transport helper every verb funnels through.
func (c *Client) post(_ context.Context, _ string, _ string) error {
	return nil
}
