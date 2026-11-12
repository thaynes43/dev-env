package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// Real TokenReview, live UID binding, status CAS and installed CRD; no provider
// launch or live-cluster call. The suite supplies the operator's pod creation.
func TestSummonOutcomeLiveReport(t *testing.T) {
	now := time.Date(2026, 11, 12, 5, 30, 0, 0, time.UTC)
	l := startLiveWith(t, func(s *Server) { s.Now = func() time.Time { return now } })
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)
	for _, verb := range []string{"done", "failed"} {
		t.Run(verb, func(t *testing.T) {
			policy := summonTestPolicy()
			entry := &policy.Spec.Summon.Sessions[0]
			snapshot, err := summonSnapshot(policy, entry, "outcome-proof")
			if err != nil {
				t.Fatal(err)
			}
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "rem-outcome-" + string(uuid.NewUUID())[:8], Namespace: sessionNS}, Spec: v1alpha1.AgentSessionSpec{
				Repo: entry.Repo, Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask, Model: entry.Model.Default, Profile: entry.Profile, Prompt: "synthetic outcome proof", Caller: policy.Name, Lane: entry.Lane, Parent: policy.Spec.Summon.ServiceAccount, Summon: snapshot,
			}}
			if err := l.k8s.Create(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "synthetic"}}}}
			if err := l.k8s.Create(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			token := l.token(sessionNS+"/"+controller.ServiceAccountName, []string{apiv1.TokenAudience}, p)
			path := apiv1.SessionOutcomePath(s.Name)
			note := strings.Repeat("é", 512)
			if verb == "failed" {
				note = strings.Repeat("a", 1007)
			}
			l.want(http.MethodPost, path, human, reportBody(verb, note), http.StatusForbidden)
			first := unmarshal[apiv1.Outcome](t, l.want(http.MethodPost, path, token, reportBody(verb, note), http.StatusOK))
			if err := l.k8s.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			requireClosed(t, s, "report", verb, verb == "failed")
			if len(s.Status.Outcome.Note) != 1024 || first.Note != s.Status.Outcome.Note {
				t.Fatal("stored note lost bytes")
			}
			rv := s.ResourceVersion
			now = now.Add(time.Minute)
			l.want(http.MethodPost, path, token, reportBody(verb, note), http.StatusOK)
			conflict := unmarshal[apiv1.Outcome](t, l.want(http.MethodPost, path, token, reportBody("working", "late"), http.StatusConflict))
			if conflict.State != first.State || conflict.Note != first.Note || !conflict.At.Equal(*first.At) {
				t.Fatal("conflict lost stored outcome")
			}
			if err := l.k8s.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			if rv != s.ResourceVersion {
				t.Fatal("repeat/conflict wrote status")
			}
		})
	}
}
