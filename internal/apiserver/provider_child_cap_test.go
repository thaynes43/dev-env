package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestProviderChildCapAdmission(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("parent", "dev", 0)
	bindCatalogFixture(t, f)
	for _, agent := range []string{"claude", "codex"} {
		req := coordinatorTask()
		req.Agent = agent
		if agent == "codex" {
			req.Model, req.Effort = "gpt-6.1-terra", "medium"
		}
		for i := 0; i < 4; i++ {
			f.now = f.now.Add(time.Second)
			w := f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
			if w.Code != http.StatusCreated {
				t.Fatalf("%s child %d: %d %s", agent, i+1, w.Code, w.Body.String())
			}
		}
		e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tok, req), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		if !strings.Contains(e.Message, agent) {
			t.Fatalf("missing agent: %s", e.Message)
		}
	}
}

func TestProviderChildCapStoredRecords(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*v1alpha1.AgentSession)
		counts bool
	}{
		{"same", func(s *v1alpha1.AgentSession) {}, true},
		{"unknown", func(s *v1alpha1.AgentSession) { s.Spec.Agent = "grok" }, true},
		{"empty", func(s *v1alpha1.AgentSession) { s.Spec.Agent = "" }, true},
		{"other agent", func(s *v1alpha1.AgentSession) { s.Spec.Agent = v1alpha1.AgentOpencode }, false},
		{"other parent", func(s *v1alpha1.AgentSession) { s.Spec.Parent = "other" }, false},
		{"other namespace", func(s *v1alpha1.AgentSession) { s.Namespace = "other" }, false},
		{"suspended mode", func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended }, false},
		{"suspended phase", func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhaseSuspended }, false},
		{"archived", func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhaseArchived }, false},
		{"failed phase", func(s *v1alpha1.AgentSession) { s.Status.Phase = v1alpha1.PhaseFailed }, false},
		{"deleting", func(s *v1alpha1.AgentSession) {
			now := metav1.Now()
			s.DeletionTimestamp = &now
			s.Finalizers = []string{"test"}
		}, false},
		{"exited", func(s *v1alpha1.AgentSession) { s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited} }, false},
		{"failed agent", func(s *v1alpha1.AgentSession) { s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentFailed} }, false},
		{"interrupted", func(s *v1alpha1.AgentSession) {
			s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentInterrupted}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objects := make([]client.Object, 0, 4)
			for i := 0; i < 4; i++ {
				s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("child-%d", i), Namespace: sessionNS}, Spec: v1alpha1.AgentSessionSpec{Parent: "parent", Agent: v1alpha1.AgentClaude}}
				tc.mutate(s)
				objects = append(objects, s)
			}
			f := newFixture(t, objects...)
			tok := f.sessionPod("parent", "dev", 0)
			w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task())
			if tc.counts {
				e := wantError(t, w, http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
				if !strings.Contains(e.Message, "claude") {
					t.Fatalf("missing canonical agent: %s", e.Message)
				}
			} else if w.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestProviderChildCapModesAndUnknownRequests(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("parent", "dev", 0)
	for _, mode := range []string{"task", "local", "remote", "task"} {
		req := task()
		req.Mode = mode
		if mode != "task" {
			req.Prompt = ""
		}
		f.now = f.now.Add(time.Second)
		w := f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", mode, w.Code, w.Body.String())
		}
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tok, task()), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	for _, agent := range []string{"grok", "cursor", "Claude", "opencode"} {
		req := task()
		req.Agent = agent
		wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tok, req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	}
}

func TestProviderChildCapConcurrentSingleServer(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("parent", "dev", 0)
	for i := 0; i < 3; i++ {
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("seed-%d", i), Namespace: sessionNS}, Spec: v1alpha1.AgentSessionSpec{Parent: "parent", Agent: v1alpha1.AgentClaude}}
		if err := f.c.Create(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; results <- f.do(http.MethodPost, apiv1.SessionsPath, tok, task()).Code }()
	}
	close(start)
	a, b := <-results, <-results
	if !((a == http.StatusCreated && b == http.StatusTooManyRequests) || (b == http.StatusCreated && a == http.StatusTooManyRequests)) {
		t.Fatalf("concurrent codes: %d, %d", a, b)
	}
}
