package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Change only the later uncached observation, modeling a name replacement or
// changed owner after admission without a real workload or concurrent writer.
type changedChildReader struct {
	client.Reader
	kind                 string
	childReads, podReads int
}

func (r *changedChildReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, object, opts...); err != nil {
		return err
	}
	if key.Name != "own-task" {
		return nil
	}
	switch value := object.(type) {
	case *v1alpha1.AgentSession:
		r.childReads++
		if r.childReads > 1 {
			switch r.kind {
			case "session-uid":
				value.UID = "replacement-session"
			case "session-version":
				value.ResourceVersion = "changed-version"
			case "parent":
				value.Spec.Parent = "foreign-parent"
			}
		}
	case *corev1.Pod:
		r.podReads++
		if r.podReads > 1 {
			switch r.kind {
			case "pod-uid":
				value.UID = "replacement-pod"
			case "pod-owner":
				value.OwnerReferences[0].UID = "replacement-session"
			case "pod-service-account":
				value.Spec.ServiceAccountName = "foreign-executor"
			}
		}
	}
	return nil
}

func TestCoordinatorExecRechecksIdentityAndSendsTargetUIDFences(t *testing.T) {
	for _, route := range []string{"log", "messages"} {
		for _, kind := range []string{"current", "session-uid", "session-version", "parent", "pod-uid", "pod-owner", "pod-service-account"} {
			t.Run(route+"/"+kind, func(t *testing.T) {
				f := coordinatorFixture(t)
				f.sessionPod("own-task", "dev", 0)
				sess := f.session("own-task")
				sess.Spec.Parent = coordinatorSA
				if err := f.c.Update(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
				f.runPod("own-task")
				var pod corev1.Pod
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: sess.Name}, &pod); err != nil {
					t.Fatal(err)
				}
				ex := &fakeExec{out: "synthetic log"}
				f.srv.Exec = ex
				f.srv.Live = &changedChildReader{Reader: f.c, kind: kind}
				method, code := http.MethodGet, http.StatusOK
				var body any
				if route == "messages" {
					method, code = http.MethodPost, http.StatusAccepted
					body = apiv1.MessageRequest{Text: "recorded synthetic answer"}
				}
				w := f.do(method, apiv1.SessionPath(sess.Name)+"/"+route, tokCoordinator, body)
				if kind != "current" {
					wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
					if len(ex.cmds) != 0 {
						t.Fatal("changed child identity reached pods/exec")
					}
					return
				}
				fence := "--expected-pod-uid " + string(pod.UID) + " --expected-session-uid " + string(sess.UID)
				if w.Code != code || len(ex.cmds) != 1 || !strings.HasSuffix(ex.cmds[0], fence) {
					t.Fatal("coordinator exec omitted exact private target UID fences")
				}
			})
		}
	}
}
