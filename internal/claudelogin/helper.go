package claudelogin

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Options tune the helper. The zero value is production: the `claude` on PATH,
// a sixty second wait for the sign-in link, and a bounded wait for a busy keeper.
type Options struct {
	Bin            string        // the claude binary; "claude" when empty
	ChallengeBound time.Duration // how long the CLI may take to print the link
	AdoptRetry     time.Duration // pause between adopt attempts while the keeper is busy
}

const (
	defaultChallengeBound = 60 * time.Second
	defaultAdoptRetry     = 3 * time.Second
	adoptAttempts         = 5
	claudeClientID        = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+ \(Claude Code\)$`)

// Environment is all the CLI gets: no inherited credential, token or proxy, and
// every path inside the staging directory. HOME is the attempt directory, so the
// CLI's state, caches and temp files all land in memory-backed staging.
func Environment(home, tmp string) []string {
	return []string{
		"HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "TMPDIR=" + tmp,
		"PATH=/usr/local/bin:/usr/bin:/bin", "TERM=dumb", "NO_COLOR=1",
		"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	}
}

// Helper runs one ceremony. It is invoked through authenticated interactive
// exec, never as the container's entrypoint. in carries the caller's one-time
// code to the CLI; out receives one JSON line with the sign-in link and the
// final result. Nothing from the CLI reaches out except the validated link.
func Helper(ctx context.Context, dir string, o Options, in io.Reader, out io.Writer) (result Response, resultErr error) {
	report := &Report{Phase: "Validation", AttemptCleared: true, CleanupComplete: true}
	defer func() { result.Login = report }()
	fail := Response{Code: "NeedsLogin"}
	if o.Bin == "" {
		o.Bin = "claude"
	}
	if o.ChallengeBound <= 0 {
		o.ChallengeBound = defaultChallengeBound
	}
	if o.AdoptRetry <= 0 {
		o.AdoptRetry = defaultAdoptRetry
	}
	c, cancel := context.WithTimeout(ctx, Lifetime)
	defer cancel()

	report.Phase = "Version"
	vctx, vcancel := context.WithTimeout(c, 10*time.Second)
	defer vcancel()
	v := exec.CommandContext(vctx, o.Bin, "--version")
	v.Env = Environment(dir, dir)
	v.WaitDelay = time.Second
	version, err := v.Output()
	if err != nil || !versionPattern.MatchString(strings.TrimSpace(string(version))) {
		report.FailureCode = "UnsupportedCLI"
		return Response{Code: "Unavailable"}, ErrHelper
	}

	report.Phase = "Reservation"
	// A lost begin reply may conceal a staging directory the keeper made.
	report.AttemptCleared, report.CleanupComplete = false, false
	r, err := Control(c, dir, "begin", "")
	if err != nil {
		return Response{Code: "Unavailable"}, ErrHelper
	}
	if !r.OK {
		if r.Code == "Busy" || r.Code == "InvalidRequest" {
			report.AttemptCleared, report.CleanupComplete = true, true
		}
		return r, ErrHelper
	}
	if !ValidUID(r.AttemptUID) {
		return fail, ErrHelper
	}
	attempt := r.AttemptUID
	home := filepath.Join(dir, attempt)
	spent := false // the keeper answered adopt: it has wiped the staging and ended the attempt
	defer func() { finish(dir, attempt, spent, report) }()

	report.Phase = "Staging"
	tmp := filepath.Join(home, "tmp")
	if os.MkdirAll(tmp, 0o700) != nil || os.MkdirAll(filepath.Join(home, ".claude"), 0o700) != nil {
		report.FailureCode = "StagingUnavailable"
		return fail, ErrHelper
	}
	cmd := exec.CommandContext(c, o.Bin, "auth", "login", "--claudeai")
	cmd.Dir = home
	cmd.Env = Environment(home, tmp)
	cmd.Stdin = in
	prompt := &promptWriter{Out: out, Challenge: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = prompt, prompt
	cmd.WaitDelay = time.Second
	report.Phase = "Native"
	if runNative(cmd, prompt, cancel, report, o.ChallengeBound) != nil {
		return fail, ErrHelper
	}

	report.Phase = "Adoption"
	for i := 0; ; i++ {
		r, err = Control(c, dir, "adopt", attempt)
		if err != nil {
			return Response{Code: "Unavailable"}, ErrHelper
		}
		if r.Code != "Busy" || i+1 >= adoptAttempts {
			break
		}
		// The keeper is mid-refresh. Adoption waits for it and never overlaps it.
		select {
		case <-c.Done():
			return Response{Code: "Unavailable"}, ErrHelper
		case <-time.After(o.AdoptRetry):
		}
	}
	spent = r.Code != "Busy"
	if !r.OK {
		return r, ErrHelper
	}
	report.Phase = "Adopted"
	return r, nil
}

// finish clears the attempt and removes this attempt's staging. The keeper wipes
// the staging itself on any adopt answer but Busy (spent); this covers every
// other exit.
// Staging stays when the CLI's process group may still be alive, because killing
// the direct child does not prove its descendants exited; expiry is then the
// recovery path.
func finish(dir, attempt string, spent bool, report *Report) {
	if report.NativeStarted && !report.NativeGroupAbsent {
		return
	}
	report.AttemptCleared = spent
	if !spent {
		r, err := Control(context.Background(), dir, "cancel", attempt)
		report.AttemptCleared = err == nil && r.OK && r.Code == "Cancelled"
	}
	home := filepath.Join(dir, attempt)
	_ = os.RemoveAll(home)
	_, err := os.Lstat(home)
	report.CleanupComplete = os.IsNotExist(err)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
var linkPattern = regexp.MustCompile(`https://claude\.(?:com|ai)/[^\s]+`)

// promptWriter reads the CLI's output line by line and forwards exactly one
// thing: the first line's sign-in link, as {"signInUrl": ...}, once it passes
// validLink. Every other byte is dropped, so no diagnostic, token-bearing body
// or code echo can reach the caller's output or a log.
type promptWriter struct {
	Out       io.Writer
	Challenge chan struct{}
	mu        sync.Mutex
	pending   string
	linkSeen  bool
}

func (w *promptWriter) Write(raw []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(raw)
	if len(w.pending) > 32768 {
		w.pending = ""
		return len(raw), nil
	}
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := ansi.ReplaceAllString(w.pending[:i], "")
		w.pending = w.pending[i+1:]
		if w.linkSeen {
			continue
		}
		if link := linkPattern.FindString(line); validLink(link) {
			w.linkSeen = true
			if json.NewEncoder(w.Out).Encode(map[string]string{"signInUrl": link}) != nil {
				return 0, ErrHelper
			}
			close(w.Challenge)
		}
	}
	return len(raw), nil
}

func (w *promptWriter) presented() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.linkSeen
}

// validLink accepts only the Claude sign-in page with this client's id and the
// request's own state and challenge. Anything else is never shown to the caller.
func validLink(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || (u.Host != "claude.com" && u.Host != "claude.ai") || u.User != nil || u.Fragment != "" || len(s) > 8192 {
		return false
	}
	if u.Path != "/cai/oauth/authorize" && u.Path != "/oauth/authorize" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	return err == nil && q.Get("client_id") == claudeClientID && q.Get("state") != "" && q.Get("code_challenge") != ""
}
