package keeper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/claudeauth"
)

type claudeProbeFixture struct {
	claudeRefreshTransport
	get func(secretValue) (claudeProfile, error)
}

func (p claudeProbeFixture) profile(_ context.Context, access secretValue) (claudeProfile, error) {
	return p.get(access)
}

func fakeClaudeProbe(result error) func(secretValue) (claudeProfile, error) {
	return func(secretValue) (claudeProfile, error) {
		return claudeProfile{AccountUUID: "acct-1", OrganizationUUID: "org-1"}, result
	}
}

func newEarlyClaudeFix(t *testing.T) *claudeFix {
	t.Helper()
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.w.EarlyRecovery = ClaudeEarlyRecoveryOptions{Enabled: true, ProbeInterval: DefaultClaudeProbeInterval, MinInterval: DefaultClaudeEarlyInterval}
	f.w.Transport = claudeProbeFixture{f.tr, fakeClaudeProbe(claudeProbeUnauthorized)}
	return f
}

func restartEarlyClaude(f *claudeFix) *claudeWorker {
	w := f.successorWorker()
	w.Transport, w.EarlyRecovery = f.w.Transport, f.w.EarlyRecovery
	return w
}

func (f *claudeFix) recovery(t *testing.T) claudeRecoveryState {
	t.Helper()
	s, err := decodeClaudeRecovery(f.journalSecret(t).Data[claudeRecoveryKey])
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClaudeEarlyOwnToken401DispatchesOnce(t *testing.T) {
	f := newEarlyClaudeFix(t)
	var gets, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/profile":
			call := gets.Add(1)
			access := claudeAccessCanary
			if call > 2 {
				access += "-replacement"
			}
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+access {
				t.Error("profile request did not use the keeper's current access token")
			}
			if call == 2 {
				w.WriteHeader(http.StatusUnauthorized)
			}
			_, _ = io.WriteString(w, claudeProfileBody)
		case "/token":
			if r.Method != http.MethodPost {
				t.Error("refresh request was not a POST")
			}
			posts.Add(1)
			_, _ = io.WriteString(w, `{"access_token":"`+claudeAccessCanary+`-replacement","expires_in":28800,"refresh_token":"`+claudeNewRefreshCanary+`"}`)
		default:
			t.Error("unexpected fake provider route")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	transport := newClaudeHTTPRefresh()
	transport.endpoint, transport.profilePath = server.URL+"/token", server.URL+"/profile"
	f.w.Transport = transport
	ctx := context.Background()
	f.tick()
	f.clk.Step(time.Minute)
	f.w.probeClaude(ctx)
	// Only the durable journal transfers the last healthy probe to the new leader.
	f.w = restartEarlyClaude(f)
	f.clk.Step(time.Minute)
	f.w.probeClaude(ctx)
	f.clk.Step(time.Minute)
	f.w.probeClaude(ctx)
	f.tick()

	state := f.recovery(t)
	if posts.Load() != 1 || gets.Load() != 3 || f.liveGeneration(t) != 2 || f.journal(t).Stage != claudeReady {
		t.Fatal("the first own-token 401 did not result in exactly one saved and published refresh")
	}
	if len(state.Probes) != 2 || state.Probes[0].LastHealthyBefore401 != claudeNow.Add(time.Minute).UnixNano() ||
		state.Probes[0].First401 != claudeNow.Add(2*time.Minute).UnixNano() || state.Incident.Replacement != 2 || !state.Incident.Published ||
		state.Incident.Page.Delivery != 0 || state.Incident.Resolved {
		t.Fatal("missing rejection bracket, replacement confirmation or interval hold")
	}
	if len(state.EarlyTransitions) != 1 || state.EarlyTransitions[0].Operation != claudeEarlyRefresh ||
		state.EarlyTransitions[0].PublishedAt == 0 || !state.EarlyTransitions[0].RefreshRotated {
		t.Fatal("successful early transition lacked provenance")
	}
}

func TestClaudeEarlyStaleProbeLosesToScheduledRefreshOrAdoption(t *testing.T) {
	for _, race := range []string{"scheduled", "adoption", "leadership loss", "expiry"} {
		t.Run(race, func(t *testing.T) {
			f := newEarlyClaudeFix(t)
			f.tick()
			ctx := context.Background()
			f.w.Transport = claudeProbeFixture{f.tr, func(secretValue) (claudeProfile, error) {
				switch race {
				case "scheduled":
					f.clk.Step(6 * time.Hour)
					f.tick()
				case "adoption":
					// A second leader adopted while the read-only GET was in flight.
					old := f.w
					f.w = f.successorWorker()
					if !adoptClaudeFixture(t, f).OK {
						t.Fatal("synthetic adoption failed")
					}
					f.w = old
				case "leadership loss":
					f.w.Fence = func(context.Context, time.Duration) error { return errClaudeLeadership }
				case "expiry":
					f.clk.Step(8 * time.Hour)
				}
				return claudeProfile{}, claudeProbeUnauthorized
			}}
			f.w.probeClaude(ctx)
			want := 0
			if race == "scheduled" {
				want = 1
			}
			if f.tr.Calls != want || len(f.journalSecret(t).Data[claudeRecoveryKey]) != 0 {
				t.Fatal("a stale GET affected the journal or dispatch")
			}
		})
	}
}

func TestClaudeEarlyOnlyProfile401Qualifies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		body  string
		class claudeProbeClass
	}{
		{"forbidden", 403, claudeAccessCanary, claudeProbeForbidden},
		{"rate limited", 429, claudeAccessCanary, claudeProbeRateLimited},
		{"server error", 503, claudeAccessCanary, claudeProbeServerError},
		{"malformed", 200, claudeAccessCanary, claudeProbeMalformed},
		{"mismatched account", 200, `{"account":{"uuid":"mismatched"},"organization":{"uuid":"org-1"}}`, claudeProbeAccountMismatch},
		{"mismatched organization", 200, `{"account":{"uuid":"acct-1"},"organization":{"uuid":"mismatched"}}`, claudeProbeAccountMismatch},
		{"timeout", 0, "", claudeProbeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEarlyClaudeFix(t)
			code := tc.code
			if code == 0 {
				code = http.StatusOK
			}
			tr, _ := claudeTokenServer(t, code, tc.body)
			if tc.code == 0 {
				tr.Client.Transport = claudeRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.Join(context.DeadlineExceeded, errors.New(claudeAccessCanary))
				})
			}
			f.w.Transport = claudeProbeFixture{f.tr, func(access secretValue) (claudeProfile, error) {
				return tr.profile(context.Background(), access)
			}}
			f.w.probeClaude(context.Background())
			s := f.recovery(t)
			if f.tr.Attempts != 0 || s.Incident.Generation != 0 || len(s.Probes) != 1 || s.Probes[0].Class != tc.class {
				t.Fatal("non-401 must be recorded as a fixed diagnostic without an attempt")
			}
			assertNoClaudeCanary(t, string(f.journalSecret(t).Data[claudeRecoveryKey]))
		})
	}
}

