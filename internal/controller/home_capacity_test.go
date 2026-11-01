package controller

import (
	"os"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestHomeCapacityChangesNewClaimsWithoutRevisionDrain(t *testing.T) {
	old := exampleTemplates(t)
	doc, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	larger, err := templates.Parse(map[string]string{templates.Key: strings.Replace(string(doc), "size: 20Gi", "size: 100Gi", 1)})
	if err != nil {
		t.Fatal(err)
	}
	s := taskSession()
	s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentIdle}
	oldPod, err := buildPod(s, old, "")
	if err != nil {
		t.Fatal(err)
	}
	newPod, err := buildPod(s, larger, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oldPod, newPod) {
		t.Fatal("HOME capacity changed the pod, including its revision")
	}
	if drainEligible(s, readyDrainPod(s.Name, old.Revision()), larger.Revision()) {
		t.Fatal("a capacity-only change made a current idle pod eligible to drain")
	}
	claim, err := buildHomeClaim(s, larger)
	if err != nil {
		t.Fatal(err)
	}
	if got := claim.Spec.Resources.Requests.Storage().String(); got != "100Gi" {
		t.Fatalf("new claim capacity %s, want 100Gi", got)
	}
}

func TestHomeCapacityKeepsReviewedExecutorIdentity(t *testing.T) {
	canary := testutil.New(t, "codex", "dev/dev-env")
	canary.Session.CreationTimestamp = metav1.Now()
	canary.Session.Spec.SessionDirectory = &v1alpha1.SessionDirectoryEnrollment{Version: 1, Project: canary.Record.Project}
	pilot := testutil.NewPilot(t)
	for name, fixture := range map[string]struct {
		s *v1alpha1.AgentSession
		t *templates.Templates
	}{
		"canary": {canary.Session, canary.Templates},
		"pilot":  {pilot.Session(t, 0), pilot.Templates},
	} {
		t.Run(name, func(t *testing.T) {
			pod, err := buildManagedPod(fixture.s, fixture.t, "https://synthetic.invalid", true, false, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			directory, err := sessiondirectory.Spec(fixture.s)
			if err != nil {
				t.Fatal(err)
			}
			if got := pod.Labels[v1alpha1.LabelRevision]; got != string(directory.Revisions.Template) {
				t.Fatalf("generated pod revision %s differs from reviewed directory identity %s", got, directory.Revisions.Template)
			}
			if pod.Labels[v1alpha1.LabelRevision] == fixture.t.Revision() {
				t.Fatal("non20Gi reviewed fixture unexpectedly used normalized ordinary revision")
			}
		})
	}
}
