package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func credentialCtl(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, runner agentd.Runner) int {
	c := agentd.CredentialsFromEnv(getenv)
	fail := func(err error) int {
		if errors.Is(err, agentd.ErrNoCredential) {
			return protocol.ExitNoCredential
		}
		_, _ = fmt.Fprintf(stderr, "%s: %s: %v\n", binaryName, args[0], err)
		if errors.Is(err, agentd.ErrNoGrantsDir) {
			return protocol.ExitNoGrantsDir
		}
		return exitFailure
	}
	usageErr := func() int {
		_, _ = fmt.Fprintf(stderr, "%s: invalid %s arguments\n", binaryName, args[0])
		return exitUsage
	}
	fs := flag.NewFlagSet("ctl "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch args[0] {
	case "credential-install", "credential-remove":
		var s agentd.CredentialSpec
		var expires string
		fs.StringVar(&s.Name, "name", "", "")
		fs.StringVar(&s.GrantUID, "grant-uid", "", "")
		fs.StringVar(&s.PodUID, "pod-uid", "", "")
		if args[0] == "credential-install" {
			fs.StringVar(&expires, "expires", "", "")
		}
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return usageErr()
		}
		// Check the exec fence before touching private stdin. Unsupported
		// or invalid flags never echo argv, which may contain bad material.
		if s.PodUID == "" || s.PodUID != c.PodUID {
			return fail(errors.New("credential target pod changed or pod UID is missing"))
		}
		if args[0] == "credential-remove" {
			if err := c.Remove(s.Name, s.GrantUID, s.PodUID); err != nil {
				return fail(err)
			}
			return exitOK
		}
		t, err := time.Parse(time.RFC3339, expires)
		if err != nil {
			return fail(errors.New("invalid credential expiry"))
		}
		s.Expires = t
		if err := s.Check(time.Now()); err != nil {
			return fail(err)
		}
		p, err := agentd.ReadCredentialPayload(stdin, s.GrantUID)
		if err != nil {
			return fail(err)
		}
		if err := c.Install(s, p); err != nil {
			return fail(err)
		}
		return exitOK
	case "credential-list":
		out := fs.String("o", "text", "")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || (*out != "text" && *out != "json") {
			return usageErr()
		}
		list, err := c.List()
		if err != nil {
			return fail(err)
		}
		if *out == "json" {
			if list == nil {
				list = []protocol.InstalledCredential{}
			}
			if err := json.NewEncoder(stdout).Encode(list); err != nil {
				return exitFailure
			}
			return exitOK
		}
		for _, m := range list {
			_, _ = fmt.Fprintf(stdout, "%s %s until %s\n", m.Name, m.Credential, m.Expires.UTC().Format(time.RFC3339))
		}
		return exitOK
	case "credential-expire":
		if len(args) != 1 {
			return usageErr()
		}
		if err := c.Expire(); err != nil {
			return fail(err)
		}
		return exitOK
	case "credential-available":
		credential := fs.String("credential", "", "")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return usageErr()
		}
		if _, err := c.Use(*credential); err != nil {
			return fail(err)
		}
		return exitOK
	case "credential-use":
		credential := fs.String("credential", "", "")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() == 0 {
			return usageErr()
		}
		p, err := c.Use(*credential)
		if err != nil {
			return fail(err)
		}
		// The private value goes only in the child's environment. Preserve
		// its normal output and exit status; provider echoes are redacted.
		res, err := runner.Run(ctx, agentd.Cmd{Name: fs.Arg(0), Args: fs.Args()[1:], Env: agentd.CredentialEnvironment(p), Stdin: stdin})
		_, _ = stdout.Write(agentd.RedactCredentialOutput(res.Stdout, p))
		_, _ = stderr.Write(agentd.RedactCredentialOutput(res.Stderr, p))
		if err != nil {
			if code := agentd.ExitCodeOf(err); code >= 0 {
				return code
			}
			return fail(errors.New("credential command could not run"))
		}
		return res.ExitCode
	}
	return usageErr()
}