type claudeEarlyIntentHook struct {
	client.Client
	beforeIntent func()
	loseAck      bool
	seen         bool
}

func (c *claudeEarlyIntentHook) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
	if c.seen || claudePatchedStage(obj, p) != claudeIntent {
		return c.Client.Patch(ctx, obj, p, opts...)
	}
	c.seen = true
	if c.beforeIntent != nil {
		c.beforeIntent()
	}
	err := c.Client.Patch(ctx, obj, p, opts...)
	if err == nil && c.loseAck {
		return errors.New("synthetic intent acknowledgement lost")
	}
	return err
}

func TestClaudeEarlyIntentCASAndRestart(t *testing.T) {
	ctx := context.Background()
	t.Run("true CAS conflict", func(t *testing.T) {
		f := newEarlyClaudeFix(t)
		f.w.Journal.Client = &claudeEarlyIntentHook{Client: f.c, beforeIntent: func() {
			s := f.journalSecret(t)
			s.Labels = map[string]string{"synthetic": "changed"}
			if err := f.c.Update(ctx, s); err != nil {
				t.Fatal(err)
			}
		}}
		f.w.probeClaude(ctx)
		if f.tr.Attempts != 0 || f.recovery(t).LastAttempt != 0 || f.journal(t).Stage != claudeReady {
			t.Fatal("a failed intent CAS consumed the latch or reached transport")
		}
		f.clk.Step(time.Minute)
		restartEarlyClaude(f).probeClaude(ctx)
		if f.tr.Calls != 1 {
			t.Fatal("a fresh probe could not qualify after the CAS conflict")
		}
	})
	t.Run("lost acknowledgement", func(t *testing.T) {
		f := newEarlyClaudeFix(t)
		f.w.Journal.Client = &claudeEarlyIntentHook{Client: f.c, loseAck: true}
		f.w.probeClaude(ctx)
		f.clk.Step(DefaultClaudeEarlyInterval)
		restartEarlyClaude(f).probeClaude(ctx)
		s := f.recovery(t)
		if f.tr.Attempts != 0 || f.journal(t).Stage != claudeReady || s.Incident.Generation != 1 || s.Incident.Page.Failure != claudeRecoveryNotSent {
			t.Fatal("a committed intent with lost acknowledgement spent or rearmed the token")
		}
	})
	t.Run("successor rollback wins dispatch CAS", func(t *testing.T) {
		f := newEarlyClaudeFix(t)
		newLeader := restartEarlyClaude(f)
		f.w.Journal.Client = &claudeCommitHookClient{Client: f.c, hook: func() { newLeader.tick(ctx) }}
		f.w.probeClaude(ctx)
		f.clk.Step(DefaultClaudeEarlyInterval)
		newLeader.probeClaude(ctx)
		if f.tr.Calls != 0 || f.tr.Attempts != 1 || f.journal(t).Stage != claudeReady || f.recovery(t).Incident.Page.Failure != claudeRecoveryNotSent {
			t.Fatal("the losing dispatcher sent or the successor lost the latch")
		}
	})
}

