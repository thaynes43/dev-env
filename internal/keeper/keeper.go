// Package keeper is dev-env-keeper's work: it keeps short-lived credentials
// fresh in Secrets that session pods mount (DESIGN-001 3.1, 6.4, D-13). It is the
// only holder of the GitHub App keys and, from plan 03 on, of the rotating logins
// (D-11, D-12). One replica runs, and leader election on a Lease makes sure that
// even a second one would wait: a rotating refresh token must have exactly one
// owner (CLAUDE.md, hard rules).
//
// Plan 01 builds the first credential, the haynes-dev-bot installation token,
// minted every 40 minutes into dev-agents/dev-env-gh-token (D-52). Each
// credential is a Job with its own schedule, so the later ones (the Max login,
// the Codex login, the ops bot's token) are new Jobs on the same loop. The
// keeper's HTTPS endpoint for the operator (the login ceremony, status, archive;
// DESIGN-001 6.10) and its expiry pages are later plans too.
package keeper

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
)

const (
	// DefaultInterval is the longest wait between two refreshes of one
	// credential: the gh token is minted every 40 minutes and lasts 60, so a
	// keeper outage of up to 20 minutes, and a kubelet sync of up to about two,
	// is invisible to sessions (DESIGN-001 5.1, 6.4).
	DefaultInterval = 40 * time.Minute

	// The retry schedule after a failed refresh: 10 s, doubling, at most 5
	// minutes, each wait varied by up to a fifth either way. Within a gh token's
	// 20-minute margin that is eight attempts.
	backoffBase   = 10 * time.Second
	backoffCap    = 5 * time.Minute
	backoffJitter = 0.2

	// minWait is the shortest wait after a success, for a credential that comes
	// back with very little life in it.
	minWait = 30 * time.Second

	// attemptTimeout bounds one refresh and its write.
	attemptTimeout = 90 * time.Second
)

// Job keeps one credential fresh in one key of one Secret.
type Job struct {
	// Name names the credential in logs and in the readiness check.
	Name string
	// Secret and Key are where the value goes.
	Secret types.NamespacedName
	Key    string
	// Refresh produces the next value.
	Refresh func(ctx context.Context) (Refreshed, error)
}

// Refreshed is one new value of a credential.
type Refreshed struct {
	// Value is written to the Secret as is.
	Value     secretValue
	ExpiresAt time.Time
	// Fields are logged with the success. They must hold no material.
	Fields []any
}

// Keeper runs every Job on its own schedule. It is a controller-runtime
// Runnable that runs only on the leader.
type Keeper struct {
	Jobs   []Job
	Writer *SecretWriter
	Log    logr.Logger
	// Interval is the longest wait between two refreshes of one credential;
	// DefaultInterval when zero. A credential that lasts less than 1.5 times
	// this is refreshed after two thirds of its life instead.
	Interval time.Duration
	// Clock is the real clock when nil.
	Clock clock.Clock
	// Rand varies the retry waits; math/rand/v2's Float64 when nil.
	Rand func() float64

	mu    sync.Mutex
	state map[string]*jobState
}

// jobState is what the keeper knows about one Job. It lives in memory only:
// a new keeper refreshes every credential at once, so it never trusts what an
// earlier process wrote.
type jobState struct {
	// expiresAt is the expiry of the value this process last wrote; zero
	// before its first write.
	expiresAt time.Time
	// failures counts the failed attempts since the last success.
	failures int
	// next is when the next attempt is due.
	next time.Time
}

// NeedLeaderElection is true: only the leader refreshes.
func (k *Keeper) NeedLeaderElection() bool { return true }

// Start runs every Job until ctx ends. Each Job refreshes at once, then on its
// schedule.
func (k *Keeper) Start(ctx context.Context) error {
	if len(k.Jobs) == 0 {
		return errors.New("the keeper has no credentials to keep")
	}
	k.init()
	var wg sync.WaitGroup
	for i := range k.Jobs {
		job := k.Jobs[i]
		k.Log.Info("keeping credential", "credential", job.Name, "secret", job.Secret.String(), "key", job.Key, "interval", k.interval().String())
		wg.Go(func() { k.run(ctx, job) })
	}
	wg.Wait()
	return nil
}

