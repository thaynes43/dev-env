package apiserver

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func TestCanaryResultCaptureAdmissionPreviewDoesNotAssignUID(t *testing.T) {
	for _, common := range []bool{false, true} {
		t.Run(map[bool]string{false: "document", true: "common-file"}[common], func(t *testing.T) {
			f, b := newCanaryFixture(t, "claude", interceptor.Funcs{})
			b.Record.CaptureOutput = true
			s := b.Session.DeepCopy()
			s.Spec.ProjectCanary.CaptureOutput = true
			s.Spec.ProjectCanary.PolicyDigest = b.Record.Digest()
			if common {
				snapshot := commonCanaryAPI(t, f, &b, "SYNTHETIC_PRIVATE_COMMON")
				var err error
				s.Spec.CommonRules, err = snapshot.Binding()
				if err != nil {
					t.Fatal(err)
				}
				s.Spec.ProjectCanary.CommonRevision = b.Record.CommonRevision
				s.Spec.ProjectCanary.PolicyDigest = b.Record.Digest()
			}
			s.UID = ""
			before := s.DeepCopy()
			if err := checkAgentd(s); err != nil || !reflect.DeepEqual(before, s) {
				t.Fatal("preview rejected valid capture or mutated the original", err)
			}
			if err := controller.CheckAgentdSession(s); err == nil {
				t.Fatal("actual runtime accepted a missing Session UID")
			}
			maximum := s.DeepCopy()
			maximum.UID = types.UID(strings.Repeat("x", 128))
			if err := controller.CheckAgentdSession(maximum); err != nil {
				t.Fatal("maximum bounded runtime UID refused", err)
			}
			maximum.UID = types.UID(strings.Repeat("x", 129))
			if err := controller.CheckAgentdSession(maximum); err == nil {
				t.Fatal("unbounded actual UID accepted")
			}
			for _, kind := range []string{"mode", "workspace", "snapshot", "authority"} {
				bad := s.DeepCopy()
				switch kind {
				case "mode":
					bad.Spec.Mode = v1alpha1.ModeLocal
				case "workspace":
					bad.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "shared"}
				case "snapshot":
					bad.Annotations[v1alpha1.AnnotationProjectSnapshot] = "invalid"
				case "authority":
					if !common {
						continue
					}
					bad.Spec.CommonRules.SnapshotDigest = strings.Repeat("0", 64)
				}
				prior := bad.DeepCopy()
				if err := checkAgentd(bad); err == nil || !reflect.DeepEqual(prior, bad) {
					t.Fatal("invalid capture accepted or mutated", kind, err)
				}
			}
		})
	}
}

func TestCanaryResultCaptureFirstCreateReturnsOnlyAssignedOriginal(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			creates := 0
			f, b := newCanaryFixture(t, provider, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				if s, ok := o.(*v1alpha1.AgentSession); ok {
					creates++
					if s.UID != "original-canary" {
						t.Fatal("preview UID reached Create", s.UID)
					}
				}
				return c.Create(ctx, o, opts...)
			}})
			f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				if s, ok := o.(*v1alpha1.AgentSession); ok && s.UID != "" {
					t.Fatal("validation-only UID reached the API Create boundary", s.UID)
				}
				return c.Create(ctx, o, opts...)
			}})
			b.Record.CaptureOutput = true
			commonCanaryAPI(t, f, &b, "SYNTHETIC_PRIVATE_COMMON")
			body := map[string]string{"canaryID": b.Record.ID}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body)
			if w.Code != http.StatusCreated {
				t.Fatal("capture initial admission", w.Code, w.Body.String())
			}
			first := decode[apiv1.Session](t, w)
			original := f.session(b.Record.SessionName())
			if first.UID != "original-canary" || first.UID != string(original.UID) || controller.CheckAgentdSession(original) != nil || strings.Contains(w.Body.String(), strings.Repeat("x", 128)) {
				t.Fatal("assigned original UID not bound", first.UID, original.UID)
			}
			w = f.do(http.MethodPost, apiv1.SessionsPath, tokClient, body)
			if w.Code != http.StatusOK || decode[apiv1.Session](t, w).UID != first.UID || creates != 1 {
				t.Fatal("retained original changed on confirmation", w.Code, w.Body.String(), creates)
			}
		})
	}
}