func TestClaudeEarlyProvedUnsentRollbackRetainsLatch(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "local rollback"
		if restart {
			name = "successor rollback from marker"
		}
		t.Run(name, func(t *testing.T) {
			f := newEarlyClaudeFix(t)
			ctx := context.Background()
			f.notSent(func() {
				secret, intent, err := f.w.load(ctx)
				state := f.recovery(t)
				if err != nil || !claudeUnsentMarked(secret, intent) || state.Incident.Generation != 1 || state.LastAttempt == 0 {
					t.Fatal("intent, marker and latch were absent before dispatch")
				}
				f.c.Fail.Store(restart)
			})
			f.w.probeClaude(ctx)
			f.c.Fail.Store(false)
			if restart {
				f.w = restartEarlyClaude(f)
				f.tick()
			}
			f.clk.Step(2 * DefaultClaudeEarlyInterval)
			f.w.probeClaude(ctx)
			s := f.recovery(t)
			if r := f.journal(t); r.Stage != claudeReady || r.Refresh.Reveal() != claudeOldRefreshCanary || r.Access.Generation != 1 {
				t.Fatal("the unspent credential was not restored")
			}
			if f.tr.Attempts != 1 || f.tr.Calls != 0 || s.Incident.Generation != 1 || s.Incident.Page.Failure != claudeRecoveryNotSent {
				t.Fatal("proved-unsent rollback lost the consumed latch or its incident")
			}
		})
	}
}

func TestClaudeEarlyTerminalFailuresKeepNeedsLogin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		want    claudeRecoveryFailure
	}{
		{"family-wide revocation", errClaudeStatus, claudeRecoveryRefreshStatus},
		{"ambiguous send", errClaudeDispatch, claudeRecoveryPossiblySpent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEarlyClaudeFix(t)
			f.tr.Run = func(int, secretValue) (claudeTokens, error) {
				return claudeTokens{}, errors.Join(tc.failure, errors.New(claudeOldRefreshCanary))
			}
			ctx := context.Background()
			f.w.probeClaude(ctx)
			first := f.recovery(t).Incident
			f.clk.Step(2 * DefaultClaudeEarlyInterval)
			w := restartEarlyClaude(f)
			w.tick(ctx)
			w.probeClaude(ctx)
			if !f.tombstoned(t) || f.tr.Calls != 1 || first.Page.Failure != tc.want || first.Page.Delivery != claudePageUndelivered || first != f.recovery(t).Incident {
				t.Fatal("terminal failure retried, lost the tombstone or replaced its page")
			}
			assertNoClaudeCanary(t, string(f.journalSecret(t).Data[claudeRecoveryKey]))
		})
	}
}

type blockedClaudePublication struct {
	claudePublisher
	blocked bool
}

func (p *blockedClaudePublication) publish(ctx context.Context, a claudeauth.Access, expiresAt, now time.Time) (bool, error) {
	if p.blocked {
		return false, errClaudePublication
	}
	return p.claudePublisher.publish(ctx, a, expiresAt, now)
}

