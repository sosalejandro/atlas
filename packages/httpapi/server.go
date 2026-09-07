// Package httpapi serves atlas's read model over HTTP for local consumers --
// the desktop shell (#102), a browser dashboard, an editor plugin.
//
// Two rules shape everything here, and both are reactions to the surface this
// replaces.
//
// ONE IMPLEMENTATION OF EVERY RULE. Handlers call the same packages the CLI
// calls and answer in the same packages/envelope. The testreg-era dashboard
// in internal/server/ did the opposite: it read registry YAML through its own
// use cases and never touched packages/store, so it computed different
// answers with different code and drifted until it could not see features,
// statement coverage, edges or snapshots at all. Three thousand lines that
// still compile and cannot tell you anything true. Nothing here is allowed to
// recompute what a package already decides.
//
// LOOPBACK ONLY. This is a local tool with no authentication, serving a
// complete map of somebody's source code -- symbol names, file paths, test
// gaps. Binding it to a routable address would publish that to the network,
// so Listen refuses any host that is not loopback rather than trusting the
// caller to pass the right flag.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// Options configures a Server.
type Options struct {
	// Store is the open state database. Required.
	Store *store.Store
	// Root is the working tree the index describes, for the checks that
	// compare the two.
	Root string
	// DBPath is reported by /api/meta so a consumer can tell which index it
	// is looking at.
	DBPath string
	// Stable drops timestamps, durations and absolute paths from every
	// response, so two requests over an unchanged index return identical
	// bytes. Same contract as the CLI's --stable.
	Stable bool
	// Now supplies the envelope timestamp. Injected so tests can pin it;
	// nil means time.Now.
	Now func() time.Time
	// Logger receives request lines. Nil is a no-op.
	Logger shared.Logger
}

// Server is the HTTP surface. Build it with New and hand it to Listen.
type Server struct {
	opts Options
	mux  *http.ServeMux
	api  huma.API
}

// New builds the server and registers every route.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("httpapi: Store is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = shared.NopLogger{}
	}
	s := &Server{opts: opts, mux: http.NewServeMux()}
	cfg := huma.DefaultConfig(APITitle, APIVersion)
	// The generated document describes a loopback service, and saying so in
	// the spec keeps the constraint with the contract rather than only in
	// prose somebody has to find.
	cfg.Info.Description = "Atlas's local read model. Loopback only: this API has " +
		"no authentication and serves a complete map of the source tree it indexed."
	// huma's DefaultConfig installs a CreateHook that adds a schema-link
	// transformer, which puts a `$schema` field in every response body.
	// Dropped, because the point of this package is that the HTTP surface
	// answers in the SAME envelope as `atlas --json`, and an extra field
	// only one of them carries is the drift this exists to prevent, in
	// miniature. The contract is still published -- at /openapi and via
	// `atlas-serve --openapi` -- which is where a schema belongs.
	cfg.CreateHooks = nil
	cfg.Transformers = nil
	s.api = humago.New(s.mux, cfg)
	s.register(s.api)
	return s, nil
}

// Handler exposes the router, for httptest and for embedding.
func (s *Server) Handler() http.Handler { return s.mux }

// OpenAPI returns the generated contract. It is derived from the handler
// types rather than maintained beside them, which is the whole reason for
// huma being here -- a hand-written spec drifts from the code it describes,
// and atlas is a tool about exactly that failure.
func (s *Server) OpenAPI() ([]byte, error) {
	b, err := s.api.OpenAPI().YAML()
	if err != nil {
		return nil, fmt.Errorf("httpapi: render openapi: %w", err)
	}
	return b, nil
}

// ErrNotLoopback is returned by Listen for any address that is not loopback.
var ErrNotLoopback = errors.New("httpapi: refusing to bind a non-loopback address")

// Listen binds addr and serves until ctx is cancelled.
//
// It refuses anything but loopback. The API has no authentication and serves
// a complete map of the user's source; "bind 0.0.0.0 for convenience" is a
// disclosure, not a convenience, and the check is here rather than in the
// caller so every caller inherits it.
func (s *Server) Listen(ctx context.Context, addr string) error {
	if err := RequireLoopback(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("httpapi: listen %s: %w", addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve runs on an existing listener until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("httpapi: serve: %w", err)
	}
}

// Addr returns the address a listener actually bound, which matters because
// --port 0 asks the OS to choose one.
func Addr(ln net.Listener) string { return ln.Addr().String() }

// RequireLoopback reports whether addr names a loopback interface.
//
// An empty host ("" or ":8080") means "every interface" to net.Listen, so it
// is rejected too -- that is the exact spelling somebody reaches for when
// they want it reachable from a container or another machine.
func RequireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("httpapi: parse address %q: %w", addr, err)
	}
	switch host {
	case "localhost":
		return nil
	case "":
		return fmt.Errorf("%w: %q binds every interface; use 127.0.0.1", ErrNotLoopback, addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q is not an IP literal or localhost", ErrNotLoopback, addr)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("%w: %q is routable; the API has no authentication and "+
			"serves a complete map of this source tree", ErrNotLoopback, addr)
	}
	return nil
}

// timeLayout is RFC3339, the same stamp the CLI envelope uses.
const timeLayout = time.RFC3339
