package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

// auth status (Story110 T23) tells a session how the keeper's shared Claude and
// Codex logins stand, like v1's claude-login-check. It is read-only and local:
// it reads the access projections the keeper publishes and agentd mounts
// (AGENTD_CLAUDE_LIVE_FILE, AGENTD_CODEX_ACCESS_FILE), calls no API and needs no
// token. The projections carry no refresh token. Only the non-secret members
// are decoded, so a token value cannot reach the output, an error or a log.

const (
	authMaxBytes       = 64 << 10
	authDefaultWarning = 30 * time.Minute
	authReady          = "Ready"
	authNeedsLogin     = "NeedsLogin"
	authUnknown        = "Unknown"
	authNotConfigured  = "NotConfigured"
)

// authProjection holds the members of both projection documents that status
// shows. Claude names its expiry "expiresAt", Codex "exp" and "last_refresh".
type authProjection struct {
	Version     int       `json:"version"`
	Generation  uint64    `json:"generation"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Exp         time.Time `json:"exp"`
	LastRefresh time.Time `json:"last_refresh"`
}

// authRow is one login's status, and the JSON element of -o json.
type authRow struct {
	Provider    string     `json:"provider"`
	State       string     `json:"state"`
	Reason      string     `json:"reason,omitempty"`
	Generation  uint64     `json:"generation,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	SecondsLeft *int64     `json:"secondsLeft,omitempty"`
	LastRefresh *time.Time `json:"lastRefresh,omitempty"`
	// code is the row's contribution to the exit code: 0 fine, 1 near expiry
	// or expired, 2 cannot tell, -1 not configured (does not count).
	code int
}

type authReport struct {
	Logins []authRow `json:"logins"`
}

func (a *app) auth(_ context.Context, args []string) error {
	cmd := newCommand("auth", outputJSON)
	var claudeFile, codexFile string
	warn := authDefaultWarning
	cmd.fs.StringVar(&claudeFile, "claude-file", "", "")
	cmd.fs.StringVar(&codexFile, "codex-file", "", "")
	cmd.fs.DurationVar(&warn, "warn", authDefaultWarning, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "status" {
		return usageError("auth takes one verb, status; agent-run help auth says how")
	}
	if warn < 0 {
		return usageError("auth status: --warn is a duration of zero or more, not %s", warn)
	}
	if claudeFile == "" {
		claudeFile = a.env.Getenv("AGENTD_CLAUDE_LIVE_FILE")
	}
	if codexFile == "" {
		codexFile = a.env.Getenv("AGENTD_CODEX_ACCESS_FILE")
	}
	now := a.env.Now()
	rows := []authRow{
		readAuthRow("claude", claudeFile, "expiresAt", now, warn),
		readAuthRow("codex", codexFile, "exp", now, warn),
	}
	code := authExitCode(rows)
	if cmd.c.output == outputJSON {
		if err := a.printJSON(authReport{Logins: rows}); err != nil {
			return err
		}
	} else {
		a.printAuthText(rows, warn)
	}
	if code != ExitOK {
		return &exitError{code: code}
	}
	return nil
}

// authExitCode is claude-login-check's contract: 0 ok, 1 expired or inside the
// warning window, 2 cannot tell. A known problem outranks an unknown. With
// nothing configured it cannot tell.
func authExitCode(rows []authRow) int {
	worst, seen := 0, false
	for _, r := range rows {
		if r.code < 0 {
			continue
		}
		seen = true
		switch r.code {
		case 1:
			return 1
		case 2:
			worst = 2
		}
	}
	if !seen {
		return 2
	}
	return worst
}

func readAuthRow(provider, path, expiryKey string, now time.Time, warn time.Duration) authRow {
	row := authRow{Provider: provider, State: authUnknown}
	if path == "" {
		row.State, row.Reason, row.code = authNotConfigured, "no projection is mounted in this pod", -1
		return row
	}
	raw, err := readAuthFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		row.Reason, row.code = "projection file is missing", 2
		return row
	case err != nil:
		row.Reason, row.code = "projection file is unreadable", 2
		return row
	}
	var p authProjection
	dec := json.NewDecoder(bytes.NewReader(raw))
	if dec.Decode(&p) != nil || p.Version != 1 || p.Generation == 0 {
		row.Reason, row.code = "projection is not a valid version 1 document", 2
		return row
	}
	exp := p.ExpiresAt
	if expiryKey == "exp" {
		exp = p.Exp
	}
	if exp.IsZero() {
		row.Reason, row.code = "projection has no expiry", 2
		return row
	}
	exp = exp.UTC()
	left := int64(exp.Sub(now) / time.Second)
	row.Generation, row.ExpiresAt, row.SecondsLeft = p.Generation, &exp, &left
	if !p.LastRefresh.IsZero() {
		lr := p.LastRefresh.UTC()
		row.LastRefresh = &lr
	}
	switch {
	case left <= 0:
		row.State, row.Reason, row.code = authNeedsLogin, "access has expired and the keeper has published no newer generation", 1
	case time.Duration(left)*time.Second <= warn:
		row.State, row.Reason, row.code = authReady, "access expires within the warning window", 1
	default:
		row.State, row.code = authReady, 0
	}
	return row
}

// readAuthFile reads one bounded regular file. A symlink is followed (a
// mounted Secret is a link into its data directory).
func readAuthFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > authMaxBytes {
		return nil, errors.New("not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, authMaxBytes+1))
	if err != nil || len(raw) > authMaxBytes {
		return nil, errors.New("not a bounded regular file")
	}
	return raw, nil
}

func (a *app) printAuthText(rows []authRow, warn time.Duration) {
	w := a.env.Stdout
	for _, r := range rows {
		line := fmt.Sprintf("%-7s %s", r.Provider, r.State)
		if r.ExpiresAt != nil {
			line += fmt.Sprintf("  generation %d  access expires %s (%s)", r.Generation,
				r.ExpiresAt.Format(time.RFC3339), authLeft(*r.SecondsLeft))
			if r.LastRefresh != nil {
				line += "  keeper last refresh " + r.LastRefresh.Format(time.RFC3339)
			}
		}
		if r.Reason != "" {
			line += "  " + r.Reason
		}
		_, _ = fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
	_, _ = fmt.Fprintf(w, "warning window %s; exit 0 ok, 1 expired or inside the window, 2 cannot tell\n", warn)
}

func authLeft(secs int64) string {
	if secs <= 0 {
		return "expired " + (time.Duration(-secs) * time.Second).Round(time.Second).String() + " ago"
	}
	return (time.Duration(secs) * time.Second).Round(time.Second).String() + " left"
}
