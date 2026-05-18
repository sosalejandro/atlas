package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/sosalejandro/atlas/internal/server"
	"github.com/spf13/cobra"
)

var (
	servePort        int
	serveProjectName string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Launch the testreg web console",
	Long: `Starts an HTTP server with a browser-based dashboard for exploring
coverage data, contract traces, sprint priorities, and diagnostic tools.

The web console uses htmx for partial page updates and reflects the same
data as the CLI commands — reading from the registry YAML files and
running the same use cases under the hood.

Example:
  testreg serve
  testreg serve --port 8090 --name "my-project"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if serveProjectName == "" {
			// Default to the base name of the project root directory.
			if resolvedProjectRoot() != "" && resolvedProjectRoot() != "." {
				serveProjectName = resolvedProjectRoot()
				// Trim to just the last path component.
				for i := len(serveProjectName) - 1; i >= 0; i-- {
					if serveProjectName[i] == '/' || serveProjectName[i] == '\\' {
						serveProjectName = serveProjectName[i+1:]
						break
					}
				}
			}
		}
		if serveProjectName == "" {
			serveProjectName = "project"
		}

		srv, err := server.New(resolvedRegistryDir(), resolvedProjectRoot(), serveProjectName)
		if err != nil {
			return fmt.Errorf("initializing server: %w", err)
		}

		addr := fmt.Sprintf(":%d", servePort)
		fmt.Fprintf(os.Stdout, "testreg web console\n")
		fmt.Fprintf(os.Stdout, "  → http://localhost%s\n", addr)
		fmt.Fprintf(os.Stdout, "  Project: %s\n", serveProjectName)
		fmt.Fprintf(os.Stdout, "  Registry: %s\n\n", resolvedRegistryDir())

		return http.ListenAndServe(addr, srv.Handler())
	},
}

func init() {
	serveCmd.Flags().IntVarP(&servePort, "port", "p", 8080, "Port to listen on")
	serveCmd.Flags().StringVar(&serveProjectName, "name", "", "Project display name (default: directory name)")
	rootCmd.AddCommand(serveCmd)
}
