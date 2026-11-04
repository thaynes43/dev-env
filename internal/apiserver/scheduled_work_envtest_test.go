package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/scheduledwork/authority"
)

// The scheduled-work routes against a real kube-apiserver with the CRDs this
// commit generates, called with real ServiceAccount tokens that the API
// reviews (TokenReview): the owner and the approver come from the token, the
// approval's template digest is kept by the installed CRD, and the approve
// route's schema preflight reads that CRD with the operator's uncached
// reader.
func TestEnvtestScheduledWorkRoutes(t *testing.T) {
	l := startLiveWith(t, func(s *Server) { s.ScheduledWork = ScheduledWorkAPI(true, swNS) })
	ctx := context.Background()
	aud := []string{apiv1.TokenAudience}
	human := l.token(humanSA, aud, nil)

	// A session and its pod, as the reconciler builds them, to call as.
	sess := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "sw-owner-envtest", Namespace: sessionNS, Labels: map[string]string{v1alpha1.LabelDepth: "0"}},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA},
	}
	if err := l.k8s.Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: sess.Name, Namespace: sessionNS, Labels: map[string]string{v1alpha1.LabelProfile: "dev"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sess, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "agent"}}},
	}
	if err := l.k8s.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: swNS, Name: authority.TemplatesConfigMap}, Data: map[string]string{authority.TemplatesKey: swTemplatesDoc}}
	if err := l.k8s.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var list v1alpha1.ScheduledWorkList
		if err := l.k8s.List(ctx, &list, client.InNamespace(swNS)); err == nil {
			for i := range list.Items {
				_ = l.k8s.Delete(ctx, &list.Items[i])
			}
		}
		_ = l.k8s.Delete(ctx, cm)
		_ = l.k8s.Delete(ctx, pod)
		_ = l.k8s.Delete(ctx, sess)
	})
	agent := l.token(sessionNS+"/"+controller.ServiceAccountName, aud, pod)
	owner := "session/" + sess.Name + "/" + string(sess.UID)

	req := newWork()
	req.Due = time.Now().Add(48 * time.Hour).In(mustZone(t, "America/New_York")).Format("2006-01-02T15:04")
	req.NotAfter = time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	created := unmarshal[apiv1.ScheduledWork](t, l.want(http.MethodPost, apiv1.ScheduledWorkPath, agent, req, http.StatusCreated))
	var stored v1alpha1.ScheduledWork
	if err := l.k8s.Get(ctx, client.ObjectKey{Namespace: swNS, Name: created.Name}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Owner != owner || stored.Spec.Editor != owner || created.SpecHash != authority.SpecHash(stored.Spec) {
		t.Fatalf("stored owner %q editor %q; view hash %s, stored %s", stored.Spec.Owner, stored.Spec.Editor, created.SpecHash, authority.SpecHash(stored.Spec))
	}
	// A replay over the real API server answers the same item.
	if again := unmarshal[apiv1.ScheduledWork](t, l.want(http.MethodPost, apiv1.ScheduledWorkPath, agent, req, http.StatusOK)); again.Name != created.Name {
		t.Fatalf("a replay made %s", again.Name)
	}
	approve := apiv1.ApproveScheduledWorkRequest{SpecHash: created.SpecHash}
	l.want(http.MethodPost, apiv1.ScheduledWorkApprovePath(created.Name), agent, approve, http.StatusForbidden)
	l.want(http.MethodPost, apiv1.ScheduledWorkRetryPath(created.Name), agent, apiv1.RetryScheduledWorkRequest{At: time.Now(), SpecHash: created.SpecHash}, http.StatusForbidden)
	l.want(http.MethodPost, apiv1.ScheduledWorkApprovePath(created.Name), l.token(humanSA, nil, nil), approve, http.StatusUnauthorized)
	l.want(http.MethodPost, apiv1.ScheduledWorkApprovePath(created.Name), human, apiv1.ApproveScheduledWorkRequest{SpecHash: strings.Repeat("ab", 32)}, http.StatusConflict)

	v := unmarshal[apiv1.ScheduledWork](t, l.want(http.MethodPost, apiv1.ScheduledWorkApprovePath(created.Name), human, approve, http.StatusOK))
	if err := l.k8s.Get(ctx, client.ObjectKey{Namespace: swNS, Name: created.Name}, &stored); err != nil {
		t.Fatal(err)
	}
	ts, err := authority.ParseTemplates(cm.Data)
	if err != nil {
		t.Fatal(err)
	}
	digest := authority.TemplateDigest(ts["ops-audit"])
	if a := stored.Status.Approval; a == nil || a.By != humanPrincipal || a.SpecHash != created.SpecHash || a.TemplateDigest != digest {
		t.Fatalf("stored approval %+v, want by %s and digest %s", a, humanPrincipal, digest)
	}
	if v.Approval == nil || v.Approval.TemplateDigest != digest {
		t.Fatalf("view approval %+v", v.Approval)
	}

	// The owner's edit voids it: the editor stays the owner, and the old
	// hash is refused.
	title := "edited by the owner"
	edited := unmarshal[apiv1.ScheduledWork](t, l.want(http.MethodPatch, apiv1.ScheduledWorkItemPath(created.Name), agent, apiv1.EditScheduledWorkRequest{Title: &title}, http.StatusOK))
	if edited.Approval == nil || edited.Approval.SpecHash == edited.SpecHash || edited.Spec.Editor != owner {
		t.Fatalf("edit: %+v", edited)
	}
	l.want(http.MethodPost, apiv1.ScheduledWorkApprovePath(created.Name), human, approve, http.StatusConflict)

	t.Run("an older CRD without approval.templateDigest fails the approval closed", func(t *testing.T) {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(scheduledWorkCRDKind)
		if err := l.k8s.Get(ctx, client.ObjectKey{Name: scheduledWorkCRDName}, u); err != nil {
			t.Fatal(err)
		}
		saved, _, _ := unstructured.NestedSlice(u.Object, "spec", "versions")
		old := withoutApprovalDigest(t, u)
		if err := l.k8s.Update(ctx, old); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for range 50 {
				cur := &unstructured.Unstructured{}
				cur.SetGroupVersionKind(scheduledWorkCRDKind)
				if err := l.k8s.Get(ctx, client.ObjectKey{Name: scheduledWorkCRDName}, cur); err == nil {
					if err := unstructured.SetNestedSlice(cur.Object, saved, "spec", "versions"); err == nil && l.k8s.Update(ctx, cur) == nil {
						return
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Error("the ScheduledWork CRD was not restored")
		})
		var prior v1alpha1.ScheduledWork
		if err := l.k8s.Get(ctx, client.ObjectKey{Namespace: swNS, Name: created.Name}, &prior); err != nil {
			t.Fatal(err)
		}
		current := approve
		current.SpecHash = edited.SpecHash
		e := unmarshal[apiv1.ErrorResponse](t, l.want(http.MethodPost, apiv1.ScheduledWorkApprovePath(created.Name), human, current, http.StatusServiceUnavailable)).Error
		if e.Code != apiv1.CodeUnavailable || !strings.Contains(e.Message, "templateDigest") {
			t.Errorf("refusal %+v", e)
		}
		var after v1alpha1.ScheduledWork
		if err := l.k8s.Get(ctx, client.ObjectKey{Namespace: swNS, Name: created.Name}, &after); err != nil {
			t.Fatal(err)
		}
		if after.ResourceVersion != prior.ResourceVersion || after.Status.Approval == nil || after.Status.Approval.SpecHash != created.SpecHash {
			t.Errorf("a refused approval wrote: %+v", after.Status.Approval)
		}
	})
}

// With the flag off, the routes answer 404 over the real API too.
func TestEnvtestScheduledWorkRoutesOff(t *testing.T) {
	l := startLive(t)
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)
	l.want(http.MethodGet, apiv1.ScheduledWorkPath, human, nil, http.StatusNotFound)
	l.want(http.MethodPost, apiv1.ScheduledWorkPath, human, newWork(), http.StatusNotFound)
	l.want(http.MethodPost, apiv1.ScheduledWorkApprovePath("sched-a"), human, apiv1.ApproveScheduledWorkRequest{SpecHash: strings.Repeat("ab", 32)}, http.StatusNotFound)
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// approve --at and retry against the real CRD's rules: Parked to Pending
// with attempt+1 and retriedAt is admitted three times, and a fourth retry is
// refused by the API before the schema would be asked. (Adopted from the
// independent review's probe.)
func TestEnvtestScheduledWorkRetryAgainstTheCRD(t *testing.T) {
	l := startLiveWith(t, func(s *Server) { s.ScheduledWork = ScheduledWorkAPI(true, swNS) })
	ctx := context.Background()
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: swNS, Name: authority.TemplatesConfigMap}, Data: map[string]string{authority.TemplatesKey: swTemplatesDoc}}
	if err := l.k8s.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var list v1alpha1.ScheduledWorkList
		if err := l.k8s.List(ctx, &list, client.InNamespace(swNS)); err == nil {
			for i := range list.Items {
				_ = l.k8s.Delete(ctx, &list.Items[i])
			}
		}
		_ = l.k8s.Delete(ctx, cm)
	})
	req := newWork()
	req.Key = "envtest-retry"
	req.Due = time.Now().Add(48 * time.Hour).In(mustZone(t, "America/New_York")).Format("2006-01-02T15:04")
	req.NotAfter = time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	created := unmarshal[apiv1.ScheduledWork](t, l.want(http.MethodPost, apiv1.ScheduledWorkPath, human, req, http.StatusCreated))
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Minute)
	read := func() v1alpha1.ScheduledWork {
		t.Helper()
		var sw v1alpha1.ScheduledWork
		if err := l.k8s.Get(ctx, client.ObjectKey{Namespace: swNS, Name: created.Name}, &sw); err != nil {
			t.Fatal(err)
		}
		return sw
	}
	// park parks the occurrence again, as the executor would.
	park := func() {
		t.Helper()
		sw := read()
		if len(sw.Status.Occurrences) == 0 {
			sw.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(at), State: v1alpha1.OccurrenceParked, Reason: "no standing authority"}}
		} else {
			sw.Status.Occurrences[0].State = v1alpha1.OccurrenceParked
			sw.Status.Occurrences[0].Reason = "parked again"
		}
		if err := l.k8s.Status().Update(ctx, &sw); err != nil {
			t.Fatal(err)
		}
	}
	park()
	l.want(http.MethodPost, apiv1.ScheduledWorkApprovePath(created.Name), human, apiv1.ApproveScheduledWorkRequest{SpecHash: created.SpecHash, At: &at}, http.StatusOK)
	if sw := read(); sw.Status.Occurrences[0].State != v1alpha1.OccurrencePending || sw.Status.Occurrences[0].Attempt != 1 || sw.Status.Occurrences[0].RetriedAt == nil || sw.Status.Approval == nil {
		t.Fatalf("after approve --at: %+v", sw.Status)
	}
	for want := int32(2); want <= 3; want++ {
		park()
		l.want(http.MethodPost, apiv1.ScheduledWorkRetryPath(created.Name), human, apiv1.RetryScheduledWorkRequest{At: at, SpecHash: created.SpecHash}, http.StatusOK)
		if o := read().Status.Occurrences[0]; o.State != v1alpha1.OccurrencePending || o.Attempt != want || o.RetriedAt == nil {
			t.Fatalf("after retry %d: %+v", want, o)
		}
	}
	park()
	l.want(http.MethodPost, apiv1.ScheduledWorkRetryPath(created.Name), human, apiv1.RetryScheduledWorkRequest{At: at, SpecHash: created.SpecHash}, http.StatusConflict)
	if o := read().Status.Occurrences[0]; o.State != v1alpha1.OccurrenceParked || o.Attempt != 3 {
		t.Fatalf("a fourth retry changed it: %+v", o)
	}
}
