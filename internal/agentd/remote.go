package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Remote mode on a pod-owned Claude login (Story110 T03, backlog 03's interim
// slice, Q-110-01). The pod's own CLI is the only owner of its login: agentd
// never copies a refresh token in, never runs `claude auth status` against
// this HOME (a second refresher of one rotating token revokes the family), and
// reads the credentials file for two facts only, whether a login is there and
// when it lapses. No token value is held, logged or put in an error.

// LoginTmuxSession is the tmux session that runs `claude auth login`, apart
// from the agent's own session so StartAgent's has-session check is unaffected.
const LoginTmuxSession = "login"

const (
	credentialsFileName = ".credentials.json"
	maxLoginFileBytes   = 256 << 10
	maxLoginCodeBytes   = 4 << 10
	loginExitMarker     = "LOGIN_EXIT="
	loginURLWait        = 60 * time.Second
	loginPollEvery      = 500 * time.Millisecond
)

// ErrLoginRequired marks a remote session whose pod has no usable login.
var ErrLoginRequired = errors.New("login required")

var loginURLPattern = regexp.MustCompile(`https://claude\.com/cai/oauth/authorize\?[^\s"'<>]+`)

// loginCheck is what agentd learns about the pod's login. It holds no token.
type loginCheck struct {
	// Present: the credentials file holds an access or refresh token.
	Present bool
	// Expires is the login's lapse time (the refresh token's), zero if unknown.
	Expires time.Time
}

// Usable reports whether the login is there and not lapsed at now.
func (c loginCheck) Usable(now time.Time) bool {
	return c.Present && (c.Expires.IsZero() || now.Before(c.Expires))
}

// credentialsShape is the only part of the credentials file agentd decodes.
// The decoded strings are reduced to booleans at once and never kept.
type credentialsShape struct {
	ClaudeAiOauth *struct {
		AccessToken           string `json:"accessToken"`
		RefreshToken          string `json:"refreshToken"`
		RefreshTokenExpiresAt int64  `json:"refreshTokenExpiresAt"`
	} `json:"claudeAiOauth"`
}

// checkLogin reads the pod's login presence and expiry. A missing file, a
// symlink, an oversize or unparseable file all mean no usable login; no error
// text carries file content.
func checkLogin(s Settings) loginCheck {
	path := filepath.Join(s.ClaudeConfigDir, credentialsFileName)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxLoginFileBytes {
		return loginCheck{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return loginCheck{}
	}
	var shape credentialsShape
	if json.Unmarshal(data, &shape) != nil || shape.ClaudeAiOauth == nil {
		return loginCheck{}
	}
	o := shape.ClaudeAiOauth
	c := loginCheck{Present: o.AccessToken != "" || o.RefreshToken != ""}
	if ts := o.RefreshTokenExpiresAt; ts > 0 {
		// Milliseconds since the epoch; tolerate seconds.
		if ts < 1e11 {
			c.Expires = time.Unix(ts, 0).UTC()
		} else {
			c.Expires = time.UnixMilli(ts).UTC()
		}
	}
	return c
}

// remoteWait is a remote session whose agent waits for a login.
type remoteWait struct {
	ws    protocol.Workspace
	repo  Step
	boot  string
	since time.Time
}

// loginWindowArgs starts `claude auth login` in its own wide tmux window (wide
// so the URL stays one line), with the token and the other unset variables
// removed so the flow cannot short-circuit on a static token. The window stays
// an hour after the CLI exits so its end can be read.
func loginWindowArgs(s Settings) []string {
	script := `env`
	for _, k := range unsetForRemote {
		script += " -u " + k
	}
	script += ` "$0" auth login; echo ` + loginExitMarker + `$?; sleep 3600`
	return []string{"new-session", "-d", "-s", LoginTmuxSession, "-x", "300", "-y", "50", "-c", s.Home,
		"sh", "-c", script, s.ClaudeBin}
}

func hasLoginWindow(ctx context.Context, r Runner, s Settings) bool {
	_, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"has-session", "-t", "=" + LoginTmuxSession}})
	return err == nil
}

// loginPane is the login window's text, wrapped lines joined. It is a secret
// carrier (the URL) and is returned to callers that print it to the operator
// only; nothing here logs it.
func loginPane(ctx context.Context, r Runner, s Settings) (string, error) {
	res, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"capture-pane", "-p", "-J", "-S", "-200", "-t", "=" + LoginTmuxSession + ":"}})
	if err != nil {
		return "", errors.New("the login window cannot be read")
	}
	return string(res.Stdout), nil
}

// StartLoginWindow starts the login ceremony's window unless one is running.
func StartLoginWindow(ctx context.Context, r Runner, s Settings) error {
	if hasLoginWindow(ctx, r, s) {
		return nil
	}
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: loginWindowArgs(s)}); err != nil {
		return fmt.Errorf("start the login window: tmux: %s", cmdDetail(err))
	}
	return nil
}

// StopLoginWindow ends the login window; a missing one is not an error.
func StopLoginWindow(ctx context.Context, r Runner, s Settings) {
	_, _ = r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"kill-session", "-t", "=" + LoginTmuxSession}})
}