func TestCanaryResultCaptureAdmissionStillRefusesChangedAuthority(t *testing.T) {
	f, b := newCanaryFixture(t, "claude", interceptor.Funcs{})
	b.Record.CaptureOutput = true
	commonCanaryAPI(t, f, &b, "SYNTHETIC_PRIVATE_COMMON")
	for _, kind := range []string{"catalog", "template", "common"} {
		t.Run(kind, func(t *testing.T) {
			key := f.srv.Projects.Key
			if kind == "template" {
				key = f.srv.PrivateCanaryTemplates
			}
			if kind == "common" {
				key = client.ObjectKey{Namespace: "dev-agents", Name: "dev-env-config-claude"}
			}
			var cm corev1.ConfigMap
			if err := f.c.Get(context.Background(), key, &cm); err != nil {
				t.Fatal(err)
			}
			before := cm.DeepCopy()
			cm.Data = map[string]string{"invalid": "changed"}
			if err := f.c.Update(context.Background(), &cm); err != nil {
				t.Fatal(err)
			}
			w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, map[string]string{"canaryID": b.Record.ID})
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatal("changed capture authority admitted", w.Code, w.Body.String())
			}
			var sessions v1alpha1.AgentSessionList
			if f.c.List(context.Background(), &sessions) != nil || len(sessions.Items) != 0 {
				t.Fatal("refusal created a Session")
			}
			before.ResourceVersion = cm.ResourceVersion
			if err := f.c.Update(context.Background(), before); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func canaryResultFixture(t *testing.T, provider string) (*fixture, *v1alpha1.AgentSession, *coord.PublishedContext, string) {
	t.Helper()
	f, b := newCanaryFixture(t, provider, interceptor.Funcs{})
	enableContexts(f)
	b.Record.CaptureOutput = true
	commonCanaryAPI(t, f, &b, "SYNTHETIC_PRIVATE_COMMON")
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, map[string]string{"canaryID": b.Record.ID})
	if w.Code != http.StatusCreated {
		t.Fatal("seed original", w.Code, w.Body.String())
	}
	s := f.session(b.Record.SessionName())
	s.Status.Phase = v1alpha1.PhaseSuspended
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(s.UID), LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "original-executor-uid", PodName: s.Name, Generation: s.Generation}}
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	snapshot, err := projectcatalog.ParseSnapshot([]byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot]))
	if err != nil {
		t.Fatal(err)
	}
	invocation := apiv1.ManagedInvocation(string(s.UID), "original-executor-uid")
	text := "SYNTHETIC_ASSISTANT_OUTPUT\n"
	spec := coord.PublishedContextSpec{
		Identity: coord.ContextIdentity{SessionUID: string(s.UID), Kind: "transcript-excerpt", Key: apiv1.ManagedOutputKey(invocation, 0)},
		Source:   coord.SessionIdentity{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)}, SourceGeneration: s.Generation, SourceParent: s.Spec.Parent,
		Project: snapshot.Project(), Repository: snapshot.Selected().Name,
		Actor:       coord.ClaimActor{Kind: "session", ServiceAccount: sessionNS + "/" + f.srv.Policy.SessionServiceAccount, PodName: s.Name, PodUID: "original-executor-uid"},
		Provenance:  coord.ClaimProvenance{CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision(), GitHub: snapshot.Selected().GitHub, DefaultBranch: snapshot.Selected().DefaultBranch, InitialPinState: "Unknown"},
		Attribution: coord.ContextAttribution{Provider: provider, RequestedModel: s.Spec.Model, ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"},
		Origin:      "ManagedOutputAssertion", Completeness: "PartialManagedOutput", StopState: "Unknown", Text: text, ContentSHA256: apiv1.ContextHash(text), ByteCount: int32(len(text)),
		ManagedOutput: &coord.ManagedOutputAssertion{Invocation: invocation, Ordinal: 0, ConversationID: "11111111-1111-4111-8111-111111111111"},
	}
	p, _, err := f.srv.PublishedContexts.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	path := apiv1.CanaryResultPath(b.Record.ID) + "?" + url.Values{"sessionUID": {string(s.UID)}, "context": {p.Name}}.Encode()
	return f, s, p, path
}

