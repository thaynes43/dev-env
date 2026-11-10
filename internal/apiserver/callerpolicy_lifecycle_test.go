package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// The annotation an earlier draft used to hold a resumed session's slot until
// reap. A resume must not write it: a resumed session counts by the ordinary
// unfinished rule.
const retiredResumeReservation = "dev-env.haynesops.com/caller-resume-reserved"

func TestCallerPolicyResumeCapacity(t *testing.T) {
	for _, workers := range []bool{false, true} {
		t.Run(fmt.Sprintf("workers=%t", workers), func(t *testing.T) {
			f, token, req := boundedLifecycleFixture(t, workers)
			a := policyCreated(t, f, token, req)
			if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(a), tokHuman, nil); w.Code != 202 {
				t.Fatal(w.Code, w.Body.String())
			}
			b := policyCreated(t, f, token, req)
			e := wantError(t, f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil), 429, apiv1.CodeLimitExceeded)
			if !strings.Contains(e.Message, "one must finish first") || strings.Contains(e.Message, "reservation") {
				t.Fatal(e.Message)
			}
			if f.session(a).Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
				t.Fatal("a refused resume changed the session")
			}
			if err := f.c.Delete(context.Background(), f.session(b)); err != nil {
				t.Fatal(err)
			}
			if w := f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil); w.Code != 202 {
				t.Fatal("available capacity did not permit resume", w.Code, w.Body.String())
			}
			if _, ok := f.session(a).Annotations[retiredResumeReservation]; ok {
				t.Fatal("resume wrote a capacity reservation")
			}
			wantNoConfigMaps(t, f)
			// The resumed session is unfinished, so it holds the only slot.
			wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, req), 429, apiv1.CodeLimitExceeded)
			// Finishing releases it with no reap.
			sess := f.session(a)
			sess.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited}
			if err := f.c.Status().Update(context.Background(), sess); err != nil {
				t.Fatal(err)
			}
			policyCreated(t, f, token, req)
		})
	}
}

// A bounded resume's live count and patch run under the same create lock as
// a create. Pause the resume inside its patch: a create on the same replica
// waits, then counts the resumed session and is refused.
func TestCallerPolicyResumeSerialisedWithCreate(t *testing.T) {
	for _, workers := range []bool{false, true} {
		t.Run(fmt.Sprintf("workers=%t", workers), func(t *testing.T) {
			f, token, req := boundedLifecycleFixture(t, workers)
			a := policyCreated(t, f, token, req)
			if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(a), tokHuman, nil); w.Code != 202 {
				t.Fatal(w.Code, w.Body.String())
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var enter, once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*v1alpha1.AgentSession); ok {
					enter.Do(func() { close(entered) })
					select {
					case <-release:
					case <-time.After(10 * time.Second):
						return context.DeadlineExceeded
					}
				}
				return c.Patch(ctx, obj, patch, opts...)
			}})
			resumed, created := make(chan int, 1), make(chan int, 1)
			go func() { resumed <- f.do(http.MethodPost, apiv1.SessionResumePath(a), tokHuman, nil).Code }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("resume did not reach its patch")
			}
			go func() { created <- f.do(http.MethodPost, apiv1.SessionsPath, token, req).Code }()
			select {
			case code := <-created:
				t.Fatalf("create did not wait for the bounded resume: %d", code)
			case <-time.After(200 * time.Millisecond):
			}
			unblock()
			if code := <-resumed; code != http.StatusAccepted {
				t.Fatal("resume", code)
			}
			if code := <-created; code != http.StatusTooManyRequests {
				t.Fatal("create passed a stale count", code)
			}
		})
	}
}

func TestCallerPolicyScheduledWorkerRecordsParent(t *testing.T) {
	f, _, _ := boundedLifecycleFixture(t, true)
	parent := f.session("bounded-parent")
	d := swDispatch()
	d.Owner = &scheduledwork.SessionRef{Name: parent.Name, UID: parent.UID}
	name, uid, adopted, err := f.srv.CreateScheduledSession(context.Background(), swRequest(), d)
	if err != nil || name == "" || uid == "" || adopted {
		t.Fatal("scheduled worker was not created", name, uid, adopted, err)
	}
	if f.session(name).Annotations[callerParentUID] != string(parent.UID) {
		t.Fatal("scheduled child did not retain its bounded parent identity")
	}
	_, _, adopted, err = f.srv.CreateScheduledSession(context.Background(), swRequest(), d)
	if err != nil || !adopted {
		t.Fatal("retry did not adopt", adopted, err)
	}
	wantNoConfigMaps(t, f)
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

// A lost resume response needs no recovery: nothing is held, and the next
// create is admitted or refused by the live count alone.
func TestCallerPolicyUnknownResumeNeedsNoRecovery(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%t", committed), func(t *testing.T) {
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
			wantNoConfigMaps(t, f)
			if committed {
				wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, token, req), 429, apiv1.CodeLimitExceeded)
				return
			}
			policyCreated(t, f, token, req)
		})
	}
}