func TestClaudeEarlySavedReplacementRetriesOnlyPublication(t *testing.T) {
	f := newEarlyClaudeFix(t)
	f.tick()
	ctx := context.Background()
	p := &blockedClaudePublication{claudePublisher: f.w.Publisher, blocked: true}
	f.w.Publisher = p
	f.tr.Run = func(call int, r secretValue) (claudeTokens, error) {
		answer, err := rotated(call, r)
		answer.ExpiresIn = 9 * time.Minute
		return answer, err
	}
	f.w.probeClaude(ctx)
	if f.liveGeneration(t) != 1 || f.journal(t).Access.Generation != 2 || f.recovery(t).Incident.Published {
		t.Fatal("the replacement was not saved ahead of publication")
	}
	f.w = restartEarlyClaude(f)
	f.clk.Step(7 * time.Minute) // past the routine due time, before access expiry
	f.tick()
	if f.tr.Calls != 1 || f.recovery(t).Incident.Page.Failure != claudeRecoveryDeadline {
		t.Fatal("the due-time tick replayed a POST or failed to record the deadline")
	}
	p.blocked = false
	f.tick()
	if f.tr.Calls != 1 || f.liveGeneration(t) != 2 || !f.recovery(t).Incident.Published {
		t.Fatal("restarted keeper did not recover by publishing only")
	}
}

func TestClaudeEarlyLostReplacementAcknowledgementRetriesOnlyPublication(t *testing.T) {
	f := newEarlyClaudeFix(t)
	f.tick()
	f.w.Journal.Client = &claudeLostAckClient{Client: f.c, loseAt: 3} // intent, dispatch commit, replacement
	f.w.probeClaude(t.Context())
	if f.journal(t).Access.Generation != 2 || f.liveGeneration(t) != 1 || f.recovery(t).Incident.Published {
		t.Fatal("setup did not leave a saved but unpublished replacement")
	}
	f.w = restartEarlyClaude(f)
	f.tick()
	if f.tr.Calls != 1 || f.liveGeneration(t) != 2 || !f.recovery(t).Incident.Published {
		t.Fatal("recovery from the lost save acknowledgement did not publish only")
	}
}

func TestClaudeEarlyRepeatCutoffRequiresHealthySuccessorThroughFloor(t *testing.T) {
	for _, flap := range []bool{false, true} {
		name := "verified healthy at the floor"
		if flap {
			name = "successor rejected before the floor"
		}
		t.Run(name, func(t *testing.T) {
			f := newEarlyClaudeFix(t)
			ctx := context.Background()
			f.w.probeClaude(ctx)
			f.clk.Step(time.Minute)
			f.w.Transport = claudeProbeFixture{f.tr, fakeClaudeProbe(nil)}
			f.w.probeClaude(ctx)
			f.w = restartEarlyClaude(f)
			if flap {
				f.w.Transport = claudeProbeFixture{f.tr, fakeClaudeProbe(claudeProbeUnauthorized)}
				f.w.probeClaude(ctx)
			}
			f.clk.Step(DefaultClaudeEarlyInterval - time.Minute)
			f.w.Transport = claudeProbeFixture{f.tr, fakeClaudeProbe(nil)}
			f.w.probeClaude(ctx)
			first := f.recovery(t).Incident
			f.w.Transport = claudeProbeFixture{f.tr, fakeClaudeProbe(claudeProbeUnauthorized)}
			f.clk.Step(time.Minute)
			f.w.probeClaude(ctx)
			second := f.recovery(t).Incident
			if flap {
				if !first.Held || first.Resolved || first != second || first.Page.Failure != claudeRecoveryFlapping ||
					first.Page.Generation != 2 || first.Page.At != claudeNow.Add(time.Minute).UnixNano() || first.ReferenceHigh|first.ReferenceLow == 0 || f.tr.Calls != 1 {
					t.Fatal("flapping did not hold through the floor with one sanitized incident record")
				}
			} else if !first.Resolved || second.Generation != 2 || second.ReferenceHigh == first.ReferenceHigh || f.tr.Calls != 2 {
				t.Fatal("a new cutoff after healthy recovery and the floor did not qualify exactly once")
			}
		})
	}
}