// Install after seeding fixtures. Every result-handler storage interaction is a
// named read; no source executor or workspace object is available to consult.
func forbidCanaryResultSideEffects(t *testing.T, f *fixture) *int {
	t.Helper()
	reads := new(int)
	deny := func(what string) error { t.Fatalf("result reader performed %s", what); return nil }
	c := interceptor.NewClient(f.c, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			switch obj.(type) {
			case *v1alpha1.AgentSession, *corev1.ConfigMap:
			case *coord.PublishedContext:
				*reads++
			default:
				return deny("Pod/HOME/Job/other object GET")
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return deny("list")
		},
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return deny("create")
		},
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			return deny("update")
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return deny("delete")
		},
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return deny("patch")
		},
	})
	f.srv.Client, f.srv.Live = c, c
	f.srv.PublishedContexts.Client, f.srv.PublishedContexts.Live = c, c
	return reads
}

func TestCanaryResultOriginalStoppedSourceRead(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f, s, p, path := canaryResultFixture(t, provider)
			ex := &fakeExec{}
			f.srv.Exec = ex
			// No Pod or HOME PVC exists in this fixture, and it must not be read.
			reads := forbidCanaryResultSideEffects(t, f)
			w := f.do(http.MethodGet, path, tokClient, nil)
			if w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			got := decode[apiv1.CanaryResult](t, w)
			if got.SessionUID != string(s.UID) || got.CanaryID != s.Spec.ProjectCanary.ID || got.Context.Text != p.Spec.Text || got.Context.Metadata.UID != string(p.UID) || got.Context.Metadata.ManagedOutput.Invocation != p.Spec.ManagedOutput.Invocation || got.Context.Metadata.StopState != "Unknown" || *reads != 1 || len(ex.cmds) != 0 {
				t.Fatal("original stored publication was not read exactly once", got, *reads, ex.cmds)
			}
			if strings.Contains(w.Body.String(), s.Spec.Prompt) || strings.Contains(w.Body.String(), "SYNTHETIC_PRIVATE_COMMON") || strings.Contains(w.Body.String(), "homeClaim") || strings.Contains(w.Body.String(), "templateRevision") {
				t.Fatal("result exposed private or invented publication fields")
			}
			// The dedicated fixed reader does not widen generic context payload ACLs.
			wantError(t, f.do(http.MethodGet, apiv1.PublishedContextsPath+"/"+p.Name+"?project=sample", tokClient, nil), http.StatusForbidden, apiv1.CodeForbidden)
		})
	}
}

func TestCanaryResultSelectionBeforeAuthentication(t *testing.T) {
	f := newFixture(t)
	f.srv.Auth = nil // Any authenticated path would panic rather than pass.
	name := apiv1.ContextName("original-uid", "transcript-excerpt", "key")
	for _, path := range []string{
		apiv1.CanaryResultPath("claude-rules"),
		apiv1.CanaryResultPath("claude-rules") + "?sessionUID=original-uid",
		apiv1.CanaryResultPath("claude-rules") + "?sessionUID=null&context=bad",
		apiv1.CanaryResultPath("claude-rules") + "?sessionUID=original-uid&sessionUID=other&context=" + name,
		apiv1.CanaryResultPath("claude-rules") + "?sessionUID=original-uid&context=" + name + "&project=other",
		apiv1.CanaryResultPath("UPPER") + "?sessionUID=original-uid&context=" + name,
	} {
		w := f.do(http.MethodGet, path, tokClient, nil)
		wantError(t, w, http.StatusBadRequest, apiv1.CodeBadRequest)
	}
	w := f.do(http.MethodPost, apiv1.CanaryResultPath("claude-rules"), tokClient, nil)
	wantError(t, w, http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed)
}

