package multirouter

import "net/http"

// Stubs to keep the file standalone-parseable.
type chiRouter struct{}

func (r *chiRouter) Get(string, http.HandlerFunc)     {}
func (r *chiRouter) Post(string, http.HandlerFunc)    {}
func (r *chiRouter) Route(string, func(r *chiRouter)) {}

type echoRouter struct{}

func (e *echoRouter) GET(string, http.HandlerFunc)         {}
func (e *echoRouter) POST(string, http.HandlerFunc)        {}
func (e *echoRouter) Group(string, ...interface{}) *echoRouter { return e }

type h struct{}

func (h *h) Login(w http.ResponseWriter, r *http.Request)  {}
func (h *h) Logout(w http.ResponseWriter, r *http.Request) {}
func (h *h) Health(w http.ResponseWriter, r *http.Request) {}

// MountBoth wires both a chi router AND an echo router from a single fn,
// exercising the multi-router-in-one-file edge case.
func MountBoth(c *chiRouter, e *echoRouter, hh *h) {
	c.Post("/api/v1/auth/login", hh.Login)
	c.Route("/api/v1/admin", func(c *chiRouter) {
		c.Get("/health", hh.Health)
	})

	api := e.Group("/api/v1")
	api.POST("/auth/logout", hh.Logout)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", hh.Health)
}
