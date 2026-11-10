// Command dev-env-frontdoor is Meatball's front-door MCP server (DESIGN-060).
// It serves streamable HTTP at /mcp. The bearer token and the tracker
// credential are files; neither is logged.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/thaynes43/dev-env/internal/frontdoor"
	"github.com/thaynes43/dev-env/internal/version"
)

// opapiToken is the tracker credential shape. A startup error never prints one.
var opapiToken = regexp.MustCompile(`opapi-[0-9A-Za-z]{16,}`)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		if _, err := fmt.Fprintln(stdout, version.Get().String("dev-env-frontdoor")); err != nil {
			return 1
		}
		return 0
	}
	fs := flag.NewFlagSet("dev-env-frontdoor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg frontdoor.Config
	fs.StringVar(&cfg.Listen, "listen", ":8080", "listen address")
	fs.StringVar(&cfg.BearerFile, "token-file", "", "file holding the one bearer token")
	fs.StringVar(&cfg.TrackerFile, "tracker-token-file", "", "file holding the tracker credential")
	fs.StringVar(&cfg.OperatorTokenFile, "operator-token-file", "", "projected ServiceAccount token for the operator")
	fs.StringVar(&cfg.APIURL, "api-url", "", "operator API URL")
	fs.StringVar(&cfg.CAFile, "ca-file", "", "PEM CA for the operator API")
	fs.StringVar(&cfg.Opwork, "opwork", "", "opwork executable")
	fs.StringVar(&cfg.Project, "project", "", "OpenProject project")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(fs.Args()) != 0 {
		_, _ = fmt.Fprintln(stderr, "dev-env-frontdoor takes no arguments")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := frontdoor.Serve(ctx, cfg, stderr); err != nil {
		_, _ = fmt.Fprintln(stderr, "dev-env-frontdoor:", opapiToken.ReplaceAllString(err.Error(), "[redacted]"))
		return 1
	}
	return 0
}
