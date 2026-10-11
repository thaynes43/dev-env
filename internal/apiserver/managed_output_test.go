package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

func TestManagedOutputTrustedOriginalCanaryAndOfflineRead(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f, binding := newCanaryFixture(t, provider, interceptor.Funcs{})
			enableContexts(f)
			ex := &fakeExec{}
			f.srv.Exec = ex
			record := binding.Record
			record.CaptureOutput = true
			raw, _ := json.Marshal([]projectcanary.Record{record})
			policy, err := projectcanary.Parse(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			f.srv.PrivateProjectCanaries = policy
			token := contextSession(t, f, record.SessionName(), clientSA)
			s := f.session(record.SessionName())
			s.Spec = binding.Session.Spec
			s.Spec.ProjectCanary.CaptureOutput = true
			s.Spec.ProjectCanary.PolicyDigest = record.Digest()
			s.Annotations = binding.Session.Annotations
			s.Labels = binding.Session.Labels
			s.Finalizers = binding.Session.Finalizers
			if err := f.c.Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(s.UID), LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "different-original-pod"}}
			if err := f.c.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			invocation := apiv1.ManagedInvocation(string(s.UID), f.auth[token].PodUID)
			req := apiv1.PublishContextRequest{Kind: "transcript-excerpt", Key: apiv1.ManagedOutputKey(invocation, 0), Text: "synthetic managed stdout", ManagedOutput: &apiv1.ManagedOutputRequest{Ordinal: 0, ConversationID: "11111111-1111-4111-8111-111111111111", Completeness: "PartialManagedOutput"}}
			wantError(t, f.do(http.MethodPost, apiv1.PublishedContextsPath, token, req), http.StatusForbidden, apiv1.CodeForbidden)
			s.Status.PrivateHistory.LastExecutor.PodUID = f.auth[token].PodUID
			if err := f.c.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			w := f.do(http.MethodPost, apiv1.PublishedContextsPath, token, req)
			if w.Code != http.StatusCreated {
				t.Fatal(w.Code, w.Body.String())
			}
			result := decode[apiv1.PublishContextResult](t, w)
			m := result.Metadata
			if m.Origin != "ManagedOutputAssertion" || m.ManagedOutput == nil || m.ManagedOutput.Invocation != invocation || m.Source.UID != string(s.UID) || m.Actor.PodUID != f.auth[token].PodUID || m.Attribution.Provider != provider || m.Attribution.ObservedModelState != "Unknown" || m.StopState != "Unknown" {
				t.Fatal("server assertion provenance", m)
			}
			w = f.do(http.MethodPost, apiv1.PublishedContextsPath, token, req)
			if w.Code != http.StatusOK {
				t.Fatal("immutable retry", w.Code, w.Body.String())
			}
			changed := req
			changed.Text = "changed"
			wantError(t, f.do(http.MethodPost, apiv1.PublishedContextsPath, token, changed), http.StatusConflict, apiv1.CodeConflict)
			bad := req
			bad.Key = apiv1.ManagedOutputKey(invocation, 1)
			wantError(t, f.do(http.MethodPost, apiv1.PublishedContextsPath, token, bad), http.StatusForbidden, apiv1.CodeForbidden)
			reader := contextSession(t, f, "capture-reader-"+provider, humanSA)
			// Removing source objects does not turn an authorized read into source exec.
			s = f.session(record.SessionName())
			s.Finalizers = nil
			if err := f.c.Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if err := f.c.Delete(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			w = f.do(http.MethodGet, apiv1.PublishedContextsPath+"/"+m.Name+"?project=sample", reader, nil)
			if w.Code != http.StatusOK || decode[apiv1.PublishedContext](t, w).Text != req.Text {
				t.Fatal("offline read", w.Code, w.Body.String())
			}
			if len(ex.cmds) != 0 {
				t.Fatal("capture read executed source")
			}
		})
	}
}

func TestManagedOutputCannotBeAssertedByOrdinarySessionOrUnsupportedInput(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	token := contextSession(t, f, "ordinary-output", humanSA)
	s := f.session("ordinary-output")
	id := apiv1.ManagedInvocation(string(s.UID), f.auth[token].PodUID)
	req := apiv1.PublishContextRequest{Kind: "transcript-excerpt", Key: apiv1.ManagedOutputKey(id, 0), Text: "synthetic", ManagedOutput: &apiv1.ManagedOutputRequest{ConversationID: "11111111-1111-4111-8111-111111111111", Completeness: "Unknown"}}
	wantError(t, f.do(http.MethodPost, apiv1.PublishedContextsPath, token, req), http.StatusForbidden, apiv1.CodeForbidden)
	req.ManagedOutput.Ordinal = 4
	wantError(t, f.do(http.MethodPost, apiv1.PublishedContextsPath, token, req), http.StatusBadRequest, apiv1.CodeBadRequest)
	req.ManagedOutput.Ordinal = 0
	req.ManagedOutput.Completeness = "Complete"
	wantError(t, f.do(http.MethodPost, apiv1.PublishedContextsPath, token, req), http.StatusBadRequest, apiv1.CodeBadRequest)
}
