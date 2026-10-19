package apiserver

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// CreateStandby makes an ordinary remote AgentSession through the API's own
// checks, labelled so the standby runnable finds it again (Story110 T25).
func TestCreateStandby(t *testing.T) {
	f := newFixture(t)
	req := apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "claude", Mode: "remote", Model: "claude-opus-5-5", Effort: "xhigh", Profile: "dev"}
	name, err := f.srv.CreateStandby(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels[v1alpha1.LabelStandby] != "true" || got.Labels[v1alpha1.LabelDepth] != "0" ||
		got.Spec.Mode != v1alpha1.ModeRemote || got.Spec.Agent != v1alpha1.AgentClaude ||
		got.Spec.Model != "claude-opus-5-5" || got.Spec.Parent != StandbyParent {
		t.Errorf("standby session: labels %v spec %+v", got.Labels, got.Spec)
	}
	// A second one in the same second takes the next numbered name.
	if second, err := f.srv.CreateStandby(context.Background(), req); err != nil || second == name {
		t.Errorf("a second standby: %q, %v", second, err)
	}
	// The API's own checks apply: an unknown profile is refused, nothing created.
	bad := req
	bad.Profile = "no-such-profile"
	if _, err := f.srv.CreateStandby(context.Background(), bad); err == nil {
		t.Error("an invalid standby request was accepted")
	}
}
