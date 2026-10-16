// Command pilot_rendezvous performs one bounded pilot gate-to-dispatch step.
// It is nonexecuting by default. Tests inject every clock/transport/dispatch;
// an actual invocation still needs separate runtime authorization and the
// existing operator's immutable policy admission. It grants no authority.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	kjson "sigs.k8s.io/json"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

var errRefused = errors.New("pilot rendezvous refused; no retry or replacement")
var headPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

// Config is a reviewed nonsecret binding, never a token or an access grant.
// The original A identity and exact binary/CA/prompt bytes are pinned before
// observation. The operator independently enforces B's sealed request.
type Config struct {
	Policy           projectcanary.PilotPolicy `json:"policy"`
	AUID             string                    `json:"aUID"`
	APodUID          string                    `json:"aPodUID"`
	Head             string                    `json:"head"`
	CommonMarker     string                    `json:"commonMarker"`
	RepositoryMarker string                    `json:"repositoryMarker"`
	ATaskStarted     time.Time                 `json:"aTaskStarted"`
	Binary           string                    `json:"binary"`
	BinarySHA256     string                    `json:"binarySHA256"`
	CAFile           string                    `json:"caFile"`
	CASHA256         string                    `json:"caSHA256"`
	PromptFile       string                    `json:"promptFile"`
	ReceiptDir       string                    `json:"receiptDir"`
}

func (c Config) validate() error {
	if c.Policy.Validate() != nil || !protocol.ValidKubeUID(c.AUID) || !protocol.ValidKubeUID(c.APodUID) || !headPattern.MatchString(c.Head) || (!c.ATaskStarted.IsZero() && (c.ATaskStarted.Before(c.Policy.NotBefore) || !c.ATaskStarted.Before(c.Policy.ExpiresAt))) || c.CommonMarker == "" || c.RepositoryMarker == "" {
		return errRefused
	}
	for _, p := range []string{c.Binary, c.CAFile, c.PromptFile, c.ReceiptDir} {
		if !filepath.IsAbs(p) {
			return errRefused
		}
	}
	if !fileDigest(c.Binary, c.BinarySHA256) || !fileDigest(c.CAFile, c.CASHA256) {
		return errRefused
	}
	prompt, err := os.ReadFile(c.PromptFile)
	if err != nil || string(prompt) != c.Policy.Prompts[1] {
		return errRefused
	}
	return nil
}
func fileDigest(path, expected string) bool {
	if len(expected) != 64 {
		return false
	}
	data, err := os.ReadFile(path)
	sum := sha256.Sum256(data)
	return err == nil && hex.EncodeToString(sum[:]) == expected
}
func (c Config) dispatchArgs() []string {
	r := c.Policy.Request(1)
	return []string{"--project", r.Project, "--repo", r.Repo, "--agent", r.Agent, "--model", r.Model, "--effort", r.Effort, "--size", r.Size, "--profile", r.Profile, "--timeout", r.Limits.Timeout, "--idempotency-key", r.IdempotencyKey, "--prompt-file", c.PromptFile, "--wait", "0", "--once", "-o", "json", "--ca-file", c.CAFile}
}

// Phase ends are absolute, not reset by a poll, a model turn or late readiness.
// A six-minute cold-start rendezvous, four-minute ACK phase and two-minute
// terminal/handoff phase fit in A's 15m task, 20m pod, and the 30m window.
// Reserve a final minute for admission closure. Infeasible starts are refused.
type Deadlines struct {
	Startup  time.Time `json:"startup"`
	ACK      time.Time `json:"ack"`
	Terminal time.Time `json:"terminal"`
}

func deadlines(c Config, claim, created, now time.Time) (Deadlines, error) {
	d := Deadlines{claim.Add(6 * time.Minute), claim.Add(10 * time.Minute), claim.Add(12 * time.Minute)}
	cap := c.ATaskStarted.Add(15 * time.Minute)
	for _, t := range []time.Time{created.Add(20 * time.Minute), c.Policy.ExpiresAt.Add(-time.Minute)} {
		if t.Before(cap) {
			cap = t
		}
	}
	if claim.Before(c.ATaskStarted) || now.Before(claim) || now.Sub(claim) > 30*time.Second || d.Terminal.After(cap) {
		return Deadlines{}, errRefused
	}
	return d, nil
}

