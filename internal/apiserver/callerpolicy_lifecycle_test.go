package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
)

func boundedLifecycleFixture(t *testing.T, workers bool) (*fixture, string, apiv1.CreateSessionRequest) {
	t.Helper()
	p := meatballPolicy()
	p.Spec.MaxConcurrent = 1
	f := meatballFixture(t, p)
	if !workers {
		return f, tokMeatball, meatballRequest()
	}
	token := f.sessionPod("bounded-parent", "dev", 0)
	parent := f.session("bounded-parent")
	cap := int32(1)
	parent.Spec.Limits = &v1alpha1.SessionLimits{MaxWorkers: &cap}
	if err := f.c.Update(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	return f, token, task()
}

func policyCreated(t *testing.T, f *fixture, token string, req apiv1.CreateSessionRequest) string {
	t.Helper()
	w := f.do(http.MethodPost, apiv1.SessionsPath, token, req)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	return decode[apiv1.Session](t, w).Name
}

func TestCallerPolicyResumeCapacity(t *testing.T) {
	for _, workers := range []bool{false, true} {
		for _, terminal := range []bool{false, true} {
			t.Run(fmt.Sprintf("workers=%t/terminal=%t", workers, terminal), func(t *testing.T) {
				f, token, req := boundedLifecycleFixture(t, workers)
				a := policyCreated(t, f, token, req)
				if terminal {
					sess := f.session(a)
					sess.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited}
					if err := f.c.Status().Update(context.Background(), sess); err != nil {
						t.Fatal(err)
					}
				}
				if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(a), tokHuman, nil); w.Code != 202 {
					t.Fatal(w.Code, w.Body.String())
				}
				b := policyCreated(t, f, token, req)
				wantError(t, f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil), 429, apiv1.CodeLimitExceeded)
				if err := f.c.Delete(context.Background(), f.session(b)); err != nil {
					t.Fatal(err)
				}
				if w := f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil); w.Code != 202 {
					t.Fatal("available capacity did not permit resume", w.Code, w.Body.String())
				}
				wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, req), 429, apiv1.CodeLimitExceeded)
				// A reservation survives stale status, later suspension and another
				// resume; the target's own reservation must not double-count it.
				if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(a), tokHuman, nil); w.Code != 202 {
					t.Fatal(w.Code, w.Body.String())
				}
				wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, req), 429, apiv1.CodeLimitExceeded)
				if w := f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil); w.Code != 202 {
					t.Fatal("repeat resume counted its own reservation", w.Code, w.Body.String())
				}
				if err := f.c.Delete(context.Background(), f.session(a)); err != nil {
					t.Fatal(err)
				}
				policyCreated(t, f, token, req)
			})
		}
	}
}

func TestCallerPolicyResumeFencesOtherReplicas(t *testing.T) {
	for _, workers := range []bool{false, true} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			f, token, req := boundedLifecycleFixture(t, workers)
			a := policyCreated(t, f, token, req)
			f.do(http.MethodPost, apiv1.SessionSuspendPath(a), tokHuman, nil)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				close(entered)
				select {
				case <-release:
				case <-time.After(10 * time.Second):
					return context.DeadlineExceeded
				}
				return c.Patch(ctx, obj, patch, opts...)
			}})
			other := meatballFixture(t)
			other.srv.Client, other.srv.Live, other.srv.Auth = f.c, f.c, f.auth
			result := make(chan int, 1)
			go func() { result <- f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil).Code }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("resume did not reach its patch")
			}
			wantError(t, other.do(http.MethodPost, apiv1.SessionsPath, token, req), 409, apiv1.CodeConflict)
			unblock()
			if code := <-result; code != 202 {
				t.Fatal("resume", code)
			}
			wantError(t, other.do(http.MethodPost, apiv1.SessionsPath, token, req), 429, apiv1.CodeLimitExceeded)
		})
	}
}

func TestCallerPolicyScheduledContentionRetries(t *testing.T) {
	f, token, req := boundedLifecycleFixture(t, true)
	parent := f.session("bounded-parent")
	*parent.Spec.Limits.MaxWorkers = 2
	if err := f.c.Update(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			close(entered)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				return context.DeadlineExceeded
			}
		}
		return c.Create(ctx, obj, opts...)
	}})
	other := meatballFixture(t)
	other.srv.Client, other.srv.Live, other.srv.Auth = f.c, f.c, f.auth
	other.srv.Now = f.srv.Now
	result := make(chan int, 1)
	go func() { result <- f.do(http.MethodPost, apiv1.SessionsPath, token, req).Code }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("ordinary create did not acquire admission")
	}
	d := swDispatch()
	d.Owner = &scheduledwork.SessionRef{Name: parent.Name, UID: parent.UID}
	_, _, _, err := other.srv.CreateScheduledSession(context.Background(), swRequest(), d)
	var refused *scheduledwork.RefusedError
	if err == nil || errors.As(err, &refused) {
		t.Fatalf("contention must be retryable, got %v", err)
	}
	unblock()
	if code := <-result; code != 201 {
		t.Fatal("ordinary create", code)
	}
	name, uid, adopted, err := other.srv.CreateScheduledSession(context.Background(), swRequest(), d)
	if err != nil || name == "" || uid == "" || adopted {
		t.Fatal("retry did not create", name, uid, adopted, err)
	}
	if f.session(name).Annotations[callerParentUID] != string(parent.UID) {
		t.Fatal("scheduled child did not retain its bounded parent identity")
	}
	_, _, adopted, err = other.srv.CreateScheduledSession(context.Background(), swRequest(), d)
	if err != nil || !adopted {
		t.Fatal("retry did not adopt", adopted, err)
	}
}

func TestCallerPolicyResumeRefusesMissingOrReplacedAuthority(t *testing.T) {
	for _, workers := range []bool{false, true} {
		for _, replace := range []bool{false, true} {
			f, token, req := boundedLifecycleFixture(t, workers)
			a := policyCreated(t, f, token, req)
			f.do(http.MethodPost, apiv1.SessionSuspendPath(a), tokHuman, nil)
			if workers {
				parent := f.session("bounded-parent")
				// Simulate completed rescue/finalization, so the old parent
				// really disappears before checking orphan or replacement.
				parent.Finalizers = nil
				if err := f.c.Update(context.Background(), parent); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), parent); err != nil {
					t.Fatal(err)
				}
				if replace {
					parent.ResourceVersion, parent.UID = "", "replacement"
					if err := f.c.Create(context.Background(), parent); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if err := f.c.Delete(context.Background(), meatballPolicy()); err != nil {
					t.Fatal(err)
				}
			}
			wantError(t, f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil), 403, apiv1.CodeForbidden)
		}
	}
}

func TestCallerPolicyUnknownResumeRetainsLock(t *testing.T) {
	for _, committed := range []bool{false, true} {
		f, token, req := boundedLifecycleFixture(t, false)
		a := policyCreated(t, f, token, req)
		f.do(http.MethodPost, apiv1.SessionSuspendPath(a), tokHuman, nil)
		f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if committed {
				if err := c.Patch(ctx, obj, patch, opts...); err != nil {
					return err
				}
			}
			return context.DeadlineExceeded
		}})
		wantError(t, f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil), 503, apiv1.CodeUnavailable)
		f.srv.Client = f.c
		wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, req), 409, apiv1.CodeConflict)
	}
}
