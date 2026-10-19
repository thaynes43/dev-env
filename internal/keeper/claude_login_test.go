package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/internal/claudelogin"
)

const (
	loginAccessCanary  = "login-access-synthetic-canary"
	loginRefreshCanary = "login-refresh-synthetic-canary"
	loginCodeCanary    = "LOGIN-CODE-SYNTHETIC-CANARY"
	loginNoiseCanary   = "NATIVE-NOISE-SYNTHETIC-CANARY"
	loginLink          = "https://claude.com/cai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&state=synthetic-state&code_challenge=synthetic-challenge&code_challenge_method=S256"
)

func loginCredentials(t *testing.T, mutate func(o map[string]any)) []byte {
	t.Helper()
	o := map[string]any{
		"accessToken": loginAccessCanary, "refreshToken": loginRefreshCanary,
		"expiresAt": claudeNow.Add(8 * time.Hour).UnixMilli(), "refreshTokenExpiresAt": claudeNow.Add(30 * 24 * time.Hour).UnixMilli(),
		"scopes": []string{"user:profile", "user:inference"}, "subscriptionType": nil, "rateLimitTier": nil,
	}
	if mutate != nil {
		mutate(o)
	}
	raw, err := json.Marshal(map[string]any{"claudeAiOauth": o})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// fakeClaude writes a stand-in for the CLI. The mode picks its behavior; every
// run records its arguments and whole environment outside the staging.
func fakeClaude(t *testing.T, mode string, creds []byte) (bin, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	credsFile := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(credsFile, creds, 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.296 (Claude Code)"; exit 0; fi
{ echo "ARGS: $*"; env; } > %[1]q
echo "diagnostic %[2]s"
case %[3]q in
  nolink) echo "no sign-in link here %[2]s"; exit 1 ;;
  hang) exec sleep 5 ;;
  badlink) echo "visit: https://evil.example/cai/oauth/authorize?client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=s&code_challenge=c" ;;
  *) echo "If the browser did not open, visit: %[4]s" ;;