func TestClaudeEarlyAdoptionClearsHoldButNotFloor(t *testing.T) {
	f := newEarlyClaudeFix(t)
	ctx := context.Background()
	f.w.probeClaude(ctx)
	f.clk.Step(time.Minute)
	f.w.probeClaude(ctx)
	before := f.recovery(t)
	if !before.Incident.Held {
		t.Fatal("fixture did not enter flapping hold")
	}
	f.w.Transport = f.tr
	if !adoptClaudeFixture(t, f).OK {
		t.Fatal("synthetic adoption failed")
	}
	after := f.recovery(t)
	if after.Incident.Held || !after.Incident.Resolved || before.LastAttempt != after.LastAttempt || before.CooldownUntil != after.CooldownUntil || before.Incident.Page != after.Incident.Page {
		t.Fatal("adoption did not persist the resolved hold while preserving the cooldown and evidence")
	}
	f.w.Transport = claudeProbeFixture{f.tr, fakeClaudeProbe(claudeProbeUnauthorized)}
	f.w.probeClaude(ctx)
	if f.tr.Calls != 1 {
		t.Fatal("new incarnation bypassed the durable inter-attempt floor")
	}
	f.clk.Step(DefaultClaudeEarlyInterval - time.Minute)
	f.w.probeClaude(ctx)
	if f.tr.Calls != 2 || f.recovery(t).Incident.Generation != 3 {
		t.Fatal("new incarnation could not qualify at the durable floor")
	}
}

func TestClaudeEarlyOffAndOlderKeeperPreserveJournalBytes(t *testing.T) {
	f := newEarlyClaudeFix(t)
	f.w.EarlyRecovery.Enabled = false
	ctx := context.Background()
	before := f.journalSecret(t)
	f.w.probeClaude(ctx)
	if f.tr.Attempts != 0 || f.journalSecret(t).ResourceVersion != before.ResourceVersion {
		t.Fatal("disabled mode attempted a probe or touched the journal")
	}
	f.w.Transport = f.tr
	if !adoptClaudeFixture(t, f).OK {
		t.Fatal("synthetic adoption failed")
	}
	provenance := f.journalSecret(t).Data[claudeProvenanceKey]
	f.w.EarlyRecovery.Enabled = true
	f.w.Transport = claudeProbeFixture{f.tr, fakeClaudeProbe(claudeProbeUnauthorized)}
	f.w.probeClaude(ctx)
	saved := f.journalSecret(t)
	if len(saved.Data[claudeRecoveryKey]) == 0 || !bytes.Equal(saved.Data[claudeProvenanceKey], provenance) {
		t.Fatal("early refresh changed the existing slice 3 rows")
	}
	f.clk.Step(6 * time.Hour)
	old := f.successorWorker()
	old.tick(ctx)
	rolledBack := f.journalSecret(t)
	p, err := decodeClaudeProvenance(rolledBack.Data[claudeProvenanceKey])
	if err != nil || len(p.Transitions) != 2 || p.Transitions[0].Operation != claudeAdoption || p.Transitions[1].Operation != claudeScheduledRefresh ||
		!bytes.Equal(rolledBack.Data[claudeRecoveryKey], saved.Data[claudeRecoveryKey]) {
		t.Fatal("a routine disabled rotation did not preserve both schemas")
	}
}

type claudeCadenceClock struct {
	*clocktesting.FakeClock
	armed chan time.Duration
}

func (c claudeCadenceClock) After(d time.Duration) <-chan time.Time {
	ch := c.FakeClock.After(d)
	c.armed <- d
	return ch
}

func TestClaudeEarlyConfiguredCadenceSkipsMissedSlots(t *testing.T) {
	f := newEarlyClaudeFix(t)
	armed := make(chan time.Duration, 2)
	f.w.Clock = claudeCadenceClock{f.clk, armed}
	f.w.EarlyRecovery.ProbeInterval = 10 * time.Second
	calls := 0
	f.w.Transport = claudeProbeFixture{f.tr, func(secretValue) (claudeProfile, error) {
		calls++
		if calls == 1 {
			f.clk.Step(11 * time.Second)
		}
		return fakeClaudeProbe(nil)(secretValue{})
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.w.runClaudeProbes(ctx)
	}()
	if d := <-armed; d != 10*time.Second {
		t.Fatalf("initial cadence=%s", d)
	}
	f.clk.Step(10 * time.Second)
	if d := <-armed; d != 9*time.Second || calls != 1 {
		t.Fatalf("cadence after missed slot=%s, calls=%d", d, calls)
	}
	f.clk.Step(9 * time.Second)
	if d := <-armed; d != 10*time.Second || calls != 2 {
		t.Fatalf("cadence after realignment=%s, calls=%d", d, calls)
	}
	cancel()
	<-done
}