func TestCanaryResultRefusesChangedAuthority(t *testing.T) {
	for _, kind := range []string{"parent", "session-uid", "policy", "catalog-uid", "catalog-rv", "template", "common-source", "generation", "publication-source", "publication-parent", "publication-project", "publication-provider", "executor", "missing-publication", "missing-session", "payload-grant", "canary-off", "publication-off", "retention-off", "common-off", "codex-off", "other-client", "human"} {
		t.Run(kind, func(t *testing.T) {
			f, s, p, path := canaryResultFixture(t, "codex")
			token := tokClient
			switch kind {
			case "parent":
				s.Spec.Parent = "dev/other"
			case "session-uid":
				path = strings.ReplaceAll(path, "sessionUID="+string(s.UID), "sessionUID=other-original")
			case "policy":
				s.Spec.ProjectCanary.PolicyDigest = strings.Repeat("0", 64)
			case "generation":
				s.Generation++
			case "executor":
				s.Status.PrivateHistory.LastExecutor.PodUID = "other-original-pod"
			case "catalog-uid", "catalog-rv", "template", "common-source":
				key := f.srv.Projects.Key
				if kind == "template" {
					key = f.srv.PrivateCanaryTemplates
				}
				if kind == "common-source" {
					key = client.ObjectKey{Namespace: "dev-agents", Name: "dev-env-config-claude"}
				}
				var cm corev1.ConfigMap
				if err := f.c.Get(context.Background(), key, &cm); err != nil {
					t.Fatal(err)
				}
				if kind == "catalog-uid" {
					cm.UID = "replacement-catalog"
				}
				if kind == "template" {
					cm.Data["templates.yaml"] = "invalid"
				}
				if kind == "common-source" {
					cm.UID = "replacement-common"
				}
				if err := f.c.Update(context.Background(), &cm); err != nil {
					t.Fatal(err)
				}
			case "publication-source":
				p.Spec.Source.Name = "foreign-source"
			case "publication-parent":
				p.Spec.SourceParent = "dev/other"
			case "publication-project":
				p.Spec.Project = "other"
				p.Labels["coordination.dev-env.haynesops.com/project"] = "other"
			case "publication-provider":
				p.Spec.Attribution.Provider = "claude"
			case "missing-publication":
				if err := f.c.Delete(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			case "missing-session":
				s.Finalizers = nil
			case "payload-grant":
				f.srv.ContextReadProjects = nil
			case "canary-off":
				f.srv.PrivateProjectCanaries = nil
			case "publication-off":
				f.srv.PublishedContexts = nil
			case "retention-off":
				f.srv.PrivateHistoryRetention = false
			case "common-off":
				f.srv.RetainedCommonRules = false
			case "codex-off":
				f.srv.ManagedCodexTasks = false
			case "other-client":
				token = "tok-other"
				f.auth[token] = saIdentity("dev/other")
				f.srv.Policy.Clients = append(f.srv.Policy.Clients, "dev/other")
			case "human":
				token = tokHuman
			}
			if kind != "executor" {
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.c.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if kind == "executor" {
				stored := f.session(s.Name)
				if stored.Status.PrivateHistory.LastExecutor.PodUID != "other-original-pod" {
					t.Fatal("negative fixture did not persist the changed executor UID")
				}
			}
			if kind == "missing-session" {
				if err := f.c.Delete(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(kind, "publication-") {
				if err := f.c.Update(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			}
			if f.srv.PublishedContexts != nil {
				forbidCanaryResultSideEffects(t, f)
			}
			w := f.do(http.MethodGet, path, token, nil)
			wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
			if strings.Contains(w.Body.String(), p.Spec.Text) {
				t.Fatal("refusal disclosed text")
			}
		})
	}
}

func TestCanaryResultFinalAuthorityFence(t *testing.T) {
	for _, change := range []string{"parent", "payload-grant", "catalog", "common"} {
		t.Run(change, func(t *testing.T) {
			f, s, _, path := canaryResultFixture(t, "claude")
			f.srv.PublishedContexts.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				err := c.Get(ctx, key, o, opts...)
				if err == nil {
					switch change {
					case "parent":
						f.srv.Policy.Clients = nil
					case "payload-grant":
						f.srv.ContextReadProjects = nil
					case "catalog", "common":
						key := f.srv.Projects.Key
						if change == "common" {
							key = client.ObjectKey{Namespace: "dev-agents", Name: "dev-env-config-claude"}
						}
						var cm corev1.ConfigMap
						if err := f.c.Get(ctx, key, &cm); err != nil {
							t.Fatal(err)
						}
						cm.UID = "replacement-authority"
						if err := f.c.Update(ctx, &cm); err != nil {
							t.Fatal(err)
						}
					}
				}
				return err
			}})
			w := f.do(http.MethodGet, path, tokClient, nil)
			wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
			if strings.Contains(w.Body.String(), "SYNTHETIC_ASSISTANT_OUTPUT") {
				t.Fatal("late authority change disclosed text", s.UID)
			}
		})
	}
}

func TestCanaryResultRejectedIdentityDoesNotResolveObjects(t *testing.T) {
	for _, kind := range []string{"session", "coordinator", "human", "other-client", "ambiguous-human", "ambiguous-session", "ambiguous-coordinator"} {
		t.Run(kind, func(t *testing.T) {
			f, _, _, path := canaryResultFixture(t, "claude")
			token := "unexpected-caller"
			id := saIdentity("dev/other")
			switch kind {
			case "session":
				id = saIdentity(f.srv.Policy.SessionNamespace + "/" + f.srv.Policy.SessionServiceAccount)
				id.PodName, id.PodUID = "other-session", "other-pod-uid"
			case "coordinator":
				id = saIdentity("dev/host")
				id.PodName, id.PodUID = "host", "host-pod-uid"
				f.srv.Policy.CoordinatorEnabled = true
				f.srv.Policy.Coordinators = []CoordinatorHost{{ServiceAccount: "dev/host", PodName: "host", HostID: "host"}}
			case "human":
				id = saIdentity(f.srv.Policy.Human)
			case "other-client":
				f.srv.Policy.Clients = append(f.srv.Policy.Clients, "dev/other")
			case "ambiguous-human":
				id = saIdentity("dev/dev-env")
				f.srv.Policy.Human = "dev/dev-env"
			case "ambiguous-session":
				id = saIdentity("dev/dev-env")
				f.srv.Policy.SessionNamespace, f.srv.Policy.SessionServiceAccount = "dev", "dev-env"
			case "ambiguous-coordinator":
				id = saIdentity("dev/dev-env")
				f.srv.Policy.Coordinators = []CoordinatorHost{{ServiceAccount: "dev/dev-env", PodName: "host", HostID: "host"}}
			}
			f.auth[token] = id
			deny := interceptor.NewClient(f.c, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				t.Fatal("rejected identity caused an object lookup before refusal")
				return nil
			}, List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				t.Fatal("rejected identity caused a list")
				return nil
			}})
			f.srv.Client, f.srv.Live = deny, deny
			f.srv.PublishedContexts.Client, f.srv.PublishedContexts.Live = deny, deny
			wantError(t, f.do(http.MethodGet, path, token, nil), http.StatusForbidden, apiv1.CodeForbidden)
		})
	}
}