esac
read -r code
[ "$code" = %[5]q ] || exit 3
[ %[3]q = loginfail ] && exit 1
mkdir -p "$CLAUDE_CONFIG_DIR"
cp %[6]q "$CLAUDE_CONFIG_DIR/.credentials.json"
chmod 600 "$CLAUDE_CONFIG_DIR/.credentials.json"
echo "Login successful %[2]s"
`, record, loginNoiseCanary, mode, loginLink, loginCodeCanary, credsFile)
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

type loginFix struct {
	*claudeFix
	dir  string
	logs *lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) add(s string) { l.mu.Lock(); l.b.WriteString(s + "\n"); l.mu.Unlock() }
func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// newLoginFix starts a worker's control socket on a real unix socket. The fixture
// clock is fake; the staged expiry is relative to it.
func newLoginFix(t *testing.T) *loginFix {
	t.Helper()
	f := newClaudeFix(t)
	f.tr.Prof = &claudeProfile{AccountUUID: "acct-1", OrganizationUUID: "org-1", SubscriptionType: "max", RateLimitTier: "default_claude_max_20x"}
	logs := &lockedBuffer{}
	f.w.Log = funcr.New(func(prefix, args string) { logs.add(prefix + " " + args) }, funcr.Options{Verbosity: 9})
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f.w.LoginDir = dir
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := f.w.serveControl(ctx); err != nil {
		t.Fatal(err)
	}
	return &loginFix{claudeFix: f, dir: dir, logs: logs}
}

func (f *loginFix) leftovers(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		if e.Name() != claudelogin.ControlSocket {
			left = append(left, e.Name())
		}
	}
	return left
}

// ceremony runs the helper against the fake CLI with the code on stdin.
type ceremonyResult struct {
	Res    claudelogin.Response
	Out    string
	Err    error
	Record string // the fake CLI's recorded arguments and environment
}

func (f *loginFix) ceremony(t *testing.T, mode string, creds []byte, o claudelogin.Options) ceremonyResult {
	t.Helper()
	bin, record := fakeClaude(t, mode, creds)
	o.Bin = bin
	var out bytes.Buffer
	res, err := claudelogin.Helper(context.Background(), f.dir, o, strings.NewReader(loginCodeCanary+"\n"), &out)
	rec, _ := os.ReadFile(record)
	return ceremonyResult{res, out.String(), err, string(rec)}
}

func (f *loginFix) assertNoSecrets(t *testing.T, texts ...string) {
	t.Helper()
	all := append([]string{f.logs.String()}, texts...)
	for _, s := range all {
		for _, c := range []string{loginAccessCanary, loginRefreshCanary, loginCodeCanary, loginNoiseCanary, claudeOldRefreshCanary} {
			if strings.Contains(s, c) {
				t.Fatalf("a secret or native diagnostic reached a log or the caller's output: %q", c)
			}
		}
	}
}

func TestClaudeLoginCeremonyAdoptsIntoJournalAndWipesStaging(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "INHERITED-SYNTHETIC-CANARY")
	f := newLoginFix(t)
	cr := f.ceremony(t, "ok", loginCredentials(t, nil), claudelogin.Options{})
	res, out, err, rec := cr.Res, cr.Out, cr.Err, cr.Record
	if err != nil || !res.OK || res.Code != "Adopted" || res.Generation != 1 || res.Login == nil || res.Login.Phase != "Adopted" {
		t.Fatalf("ceremony failed: %+v %v", res, err)
	}
	// The link, and only the link, went to the caller.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var shown map[string]string
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &shown) != nil || shown["signInUrl"] != loginLink || len(shown) != 1 {
		t.Fatalf("helper output is not exactly the sign-in link: %q", out)
	}
	// The code went in on stdin only; the CLI got fixed arguments and no inherited token.
	if !strings.Contains(rec, "ARGS: auth login --claudeai\n") || strings.Contains(rec, loginCodeCanary) || strings.Contains(rec, "INHERITED-SYNTHETIC-CANARY") || strings.Contains(rec, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatal("native login saw the code outside stdin, wrong arguments or an inherited token")
	}
	if strings.Contains(out, loginCodeCanary) || strings.Contains(out, loginNoiseCanary) || strings.Contains(out, "Login successful") {
		t.Fatal("the code or native output reached the caller")
	}
	// The journal holds the login; no refresh token is anywhere else.
	r := f.journal(t)
	if r.Stage != claudeReady || r.Access.Generation != 1 || r.Refresh.Reveal() != loginRefreshCanary || r.Access.AccountUUID != "acct-1" ||
		r.Access.OrganizationUUID != "org-1" || r.Access.SubscriptionType != "max" || r.Access.RateLimitTier != "default_claude_max_20x" ||
		!r.Access.ExpiresAt.Equal(claudeNow.Add(8*time.Hour)) || !r.RefreshExpiresAt.Equal(claudeNow.Add(30*24*time.Hour)) {
		t.Fatalf("journal not adopted correctly: %+v", r.Access)
	}
	live := f.liveSecret(t)
	blob, _ := json.Marshal(live)
	if f.liveGeneration(t) != 1 || strings.Contains(strings.ToLower(string(blob)), "refresh-synthetic") || bytes.Contains(live.Data["access.json"], []byte(loginRefreshCanary)) || !bytes.Contains(live.Data["access.json"], []byte(loginAccessCanary)) {
		t.Fatal("publication missing, or it carries refresh material")
	}
	if f.w.published != 1 {
		t.Fatal("adoption did not mark the generation published")
	}
	if left := f.leftovers(t); len(left) != 0 {
		t.Fatalf("staging not wiped: %v", left)
	}
	if res.Login.AttemptCleared != true || !res.Login.CleanupComplete {
		t.Fatalf("report does not show a cleared attempt: %+v", res.Login)
	}
	f.assertNoSecrets(t, out)
	raw, _ := json.Marshal(res)
	f.assertNoSecrets(t, string(raw))
}

func TestClaudeLoginFailuresMapToFixedCodesAndWipeStaging(t *testing.T) {
	short := claudelogin.Options{ChallengeBound: 150 * time.Millisecond}
	for _, tc := range []struct {
		name, mode string
		creds      []byte
		code       string // helper response code
		failure    string // report failure code
	}{
		{"exit before any link", "nolink", nil, "NeedsLogin", "NativeFailedBeforeChallenge"},
		{"no link in time", "hang", nil, "NeedsLogin", "InitialChallengeTimeout"},
		{"link for another host is never shown", "badlink", nil, "NeedsLogin", "NativeFailedBeforeChallenge"},
		{"wrong code, CLI fails after the link", "loginfail", nil, "NeedsLogin", "NativeLoginFailed"},
		{"no credential written", "ok", []byte(`{}`), "NeedsLogin", ""},
		{"no refresh token", "ok", nil, "NeedsLogin", ""},
		{"access too short-lived", "ok", nil, "NeedsLogin", ""},
		{"profile scope missing", "ok", nil, "NeedsLogin", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginFix(t)
			creds := tc.creds
			switch tc.name {
			case "no refresh token":
				creds = loginCredentials(t, func(o map[string]any) { o["refreshToken"] = "" })
			case "access too short-lived":
				creds = loginCredentials(t, func(o map[string]any) { o["expiresAt"] = claudeNow.Add(time.Minute).UnixMilli() })
			case "profile scope missing":
				creds = loginCredentials(t, func(o map[string]any) { o["scopes"] = []string{"user:inference"} })
			case "wrong code, CLI fails after the link":
				creds = loginCredentials(t, nil)
			}
			if creds == nil {
				creds = loginCredentials(t, nil)
			}
			cr := f.ceremony(t, tc.mode, creds, short)
			res, out, err := cr.Res, cr.Out, cr.Err
			if err == nil || res.OK || res.Code != tc.code || res.Login == nil || res.Login.FailureCode != tc.failure {
				t.Fatalf("want fixed code %s/%s, got %+v login=%+v err=%v", tc.code, tc.failure, res, res.Login, err)
			}
			if f.journal(t).Stage != claudeNeedsLogin || f.liveGeneration(t) != 0 {
				t.Fatal("a failed ceremony changed the journal or the publication")
			}
			if left := f.leftovers(t); len(left) != 0 {
				t.Fatalf("staging not wiped: %v", left)
			}
			if strings.Contains(out, "evil.example") {
				t.Fatal("a foreign link was shown to the caller")
			}
			raw, _ := json.Marshal(res)
			f.assertNoSecrets(t, out, string(raw))
			// The attempt was released: a new ceremony can start.
			if r, err := claudelogin.Control(context.Background(), f.dir, "begin", ""); err != nil || r.Code != "Started" {
				t.Fatalf("attempt not released: %+v %v", r, err)
			}
		})
	}
}

func TestClaudeLoginAccountMismatchAndProfileFailureLeaveTheJournalAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		prof *claudeProfile
		code string
	}{
		{"another account", &claudeProfile{AccountUUID: "acct-OTHER", OrganizationUUID: "org-1"}, "AccountMismatch"},
		{"profile unavailable", nil, "Unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginFix(t)
			f.seed(t, syntheticClaudeRecord(4))
			f.tr.Prof = tc.prof
			cr := f.ceremony(t, "ok", loginCredentials(t, nil), claudelogin.Options{})
			res, out, err := cr.Res, cr.Out, cr.Err
			if err == nil || res.Code != tc.code {
				t.Fatalf("want %s, got %+v %v", tc.code, res, err)
			}
			r := f.journal(t)
			if r.Access.Generation != 4 || r.Refresh.Reveal() != claudeOldRefreshCanary || r.Stage != claudeReady {
				t.Fatal("the existing login was disturbed")
			}
			if left := f.leftovers(t); len(left) != 0 {
				t.Fatalf("staging not wiped: %v", left)
			}
			f.assertNoSecrets(t, out)
		})
	}
}

// Adoption replaces a Ready login, recovers an interrupted one, and always
// goes strictly above both the journal's and the published generation.
func TestClaudeLoginGenerationAboveJournalAndPublished(t *testing.T) {
	f := newLoginFix(t)
	// A journal re-created empty while the live Secret still holds generation 7.
	f.seed(t, syntheticClaudeRecord(7))
	f.tick() // publishes 7
	var js corev1.Secret
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: "system", Name: DefaultClaudeJournalSecret}, &js); err != nil {
		t.Fatal(err)
	}
	js.Data = nil
	if err := f.c.Update(context.Background(), &js); err != nil {
		t.Fatal(err)
	}
	if f.liveGeneration(t) != 7 {
		t.Fatal("setup: expected generation 7 published")
	}
	cr := f.ceremony(t, "ok", loginCredentials(t, nil), claudelogin.Options{})
	res, err := cr.Res, cr.Err
	if err != nil || res.Generation != 8 || f.journal(t).Access.Generation != 8 || f.liveGeneration(t) != 8 {
		t.Fatalf("generation not above the published one: %+v %v", res, err)
	}
	// An interrupted refresh (RefreshIntent) is recovered by a fresh login.
	r := f.journal(t)
	r.Stage, r.AttemptUID = claudeIntent, claudeUID
	saveClaude(t, f.w.Journal, r)
	cr = f.ceremony(t, "ok", loginCredentials(t, func(o map[string]any) { o["accessToken"] = "login-access-two" }), claudelogin.Options{})
	res, err = cr.Res, cr.Err
	if err != nil || res.Generation != 9 || f.journal(t).Stage != claudeReady || f.journal(t).Access.AccessToken != "login-access-two" {
		t.Fatalf("interrupted login not recovered: %+v %v", res, err)
	}
}

// The control socket answers Busy while a refresh is in flight, and the staged
// credential is kept so the helper can retry; adoption never overlaps a refresh.
func TestClaudeLoginNeverAdoptsWhileARefreshIsInFlight(t *testing.T) {
	f := newLoginFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour) // the refresh is due
	begun, err := claudelogin.Control(context.Background(), f.dir, "begin", "")
	if err != nil || begun.Code != "Started" {
		t.Fatalf("begin: %+v %v", begun, err)
	}
	staged := filepath.Join(f.dir, begun.AttemptUID, claudelogin.StagedCredentials)
	if err := os.MkdirAll(filepath.Dir(staged), 0o700); err != nil || os.WriteFile(staged, loginCredentials(t, nil), 0o600) != nil {
		t.Fatal("could not stage the fixture credential")
	}
	inPOST, release := make(chan struct{}), make(chan struct{})
	f.tr.Run = func(call int, r secretValue) (claudeTokens, error) {
		close(inPOST)
		<-release
		return rotated(call, r)
	}
	done := make(chan struct{})
	go func() { f.tick(); close(done) }()
	<-inPOST
	res, err := claudelogin.Control(context.Background(), f.dir, "adopt", begun.AttemptUID)
	if err != nil || res.Code != "Busy" || res.OK {
		t.Fatalf("adopted (or errored) during a refresh: %+v %v", res, err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatal("a Busy answer must keep the staged credential for the retry")
	}
	close(release)
	<-done
	if r := f.journal(t); r.Access.Generation != 2 || r.Refresh.Reveal() != claudeNewRefreshCanary {
		t.Fatal("the refresh did not complete first")
	}
	// The retry now adopts on top of the completed refresh.
	f.clk.Step(time.Minute)
	res, err = claudelogin.Control(context.Background(), f.dir, "adopt", begun.AttemptUID)
	if err != nil || res.Code != "Adopted" || res.Generation != 3 || f.journal(t).Refresh.Reveal() != loginRefreshCanary {
		t.Fatalf("retry did not adopt: %+v %v", res, err)
	}
	if left := f.leftovers(t); len(left) != 0 {
		t.Fatalf("staging not wiped: %v", left)
	}
}

func TestClaudeLoginAttemptLifecycle(t *testing.T) {
	f := newLoginFix(t)
	ctx := context.Background()
	a, err := claudelogin.Control(ctx, f.dir, "begin", "")
	if err != nil || a.Code != "Started" || !claudelogin.ValidUID(a.AttemptUID) {
		t.Fatalf("begin: %+v %v", a, err)
	}
	if r, _ := claudelogin.Control(ctx, f.dir, "begin", ""); r.Code != "Busy" {
		t.Fatal("a second attempt must be Busy")
	}
	if r, _ := claudelogin.Control(ctx, f.dir, "adopt", claudeUID); r.Code != "NeedsLogin" {
		t.Fatalf("adopting another attempt must be refused: %+v", r)
	}
	if r, _ := claudelogin.Control(ctx, f.dir, "cancel", claudeUID); r.Code != "Unavailable" {
		t.Fatal("cancelling another attempt must be refused")
	}
	if r, _ := claudelogin.Control(ctx, f.dir, "cancel", a.AttemptUID); r.Code != "Cancelled" {
		t.Fatal("cancel failed")
	}
	if left := f.leftovers(t); len(left) != 0 {
		t.Fatalf("cancel left staging: %v", left)
	}
	// An attempt past its lifetime cannot be adopted, and its staging goes.
	b, _ := claudelogin.Control(ctx, f.dir, "begin", "")
	staged := filepath.Join(f.dir, b.AttemptUID, claudelogin.StagedCredentials)
	if os.MkdirAll(filepath.Dir(staged), 0o700) != nil || os.WriteFile(staged, loginCredentials(t, nil), 0o600) != nil {
		t.Fatal("fixture")
	}
	f.clk.Step(claudelogin.Lifetime + time.Second)
	if r, _ := claudelogin.Control(ctx, f.dir, "adopt", b.AttemptUID); r.Code != "NeedsLogin" {
		t.Fatalf("expired attempt adopted: %+v", r)
	}
	if left := f.leftovers(t); len(left) != 0 || f.journal(t).Stage != claudeNeedsLogin {
		t.Fatalf("expired attempt left staging or changed the journal: %v", left)
	}
	if r, _ := claudelogin.Control(ctx, f.dir, "begin", ""); r.Code != "Started" {
		t.Fatal("the expired attempt blocked a new one")
	}
}

func TestClaudeLoginStagedFileIsStrict(t *testing.T) {
	f := newLoginFix(t)
	ctx := context.Background()
	for name, setup := range map[string]func(staged string){
		"group-readable file": func(p string) { _ = os.WriteFile(p, loginCredentials(t, nil), 0o640); _ = os.Chmod(p, 0o640) },
		"symlinked file": func(p string) {
			target := filepath.Join(filepath.Dir(p), "elsewhere")
			_ = os.WriteFile(target, loginCredentials(t, nil), 0o600)
			_ = os.Symlink(target, p)
		},
		"unknown duplicate member": func(p string) {
			_ = os.WriteFile(p, []byte(`{"claudeAiOauth":{"accessToken":"a","accessToken":"b"}}`), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, _ := claudelogin.Control(ctx, f.dir, "begin", "")
			staged := filepath.Join(f.dir, a.AttemptUID, claudelogin.StagedCredentials)
			if os.MkdirAll(filepath.Dir(staged), 0o700) != nil {
				t.Fatal("fixture")
			}
			setup(staged)
			if r, _ := claudelogin.Control(ctx, f.dir, "adopt", a.AttemptUID); r.OK || r.Code != "NeedsLogin" {
				t.Fatalf("accepted: %+v", r)
			}
			if f.journal(t).Stage != claudeNeedsLogin || len(f.leftovers(t)) != 0 {
				t.Fatal("journal changed or staging kept")
			}
		})
	}
}

func TestClaudeLoginStatusReportsFixedWords(t *testing.T) {
	f := newLoginFix(t)
	ctx := context.Background()
	if r, _ := claudelogin.Control(ctx, f.dir, "status", ""); r.Code != "NeedsLogin" || !r.OK {
		t.Fatalf("empty journal: %+v", r)
	}
	f.seed(t, syntheticClaudeRecord(1))
	if r, _ := claudelogin.Control(ctx, f.dir, "status", ""); r.Code != "PendingPublication" || r.Generation != 1 {
		t.Fatalf("unpublished: %+v", r)
	}
	f.tick()
	if r, _ := claudelogin.Control(ctx, f.dir, "status", ""); r.Code != "Ready" || r.Generation != 1 {
		t.Fatalf("published: %+v", r)
	}
}

// A lost leadership refuses begin and adopt before anything is read or written.
func TestClaudeLoginRefusesWithoutLeadership(t *testing.T) {
	f := newLoginFix(t)
	ctx := context.Background()
	a, _ := claudelogin.Control(ctx, f.dir, "begin", "")
	staged := filepath.Join(f.dir, a.AttemptUID, claudelogin.StagedCredentials)
	if os.MkdirAll(filepath.Dir(staged), 0o700) != nil || os.WriteFile(staged, loginCredentials(t, nil), 0o600) != nil {
		t.Fatal("fixture")
	}
	f.w.Fence = func(context.Context, time.Duration) error { return errClaudeLeadership }
	if r, _ := claudelogin.Control(ctx, f.dir, "adopt", a.AttemptUID); r.Code != "Unavailable" {
		t.Fatalf("adopted without leadership: %+v", r)
	}
	if f.journal(t).Stage != claudeNeedsLogin {
		t.Fatal("journal written without leadership")
	}
	if r, _ := claudelogin.Control(ctx, f.dir, "begin", ""); r.Code != "Unavailable" {
		t.Fatalf("began without leadership: %+v", r)
	}
}

func TestClaudeLoginControlRejectsMalformedRequests(t *testing.T) {
	f := newLoginFix(t)
	if _, err := claudelogin.Control(context.Background(), "relative", "begin", ""); err == nil {
		t.Fatal("relative dir accepted")
	}
	if _, err := claudelogin.Control(context.Background(), f.dir, "refresh-once", ""); err == nil {
		t.Fatal("unknown action accepted")
	}
	if _, err := claudelogin.Control(context.Background(), f.dir, "adopt", "../../etc"); err == nil {
		t.Fatal("non-uid attempt accepted")
	}
	// begin with an attempt, and unknown members, are InvalidRequest.
	if r, _ := claudelogin.Control(context.Background(), f.dir, "begin", claudeUID); r.Code != "InvalidRequest" {
		t.Fatalf("begin with an attempt: %+v", r)
	}
	fi, err := os.Stat(filepath.Join(f.dir, claudelogin.ControlSocket))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("control socket is not private")
	}
}
