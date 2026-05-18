package httproutes

import (
	"net/http"
)

// chi-like router stub so the file parses without external deps.
type router struct{}

func (r *router) Get(string, http.HandlerFunc)                       {}
func (r *router) Post(string, http.HandlerFunc)                      {}
func (r *router) Delete(string, http.HandlerFunc)                    {}
func (r *router) Patch(string, http.HandlerFunc)                     {}
func (r *router) Route(string, func(r *router))                      {}
func (r *router) Group(func(r *router))                              {}
func (r *router) With(...interface{}) *router                        { return r }
func (r *router) Handle(string, http.Handler)                        {}
func (r *router) HandleFunc(string, func(http.ResponseWriter, *http.Request)) {}

type echoRouter struct{}

func (e *echoRouter) GET(string, http.HandlerFunc)         {}
func (e *echoRouter) POST(string, http.HandlerFunc)        {}
func (e *echoRouter) Any(string, http.HandlerFunc)         {}
func (e *echoRouter) Group(string, ...interface{}) *echoRouter { return e }

type handler struct{}

func (h *handler) Login(w http.ResponseWriter, r *http.Request)    {}
func (h *handler) Logout(w http.ResponseWriter, r *http.Request)   {}
func (h *handler) Profile(w http.ResponseWriter, r *http.Request)  {}
func (h *handler) Admin(w http.ResponseWriter, r *http.Request)    {}
func (h *handler) Healthz(w http.ResponseWriter, r *http.Request)  {}
func (h *handler) Status(w http.ResponseWriter, r *http.Request)   {}

func mustAuth(http.HandlerFunc) http.HandlerFunc { return nil }

// RegisterChiRoutes registers a small handful of routes through chi's API.
func RegisterChiRoutes(r *router, h *handler) {
	// @atlas:contract auth.login.chi
	r.Post("/api/v1/auth/login", h.Login)
	r.Get("/api/v1/auth/profile", h.Profile)

	r.Route("/api/v1/admin", func(r *router) {
		r.Get("/users", h.Admin)
		r.With(mustAuth(h.Login)).Get("/sessions", h.Admin)
	})

	r.HandleFunc("POST /healthz", h.Healthz)
}

// RegisterEchoRoutes registers a couple of routes through echo's API.
func RegisterEchoRoutes(e *echoRouter, h *handler) {
	apiGroup := e.Group("/api")
	apiGroup.POST("/status", h.Status)
	e.GET("/", h.Healthz)
	e.Any("/legacy/*", h.Logout)
}