// LoginURL returns the sign-in link to print to the operator and nobody else.
// A window whose CLI already ended (a used or abandoned flow) is replaced so
// the link is fresh. It waits up to loginURLWait for the link to appear.
func LoginURL(ctx context.Context, r Runner, s Settings, sess protocol.Session, now func() time.Time) (string, error) {
	if sess.Mode != protocol.ModeRemote {
		return "", errors.New("login is for remote sessions")
	}
	if checkLogin(s).Usable(now()) {
		return "", errors.New("this pod already has a login")
	}
	if hasLoginWindow(ctx, r, s) {
		if pane, err := loginPane(ctx, r, s); err == nil && strings.Contains(pane, loginExitMarker) {
			StopLoginWindow(ctx, r, s)
		}
	}
	if err := StartLoginWindow(ctx, r, s); err != nil {
		return "", err
	}
	deadline := now().Add(loginURLWait)
	for {
		if pane, err := loginPane(ctx, r, s); err == nil {
			if u := loginURLPattern.FindString(pane); u != "" {
				return u, nil
			}
		}
		if !now().Before(deadline) {
			return "", errors.New("no sign-in link appeared in the login window; run login url again")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(loginPollEvery):
		}
	}
}

var loginCodeShape = regexp.MustCompile(`^[A-Za-z0-9._~#+/=-]{8,}$`)

// LoginCode sends the code the sign-in page showed into the waiting prompt. The
// code arrives on stdin and goes to tmux through a buffer loaded from stdin,
// never an argument, a file or a log line. It returns once the code is sent.
func LoginCode(ctx context.Context, r Runner, s Settings, sess protocol.Session, stdin io.Reader) error {
	if sess.Mode != protocol.ModeRemote {
		return errors.New("login is for remote sessions")
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, maxLoginCodeBytes+1))
	if err != nil || len(raw) > maxLoginCodeBytes {
		return errors.New("read the code: it is missing or too long")
	}
	code := strings.TrimSpace(string(raw))
	if !loginCodeShape.MatchString(code) {
		return errors.New("the code does not look like <code>#<state>; nothing was sent")
	}
	if !hasLoginWindow(ctx, r, s) {
		return errors.New("no login window is waiting; run login url first")
	}
	if pane, err := loginPane(ctx, r, s); err != nil || strings.Contains(pane, loginExitMarker) {
		return errors.New("the login window has ended; run login url for a fresh link")
	}
	buffer := "agentd-login-" + newBootID()
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"load-buffer", "-b", buffer, "-"}, Stdin: strings.NewReader(code)}); err != nil {
		return errors.New("tmux load-buffer failed")
	}
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"paste-buffer", "-b", buffer, "-d", "-t", "=" + LoginTmuxSession + ":"}}); err != nil {
		return errors.New("tmux paste-buffer failed")
	}
	time.Sleep(deliverSettle)
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"send-keys", "-t", "=" + LoginTmuxSession + ":", "Enter"}}); err != nil {
		return errors.New("tmux send-keys failed")
	}
	return nil
}

// LoginStatus is one plain line about the login, for the operator.
func LoginStatus(ctx context.Context, r Runner, s Settings, sess protocol.Session, now time.Time) string {
	if sess.Mode != protocol.ModeRemote {
		return "not a remote session"
	}
	c := checkLogin(s)
	switch {
	case c.Usable(now):
		if c.Expires.IsZero() {
			return "logged in"
		}
		return fmt.Sprintf("logged in, lapses %s (%d days)", c.Expires.Format("2006-01-02"), int(c.Expires.Sub(now).Hours()/24))
	case c.Present:
		return "login-required: the login lapsed " + c.Expires.Format("2006-01-02")
	case !hasLoginWindow(ctx, r, s):
		return "login-required: no login window; run login url"
	}
	if pane, err := loginPane(ctx, r, s); err == nil && strings.Contains(pane, loginExitMarker) {
		return "login-required: the login window ended without a login; run login url"
	}
	return "login-required: waiting for the code; run login url, then login code"
}

// remoteLoginUsable reports whether a remote session may start its agent now.
func remoteLoginUsable(s Settings, sess protocol.Session, now time.Time) bool {
	return sess.Mode != protocol.ModeRemote || checkLogin(s).Usable(now)
}

// loginWindowEnded reports whether the login window is gone or its CLI has
// finished; agentd waits for that before it starts the agent so the CLI has
// written the whole login (.claude.json after the credentials).
func loginWindowEnded(ctx context.Context, r Runner, s Settings) bool {
	if !hasLoginWindow(ctx, r, s) {
		return true
	}
	pane, err := loginPane(ctx, r, s)
	return err == nil && strings.Contains(pane, loginExitMarker)
}

// claudeSessionRemote is the part of the CLI's sessions/<pid>.json that names
// the Remote Control entry (S-6: bridgeSessionId holds session_<id>).
type claudeSessionRemote struct {
	BridgeSessionID string `json:"bridgeSessionId"`
}

const remoteURLBase = "https://claude.ai/code/"

var bridgeSessionID = regexp.MustCompile(`^session_[A-Za-z0-9_-]{1,120}$`)

// collectRemoteControl builds status.remoteControl from the CLI's own session
// record, not from the pane. loggedIn is whether the pod has a usable login;
// pid is the live CLI's pid, 0 when none.
func collectRemoteControl(s Settings, sess protocol.Session, loggedIn bool, pid int, launched bool) *protocol.RemoteControl {
	rc := &protocol.RemoteControl{Name: sess.Name}
	switch {
	case pid > 0:
		rc.State = protocol.RemoteStarting
		var rec claudeSessionRemote
		data, err := os.ReadFile(filepath.Join(s.ClaudeConfigDir, "sessions", strconv.Itoa(pid)+".json"))
		if err == nil && json.Unmarshal(data, &rec) == nil && bridgeSessionID.MatchString(rec.BridgeSessionID) {
			rc.State, rc.SessionID, rc.URL = protocol.RemoteRegistered, rec.BridgeSessionID, remoteURLBase+rec.BridgeSessionID
		}
	case !loggedIn:
		rc.State = protocol.RemoteLoginRequired
	case launched:
		rc.State = protocol.RemoteOffline
	default:
		rc.State = protocol.RemoteStarting
	}
	return rc
}
