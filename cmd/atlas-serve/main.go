// Command atlas-serve exposes an already-built atlas index over a local
// HTTP API, for the desktop shell, a browser dashboard or an editor plugin.
//
// IT IS A SEPARATE BINARY ON PURPOSE, and the reason is the first sentence of
// docs/security.md: "Nothing leaves your machine." That claim is enforced
// rather than promised -- packages/redact/egress_test.go walks the import
// graph of `cmd/atlas` and fails the build if any first-party package it
// reaches imports net, net/http, net/rpc or net/smtp.
//
// An API needs net/http. Putting it inside `atlas` would have meant weakening
// that guarantee for every user in order to serve the ones who want a GUI,
// and the guarantee is worth more than the convenience: it is the reason an
// auditor can accept atlas reading a proprietary tree at all.
//
// So the split is the answer. `atlas` keeps the absolute claim -- it cannot
// open a socket, full stop, and the test still proves it. `atlas-serve` is
// the one component that listens, it is auditable on its own, and it is
// bound by two rules of its own:
//
//   - LOOPBACK ONLY. httpapi.RequireLoopback refuses anything routable. The
//     API has no authentication and serves a complete map of the source
//     tree; on a routable address that is a disclosure, not a convenience.
//   - IT NEVER DIALS. TestServeBinary_NeverDialsOut asserts no first-party
//     package this binary reaches constructs an outbound connection, so
//     "nothing leaves the machine" survives verbatim for this binary too --
//     it accepts connections, it does not make them.
//
// It reads the index and never writes to it. Scanning stays in `atlas`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/sosalejandro/atlas/packages/httpapi"
	"github.com/sosalejandro/atlas/packages/store"
)

// Exit codes mirror internal/cli/exitcode.go, because a caller scripting the
// two binaries should not have to learn two contracts.
const (
	exitOK           = 0
	exitUsage        = 2
	exitUndetermined = 3
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var ue *usageError
		if errors.As(err, &ue) {
			os.Exit(exitUsage)
		}
		os.Exit(exitUndetermined)
	}
	os.Exit(exitOK)
}

type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func usagef(format string, a ...any) error {
	return &usageError{err: fmt.Errorf(format, a...)}
}

func run() error {
	var (
		dbPath  = flag.String("db-path", "", "path to atlas.db (default: .atlas/atlas.db under the working directory)")
		root    = flag.String("root", "", "working tree the index describes (default: the working directory)")
		host    = flag.String("host", "127.0.0.1", "loopback address to bind; anything routable is refused")
		port    = flag.Int("port", 7777, "port to bind; 0 asks the OS for a free one and prints it")
		stable  = flag.Bool("stable", false, "drop timestamps, durations and absolute paths so two requests over an unchanged index return identical bytes")
		openAPI = flag.Bool("openapi", false, "print the generated OpenAPI 3.1 document and exit")
	)
	flag.Parse()

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("atlas-serve: working directory: %w", err)
	}
	if *root == "" {
		*root = wd
	}
	if *dbPath == "" {
		*dbPath = filepath.Join(wd, ".atlas", "atlas.db")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s, err := store.Open(ctx, *dbPath)
	if err != nil {
		// Could not look, rather than looked and found nothing.
		return fmt.Errorf("atlas-serve: open %s: %w (run `atlas init` first)", *dbPath, err)
	}
	defer func() { _ = s.Close() }()

	srv, err := httpapi.New(httpapi.Options{
		Store: s, Root: *root, DBPath: *dbPath, Stable: *stable,
	})
	if err != nil {
		return fmt.Errorf("atlas-serve: %w", err)
	}

	// --openapi before binding anything: generating the contract is a build
	// step for consumers, and it must not require a listening socket.
	if *openAPI {
		doc, derr := srv.OpenAPI()
		if derr != nil {
			return derr
		}
		if _, werr := os.Stdout.Write(doc); werr != nil {
			return fmt.Errorf("atlas-serve: write openapi: %w", werr)
		}
		return nil
	}

	addr := net.JoinHostPort(*host, fmt.Sprintf("%d", *port))
	if err := httpapi.RequireLoopback(addr); err != nil {
		return usagef("atlas-serve: %w", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("atlas-serve: listen %s: %w", addr, err)
	}
	defer func() { _ = ln.Close() }()

	// Printed before Serve blocks, and to stdout, because --port 0 means the
	// caller cannot learn the address any other way.
	fmt.Printf("atlas-serve — http://%s/api/  (db: %s)\n", httpapi.Addr(ln), *dbPath)
	fmt.Println("  loopback only; ctrl-c to stop")

	return srv.Serve(ctx, ln)
}
