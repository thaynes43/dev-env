package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func commonCanaryAPI(t *testing.T, f *fixture, b *testutil.Fixture, text string) commonrules.Snapshot {
	t.Helper()
	snapshot := commonfixture.Snapshot(t, []byte(b.Session.Annotations[v1alpha1.AnnotationProjectSnapshot]), text, "CODEx_HEADER")
	b.Record.CommonRevision = snapshot.CommonRevision
	raw, _ := json.Marshal([]projectcanary.Record{b.Record})
	policy, err := projectcanary.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	f.srv.PrivateProjectCanaries = policy
	f.srv.RetainedCommonRules = true
	a, h := commonfixture.Sources(text, "CODEx_HEADER")
	for _, o := range []client.Object{a, h} {
		o.SetResourceVersion("")
		if err := f.c.Create(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func TestCommonCanaryLostACKRetainsOriginalWithoutRecapture(t *testing.T) {
	sessions, sourceReads := 0, 0
	f, b := newCanaryFixture(t, "claude", interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if _, ok := o.(*v1alpha1.AgentSession); ok {
			sessions++
			if err := c.Create(ctx, o, opts...); err != nil {
				return err
			}
			return errors.New("synthetic lost ACK")
		}
		return c.Create(ctx, o, opts...)
	}, Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if _, ok := o.(*corev1.ConfigMap); ok && key.Namespace == commonrules.Namespace {
			sourceReads++
		}
		return c.Get(ctx, key, o, opts...)
	}})
	accepted := commonCanaryAPI(t, f, &b, "COMMON_ONLY_IN_SOURCE")
	body := map[string]string{"canaryID": b.Record.ID}
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body)
	if w.Code != http.StatusOK || sessions != 1 {
		t.Fatal(w.Code, w.Body.String(), sessions)
	}
	var original v1alpha1.AgentSession
	key := client.ObjectKey{Namespace: sessionNS, Name: b.Record.SessionName()}
	if err := f.c.Get(context.Background(), key, &original); err != nil {
		t.Fatal(err)
	}
	retained, err := commonrules.BoundSession(&original)
	if err != nil || retained.CommonRevision != accepted.CommonRevision {
		t.Fatal("original snapshot lost", err)
	}
	if strings.Contains(original.Spec.Prompt, "COMMON_ONLY_IN_SOURCE") {
		t.Fatal("instruction marker leaked into prompt")
	}
	for _, name := range []string{commonrules.CommonName, commonrules.HeaderName} {
		var cm corev1.ConfigMap
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: commonrules.Namespace, Name: name}, &cm); err != nil {
			t.Fatal(err)
		}
		if err := f.c.Delete(context.Background(), &cm); err != nil {
			t.Fatal(err)
		}
	}
	beforeReads := sourceReads
	original.Status.Phase = v1alpha1.PhaseSuspended
	if err := f.c.Status().Update(context.Background(), &original); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body)
	if w.Code != http.StatusOK || sessions != 1 || sourceReads != beforeReads {
		t.Fatal("terminal retry recaptured source", w.Code, sessions, sourceReads, w.Body.String())
	}
	var kept v1alpha1.AgentSession
	_ = f.c.Get(context.Background(), key, &kept)
	if kept.UID != original.UID || kept.Spec.CommonRules.Snapshot != original.Spec.CommonRules.Snapshot {
		t.Fatal("replaced retained common input")
	}
}

func TestCommonCanaryRefusesBeforeSessionCreate(t *testing.T) {
	for _, kind := range []string{"disabled", "revision", "authority-drift", "escaped-size", "raw-size"} {
		t.Run(kind, func(t *testing.T) {
			sessions, commonReads := 0, 0
			f, b := newCanaryFixture(t, "claude", interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				if _, ok := o.(*v1alpha1.AgentSession); ok {
					sessions++
				}
				return c.Create(ctx, o, opts...)
			}, Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				err := c.Get(ctx, key, o, opts...)
				if cm, ok := o.(*corev1.ConfigMap); ok && key.Namespace == commonrules.Namespace && key.Name == commonrules.CommonName {
					commonReads++
					if kind == "authority-drift" && commonReads > 1 {
						cm.UID = "replacement-source"
					}
				}
				return err
			}})
			text := "COMMON_RULE"
			if kind == "escaped-size" {
				text = strings.Repeat("\x01", commonrules.MaxCommonBytes)
			}
			commonCanaryAPI(t, f, &b, text)
			if kind == "disabled" {
				f.srv.RetainedCommonRules = false
			}
			if kind == "revision" {
				b.Record.CommonRevision = strings.Repeat("a", 64)
				raw, _ := json.Marshal([]projectcanary.Record{b.Record})
				f.srv.PrivateProjectCanaries, _ = projectcanary.Parse(string(raw))
			}
			if kind == "raw-size" {
				var cm corev1.ConfigMap
				key := client.ObjectKey{Namespace: commonrules.Namespace, Name: commonrules.CommonName}
				_ = f.c.Get(context.Background(), key, &cm)
				cm.Data[commonrules.CommonKey] = strings.Repeat("a", commonrules.MaxCommonBytes+1)
				if err := f.c.Update(context.Background(), &cm); err != nil {
					t.Fatal(err)
				}
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, map[string]string{"canaryID": b.Record.ID})
			if w.Code < 400 || sessions != 0 {
				t.Fatal("invalid common route admitted Session", w.Code, sessions, w.Body.String())
			}
			if kind == "disabled" && commonReads != 0 {
				t.Fatal("disabled route read common authority")
			}
		})
	}
}
