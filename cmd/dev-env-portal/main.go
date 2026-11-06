// Command dev-env-portal is the internal read-only portal process.
// It serves the production assembly from /etc/dev-env-portal and does not
// select 1Password items or migrate the database.
//
// Two fixed diagnostics run instead of the server: pages-source-check and
// db-check (DESIGN-056). db-check prints exactly one line on stdout: "PASS"
// (exit 0), "FAIL <class>" (exit 3) or "INTERRUPTED" (exit 4, no usable
// verdict). A usage error prints only to stderr and exits 2.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/thaynes43/dev-env/internal/version"
	"github.com/thaynes43/dev-env/web/portal/session"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	return runCommand(args, os.Stdout, os.Stderr, commands{pages: session.CheckPagesSource, db: session.CheckDatabase, serve: session.ServeProduction})
}

// commands are the process's three entries. Tests replace them.
type commands struct {
	pages func(context.Context) session.PagesSourceCheck
	db    func(context.Context) string
	serve func(context.Context, string) error
}

func runCommand(args []string, stdout, stderr io.Writer, c commands) int {
	if len(args) == 1 && args[0] == "version" {
		if _, err := fmt.Fprintln(stdout, version.Get().String("dev-env-portal")); err != nil {
			return 1
		}
		return 0
	}
	if len(args) != 0 && (len(args) != 1 || (args[0] != "pages-source-check" && args[0] != "db-check")) {
		_, _ = fmt.Fprintln(stderr, "portal usage unavailable")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) == 1 && args[0] == "db-check" {
		line, code := session.DBCheckLine(c.db(ctx))
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			return 1
		}
		return code
	}
	if len(args) == 1 {
		result := c.pages(ctx)
		if json.NewEncoder(stdout).Encode(result) != nil || result.Status != "available" {
			return 1
		}
		return 0
	}
	if err := c.serve(ctx, session.ProductionRoot); err != nil {
		_, _ = fmt.Fprintln(stderr, "portal runtime unavailable")
		return 1
	}
	return 0
}