// ReadyCheck is the keeper's readiness: every credential has a value this
// process wrote, and none of them has expired. A replica that is not the leader
// is never ready, and neither is a leader whose refreshes have failed for a
// credential's whole life. It is a healthz.Checker.
func (k *Keeper) ReadyCheck(_ *http.Request) error {
	k.init()
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.clock().Now()
	for _, job := range k.Jobs {
		st := k.state[job.Name]
		switch {
		case st.expiresAt.IsZero():
			return fmt.Errorf("%s: not written by this keeper yet", job.Name)
		case !now.Before(st.expiresAt):
			return fmt.Errorf("%s: the value this keeper wrote expired at %s", job.Name, st.expiresAt.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

func (k *Keeper) init() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.state != nil {
		return
	}
	k.state = make(map[string]*jobState, len(k.Jobs))
	for _, job := range k.Jobs {
		k.state[job.Name] = &jobState{}
	}
}

func (k *Keeper) run(ctx context.Context, job Job) {
	for {
		wait := k.attempt(ctx, job)
		if ctx.Err() != nil {
			return
		}
		k.mu.Lock()
		k.state[job.Name].next = k.clock().Now().Add(wait)
		k.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-k.clock().After(wait):
		}
	}
}

// attempt refreshes one credential and writes it, and returns how long to wait
// before the next attempt.
func (k *Keeper) attempt(ctx context.Context, job Job) time.Duration {
	actx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	res, err := job.Refresh(actx)
	if err == nil {
		err = k.Writer.Write(actx, job.Secret, job.Key, []byte(res.Value.Reveal()), res.ExpiresAt, k.clock().Now())
	}
	if ctx.Err() != nil {
		return 0 // shutting down; not a failure
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	st := k.state[job.Name]
	now := k.clock().Now()
	log := k.Log.WithValues("credential", job.Name, "secret", job.Secret.String())
	if err != nil {
		st.failures++
		wait := backoff(st.failures, k.rand())
		current := "none written by this keeper"
		if !st.expiresAt.IsZero() {
			current = st.expiresAt.UTC().Format(time.RFC3339)
		}
		log.Error(errors.New(scrub(err.Error(), res.Value)), "refresh failed; will retry",
			"attempt", st.failures, "retryIn", wait.Round(time.Second).String(), "currentExpiresAt", current)
		return wait
	}
	st.failures = 0
	st.expiresAt = res.ExpiresAt
	wait := refreshAfter(now, res.ExpiresAt, k.interval())
	kv := append([]any{"expiresAt", res.ExpiresAt.UTC().Format(time.RFC3339),
		"nextRefresh", now.Add(wait).UTC().Format(time.RFC3339)}, res.Fields...)
	log.Info("credential written", kv...)
	return wait
}

// refreshAfter is the wait after a success: the interval, or two thirds of the
// credential's remaining life if that is shorter, and never less than minWait.
func refreshAfter(now, expiresAt time.Time, interval time.Duration) time.Duration {
	wait := min(interval, expiresAt.Sub(now)*2/3)
	return max(wait, minWait)
}

// backoff is the wait after the n-th failure in a row (n >= 1). r is in [0, 1);
// 0.5 gives the unvaried wait.
func backoff(n int, r float64) time.Duration {
	d := backoffBase
	for i := 1; i < n && d < backoffCap; i++ {
		d *= 2
	}
	d = min(d, backoffCap)
	return time.Duration(float64(d) * (1 - backoffJitter + 2*backoffJitter*r))
}

func (k *Keeper) interval() time.Duration {
	if k.Interval <= 0 {
		return DefaultInterval
	}
	return k.Interval
}

func (k *Keeper) clock() clock.Clock {
	if k.Clock == nil {
		return clock.RealClock{}
	}
	return k.Clock
}

func (k *Keeper) rand() float64 {
	if k.Rand == nil {
		return rand.Float64()
	}
	return k.Rand()
}
