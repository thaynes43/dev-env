package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// grantCtl runs `agentd ctl grant-install|grant-remove|grant-use|grant-list`
// (D-63): the kube grants in the pod's memory-backed grants volume. The broker
// runs install and remove by exec, with the token on stdin; `agent-run grant`
// runs use and list. A missing grants directory exits
// protocol.ExitNoGrantsDir, which tells the broker the pod predates D-63.
func grantCtl(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	g := agentd.GrantsFromEnv(getenv)
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "%s: %s: %v\n", binaryName, args[0], err)
		if errors.Is(err, agentd.ErrNoGrantsDir) {
			return protocol.ExitNoGrantsDir
		}
		return exitFailure
	}
	usageErr := func(format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "%s: %s: %s\n", binaryName, args[0], fmt.Sprintf(format, a...))
		return exitUsage
	}
	fs := flag.NewFlagSet(binaryName+" ctl "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	switch args[0] {
	case "grant-install":
		var spec agentd.GrantSpec
		var expires string
		podUID := fs.String("pod-uid", "", "")
		fs.StringVar(&spec.Name, "name", "", "")
		fs.StringVar(&spec.Role, "role", "", "")
		fs.StringVar(&expires, "expires", "", "")
		fs.Func("namespace", "", func(v string) error { spec.Namespaces = append(spec.Namespaces, v); return nil })
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return usageErr("usage: ctl grant-install --pod-uid <uid> --name <grant> --expires <RFC3339> --role <role> [--namespace <ns>]... (the token on stdin)")
		}
		t, err := time.Parse(time.RFC3339, expires)
		if err != nil {
			return usageErr("--expires %q is not an RFC 3339 time", expires)
		}
		spec.Expires = t
		// Everything is checked before the token is read and before
		// anything is written. A refused value exits 1, not 2: the broker
		// reads exit 2 as an agentd that does not know the command.
		if err := spec.Check(time.Now()); err != nil {
			return fail(err)
		}
		// Exec addresses a pod by name. Fence a replacement before reading
		// its stdin, so a token never enters the wrong pod's grant store.
		if *podUID == "" || *podUID != getenv(protocol.PodUIDEnv) {
			return fail(errors.New("the exec target pod changed, or --pod-uid is missing"))
		}
		token, err := agentd.ReadToken(stdin)
		if err != nil {
			return fail(err)
		}
		if err := g.Install(spec, token); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(stdout, "installed %s (%s) until %s as kube context %s\n",
			spec.Name, scope(spec.Role, spec.Namespaces), spec.Expires.UTC().Format(time.RFC3339), spec.Name)
		return exitOK
	case "grant-remove":
		name := fs.String("name", "", "")
		podUID := fs.String("pod-uid", "", "")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || *name == "" {
			return usageErr("usage: ctl grant-remove --pod-uid <uid> --name <grant>")
		}
		if *podUID == "" || *podUID != getenv(protocol.PodUIDEnv) {
			return fail(errors.New("the exec target pod changed, or --pod-uid is missing"))
		}
		if err := g.Remove(*name); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(stdout, "removed %s\n", *name)
		return exitOK
	case "grant-use":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") {
			return usageErr("usage: ctl grant-use <grant>|%s", protocol.DefaultContext)
		}
		if err := g.Use(args[1]); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(stdout, "kubectl now uses %s\n", args[1])
		return exitOK
	case "grant-list":
		out := fs.String("o", "text", "")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || (*out != "text" && *out != "json") {
			return usageErr("usage: ctl grant-list [-o text|json]")
		}
		list, err := g.List()
		if err != nil {
			return fail(err)
		}
		if *out == "json" {
			if list == nil {
				list = []protocol.InstalledGrant{}
			}
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(list); err != nil {
				return exitFailure
			}
			return exitOK
		}
		if len(list) == 0 {
			_, _ = fmt.Fprintln(stdout, "no grants installed in this pod")
			return exitOK
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "CURRENT\tNAME\tROLE\tNAMESPACES\tEXPIRES\tEXPIRED")
		for _, ig := range list {
			cur, expired := "", "no"
			if ig.Current {
				cur = "*"
			}
			if ig.Expired {
				expired = "yes"
			}
			ns := strings.Join(ig.Namespaces, ",")
			if ns == "" {
				ns = "(cluster-wide)"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", cur, ig.Name, ig.Role, ns, ig.Expires.UTC().Format(time.RFC3339), expired)
		}
		_ = tw.Flush()
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "%s: unknown ctl command %q\n", binaryName, args[0])
		return exitUsage
	}
}

func scope(role string, namespaces []string) string {
	if len(namespaces) == 0 {
		return role + " cluster-wide"
	}
	return role + " in " + strings.Join(namespaces, ", ")
}
