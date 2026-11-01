package controller

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
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
