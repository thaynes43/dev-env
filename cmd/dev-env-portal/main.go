// Command dev-env-portal is the internal read-only portal process.
// It serves the production assembly from /etc/dev-env-portal and does not
// select 1Password items or migrate the database.
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
	return runCommand(args, os.Stdout, os.Stderr, session.CheckPagesSource, session.ServeProduction)
}

func runCommand(args []string, stdout, stderr io.Writer, check func(context.Context) session.PagesSourceCheck, serve func(context.Context, string) error) int {
	if len(args) == 1 && args[0] == "version" {
		if _, err := fmt.Fprintln(stdout, version.Get().String("dev-env-portal")); err != nil {
			return 1
		}
		return 0
	}
	if len(args) != 0 && (len(args) != 1 || args[0] != "pages-source-check") {
		_, _ = fmt.Fprintln(stderr, "portal usage unavailable")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) == 1 {
		result := check(ctx)
		if json.NewEncoder(stdout).Encode(result) != nil || result.Status != "available" {
			return 1
		}
		return 0
	}
	if err := serve(ctx, session.ProductionRoot); err != nil {
		_, _ = fmt.Fprintln(stderr, "portal runtime unavailable")
		return 1
	}
	return 0
}
