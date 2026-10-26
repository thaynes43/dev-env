// Command dev-env-portal is the internal read-only portal process.
// It serves the production assembly from /etc/dev-env-portal and does not
// select 1Password items or migrate the database.
package main

import (
	"context"
	"fmt"
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
	if len(args) == 1 && args[0] == "version" {
		fmt.Println(version.Get().String("dev-env-portal"))
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "portal usage unavailable")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := session.ServeProduction(ctx, session.ProductionRoot); err != nil {
		fmt.Fprintln(os.Stderr, "portal runtime unavailable")
		return 1
	}
	return 0
}