type Observation struct {
	Session apiv1.Session
	Log     apiv1.SessionLog
}

// validateGate consumes supported show/log output, including reported markers
// and an exact structured successful claim receipt. Log text is reported
// evidence, not a new authorization source or a private stop proof.
func validateGate(c Config, o Observation, now time.Time) (*Deadlines, error) {
	s := o.Session
	if o.Log.Truncated {
		return nil, errRefused
	}
	if s.UID != c.AUID || s.Name != c.Policy.SessionName(0) || s.Repo != "dev-env" || s.IdempotencyKey != c.Policy.Key(0) || s.Prompt != c.Policy.Prompts[0] || s.Agent != "codex" || s.Model != "gpt-6.1-sol" || s.Effort != "high" || s.Mode != "task" || s.Size != "S" || s.Profile != projectcanary.PilotProfile || s.Limits == nil || s.Limits.Timeout != "15m0s" || s.CreatedAt.Before(c.Policy.NotBefore) || (!c.ATaskStarted.IsZero() && s.CreatedAt.After(c.ATaskStarted)) {
		return nil, errRefused
	}
	if s.PrivateHistory == nil || s.PrivateHistory.SessionUID != c.AUID || s.PrivateHistory.LastExecutor == nil || s.PrivateHistory.LastExecutor.PodUID != c.APodUID {
		return nil, errRefused
	}
	if s.AgentStatus == nil {
		return nil, nil
	}
	a := s.AgentStatus
	if a.Task != nil || a.State == "exited" || (a.Head != "" && a.Head != c.Head) {
		return nil, errRefused
	}
	if a.LastHeartbeat == nil || now.Before(*a.LastHeartbeat) || now.Sub(*a.LastHeartbeat) > 30*time.Second {
		return nil, errRefused
	}
	if a.Head != c.Head || a.Boot != "ready" || a.ProjectBootstrap == nil || a.ProjectBootstrap.State != "Ready" {
		return nil, nil
	}
	if a.ProjectBootstrap.CatalogRevision != c.Policy.CatalogRevision || o.Log.Session != s.Name {
		return nil, errRefused
	}
	if !strings.Contains(o.Log.Text, c.CommonMarker) || !strings.Contains(o.Log.Text, c.RepositoryMarker) {
		return nil, nil
	}
	startPrefix := "[agentd] task " + s.Name + " started "
	var started time.Time
	for _, line := range strings.Split(o.Log.Text, "\n") {
		if strings.HasPrefix(line, startPrefix) {
			raw, _, ok := strings.Cut(strings.TrimPrefix(line, startPrefix), ",")
			if !ok || !started.IsZero() {
				return nil, errRefused
			}
			var err error
			started, err = time.Parse(time.RFC3339, raw)
			if err != nil {
				return nil, errRefused
			}
		}
	}
	if started.IsZero() || started.Before(s.CreatedAt) || started.Before(c.Policy.NotBefore) || started.After(now) || (!c.ATaskStarted.IsZero() && !started.Equal(c.ATaskStarted)) {
		return nil, errRefused
	}
	c.ATaskStarted = started

	var result *apiv1.TaskClaimResult
	for _, line := range strings.Split(o.Log.Text, "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var entry struct {
			Operation string `json:"operation"`
			ExitCode  *int   `json:"exitCode"`
			Stdout    string `json:"stdout"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil || entry.Operation != "task-claim-create" {
			continue
		}
		if entry.ExitCode == nil || *entry.ExitCode != 0 || result != nil {
			return nil, errRefused
		}
		var r apiv1.TaskClaimResult
		if json.Unmarshal([]byte(entry.Stdout), &r) != nil {
			return nil, errRefused
		}
		result = &r
	}
	if result == nil {
		return nil, nil
	}
	r := result.Claim
	if (result.Outcome != "Created" && result.Outcome != "Confirmed") || !protocol.ValidKubeUID(r.UID) || r.Archived || r.Owner != (apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: s.Name, UID: c.AUID}) || r.Actor.Kind != "session" || r.Actor.PodUID != c.APodUID || r.Actor.PodName != s.Name || r.Identity != (apiv1.TaskClaimIdentity{Project: "dev-env", Repository: "dev-env", TaskKey: "task:" + c.Policy.Key(0)}) || r.Provenance.CatalogRevision != c.Policy.CatalogRevision || r.Provenance.RulesRevision != c.Policy.RulesRevision || r.Provenance.ReportedHead != c.Head {
		return nil, errRefused
	}
	d, err := deadlines(c, r.CreatedAt, s.CreatedAt, now)
	return &d, err
}

type adapters struct {
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	observe  func(context.Context) (Observation, error)
	dispatch func(context.Context, []string) ([]byte, error)
	// claimAttempt durably marks BEFORE dispatch. Never removed on an error,
	// timeout or crash: uncertain create outcomes require owner reconciliation.
	claimAttempt func(Observation, Deadlines) error
}

type Receipt struct {
	State             string    `json:"state"`
	GateAt            time.Time `json:"gateAt"`
	DispatchAt        time.Time `json:"dispatchAt"`
	Deadlines         Deadlines `json:"deadlines"`
	BUID              string    `json:"bUID,omitempty"`
	ObservationRounds int       `json:"observationRounds"`
}

func run(ctx context.Context, c Config, a adapters) (Receipt, error) {
	receipt := Receipt{State: "NO_DISPATCH_GATE_UNCONFIRMED"}
	end := a.now().Add(5 * time.Minute)
	if !c.ATaskStarted.IsZero() {
		end = c.ATaskStarted.Add(5 * time.Minute)
	}
	if t := c.Policy.ExpiresAt.Add(-13 * time.Minute); t.Before(end) {
		end = t
	}
	for round := 1; round <= 60; round++ {
		now := a.now()
		if now.Before(c.Policy.NotBefore) || !now.Before(end) {
			return receipt, errRefused
		}
		// Each observation adapter has a finite context; no reconnect/fallback.
		callCtx, cancel := context.WithTimeout(ctx, min(10*time.Second, end.Sub(now)))
		observation, err := a.observe(callCtx)
		cancel()
		if err != nil {
			return receipt, err
		}
		receipt.ObservationRounds = round
		gateAt := a.now()
		d, err := validateGate(c, observation, gateAt)
		if err != nil {
			return receipt, err
		}
		if d != nil {
			receipt.GateAt = gateAt
			receipt.Deadlines = *d
			if !gateAt.Before(end) {
				return receipt, errRefused
			}
			// There is NO reporting callback/model turn between validation and this
			// durable attempt + direct invocation. Operator admission is still final.
			if err := a.claimAttempt(observation, *d); err != nil {
				return receipt, err
			}
			receipt.State = "ATTEMPT_RESERVED_DISPATCH_NOT_CONFIRMED"
			receipt.DispatchAt = a.now()
			if receipt.DispatchAt.Sub(gateAt) > 5*time.Second || !receipt.DispatchAt.Before(d.Startup) || ctx.Err() != nil {
				return receipt, errRefused
			}
			receipt.State = "DISPATCH_OUTCOME_UNKNOWN_NO_RETRY"
			dispatchCtx, stop := context.WithTimeout(ctx, min(10*time.Second, d.Startup.Sub(receipt.DispatchAt)))
			output, err := a.dispatch(dispatchCtx, c.dispatchArgs())
			stop()
			if err != nil {
				return receipt, err
			}
			var b apiv1.Session
			if json.Unmarshal(output, &b) != nil || b.Name != c.Policy.SessionName(1) || !protocol.ValidKubeUID(b.UID) || b.UID == c.AUID || b.IdempotencyKey != c.Policy.Key(1) {
				return receipt, errRefused
			}
			receipt.State = "B_CREATE_ACCEPTED_NOT_PILOT_ACCEPTANCE"
			receipt.BUID = b.UID
			return receipt, nil
		}
		delay := min(5*time.Second, end.Sub(a.now()))
		if delay <= 0 {
			return receipt, errRefused
		}
		if err := a.sleep(ctx, delay); err != nil {
			return receipt, err
		}
	}
	return receipt, errRefused
}

// command runs the supported existing CLI once, with bounded time/output,
// inherited existing credentials and no shell, extraction, retry or fallback.
func command(ctx context.Context, binary string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	var out, stderr limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("existing CLI failed (no retry): %w", err)
	}
	return out.Bytes(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 128<<10 {
		return 0, errors.New("CLI output exceeds finite capture")
	}
	return b.Buffer.Write(p)
}
func liveAdapters(c Config, invoke func(context.Context, string, []string) ([]byte, error)) adapters {
	read := func(ctx context.Context, args ...string) ([]byte, error) {
		return invoke(ctx, c.Binary, append(args, "--ca-file", c.CAFile))
	}
	return adapters{now: time.Now, sleep: func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}, observe: func(ctx context.Context) (Observation, error) {
		var o Observation
		raw, err := read(ctx, "show", c.Policy.SessionName(0), "--json", "--once")
		if err != nil {
			return o, err
		}
		if err = json.Unmarshal(raw, &o.Session); err != nil {
			return o, err
		}
		raw, err = read(ctx, "log", c.Policy.SessionName(0), "--tail", "200", "--once", "-o", "json")
		if err != nil {
			return o, err
		}
		err = json.Unmarshal(raw, &o.Log)
		return o, err
	}, dispatch: func(ctx context.Context, args []string) ([]byte, error) { return invoke(ctx, c.Binary, args) }, claimAttempt: func(o Observation, d Deadlines) error {
		if err := c.validate(); err != nil {
			return err
		}
		return saveExclusive(filepath.Join(c.ReceiptDir, c.Policy.Key(1)+".attempt.json"), map[string]any{"state": "ATTEMPT_RESERVED_NO_RETRY", "gate": o, "deadlines": d, "argv": append([]string{c.Binary}, c.dispatchArgs()...), "policyDigest": c.Policy.Digest(), "binarySHA256": c.BinarySHA256})
	}}
}
func saveExclusive(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
func execute(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("pilot_rendezvous", flag.ContinueOnError)
	configPath := fs.String("config", "", "reviewed nonsecret binding JSON")
	enabled := fs.Bool("execute", false, "invoke existing CLI; requires separate runtime approval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errRefused
	}
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		return err
	}
	if len(raw) > 128<<10 {
		return errRefused
	}
	var c Config
	strict, err := kjson.UnmarshalStrict(raw, &c)
	if err != nil || len(strict) != 0 || c.validate() != nil {
		return errRefused
	}
	if !*enabled {
		return json.NewEncoder(out).Encode(map[string]any{"state": "NONEXECUTING_REQUIRES_SEPARATE_RUNTIME_AUTHORITY", "argv": append([]string{c.Binary}, c.dispatchArgs()...), "phaseSeconds": []int{360, 600, 720}, "noTransportCalled": true})
	}
	ctx, cancel := context.WithDeadline(context.Background(), c.Policy.ExpiresAt.Add(-time.Minute))
	defer cancel()
	receipt, runErr := run(ctx, c, liveAdapters(c, command))
	saveErr := saveExclusive(filepath.Join(c.ReceiptDir, c.Policy.Key(1)+".result.json"), receipt)
	if err := json.NewEncoder(out).Encode(receipt); err != nil {
		return err
	}
	return errors.Join(runErr, saveErr)
}
func main() {
	if err := execute(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
