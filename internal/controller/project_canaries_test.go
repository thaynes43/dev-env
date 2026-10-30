package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func TestProjectCanaryPodWholeExecutorBounds(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := testutil.New(t, provider, "dev/dev-env")
			pod, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, true, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if pod.Spec.RestartPolicy != corev1.RestartPolicyNever || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 600 || pod.Spec.TerminationGracePeriodSeconds == nil || *pod.Spec.TerminationGracePeriodSeconds != 5 || pod.Annotations["k8tz.io/inject"] != "false" || len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.EphemeralContainers) != 0 || pod.Spec.Containers[0].Image != f.Record.Image {
				t.Fatal("canary lost whole-Pod finite bounds")
			}
			for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
				request, limit := pod.Spec.Containers[0].Resources.Requests[name], pod.Spec.Containers[0].Resources.Limits[name]
				if request.Sign() <= 0 || limit.Sign() <= 0 {
					t.Fatal("unbounded canary resources")
				}
			}
			ownPVC := 0
			for _, v := range pod.Spec.Volumes {
				if v.PersistentVolumeClaim != nil {
					ownPVC++
					if v.Name != "home" || v.PersistentVolumeClaim.ClaimName != HomeClaimName(f.Session.Name) {
						t.Fatal("canary exposed another PVC")
					}
				}
			}
			if ownPVC != 1 || slices.ContainsFunc(pod.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == "shared" || m.MountPath == "/home/dev/.shared" }) {
				t.Fatal("canary still exposes shared scratch")
			}
			if envOf(pod.Spec.Containers[0], "AGENTD_WORKSPACE_ID").Value != "" || slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == "workspace" }) {
				t.Fatal("canary acquired shared project storage")
			}
			if provider == "codex" {
				if envOf(pod.Spec.Containers[0], "AGENTD_CODEX_ACCESS_FILE").Value != projectcanary.CodexAccessPath+"/access.json" {
					t.Fatal("missing access-only projection")
				}
				if _, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", false, false, nil, false); err == nil {
					t.Fatal("disabled managed Codex rendered")
				}
			}
			f.Session.Spec.Prompt += "caller change"
			if _, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, false, nil, false); err == nil {
				t.Fatal("foreign bound prompt rendered")
			}
		})
	}
}

// The canary's evidence is the agent reporting the rule markers it loaded, so
// the operator must hand them over only as rules, never as the prompt: a
// marker in what the agent is told to do proves nothing about what it loaded.
// A record whose own prompt repeats them is refused at admission
// (projectcanary.CheckPrompt, through ResolveCatalog and CheckCommon). This
// renders the pod, alone and with a retained common snapshot, and checks every
// value the agent's process receives against every line of the bound rules,
// common text and Codex header, so it fails when the operator's rendering puts
// one where the agent reads instructions.
func TestProjectCanaryRenderedPromptAndArgsCarryNoRuleLine(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, withCommon := range []bool{false, true} {
			name := provider
			if withCommon {
				name += "/common"
			}
			t.Run(name, func(t *testing.T) {
				f := testutil.New(t, provider, "dev/dev-env")
				snapshot, err := projectcatalog.ParseSnapshot([]byte(f.Session.Annotations[v1alpha1.AnnotationProjectSnapshot]))
				if err != nil {
					t.Fatal(err)
				}
				lines := ruleLines(snapshot.Rules())
				if len(lines) == 0 {
					t.Fatal("the fixture binds no rule line, so this proves nothing")
				}
				if withCommon {
					common := commonCanaryControllerWith(t, &f, "COMMON_RENDER_MARKER\nKeep the common rules out of the prompt.", "HEADER_RENDER_MARKER\nCodex reads this header first.")
					lines = append(append(lines, ruleLines(common.Common.Text)...), ruleLines(common.Header.Text)...)
				}
				pod, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, true, nil, false)
				if err != nil {
					t.Fatal(err)
				}
				c := pod.Spec.Containers[0]
				var doc protocol.Session
				if withCommon {
					if doc, err = protocol.ParseSessionFile([]byte(pod.Annotations[protocol.SessionInputAnnotation])); err != nil {
						t.Fatal(err)
					}
					if envOf(c, protocol.SessionEnv).Value != "" {
						t.Fatal("the common route still passes the session in the environment")
					}
				} else if err := json.Unmarshal([]byte(envOf(c, protocol.SessionEnv).Value), &doc); err != nil {
					t.Fatal(err)
				}
				if doc.Prompt == "" || doc.Prompt != f.Record.Prompt {
					t.Fatalf("rendered prompt %q is not the bound record's", doc.Prompt)
				}
				delivered, err := projectcatalog.ParseSnapshot(doc.ProjectSnapshot)
				if err != nil || delivered.Rules() != snapshot.Rules() {
					t.Fatal("the rules do not reach agentd through the bound snapshot")
				}
				if withCommon && len(doc.CommonRules) == 0 {
					t.Fatal("the common rules do not reach agentd through the bound snapshot")
				}
				values := map[string]string{"prompt": doc.Prompt}
				for i, v := range append(slices.Clone(c.Command), c.Args...) {
					values[fmt.Sprintf("argv[%d]", i)] = v
				}
				for _, e := range c.Env {
					if e.Name != protocol.SessionEnv {
						values["env "+e.Name] = e.Value
					}
				}
				for where, v := range values {
					for _, line := range lines {
						if strings.Contains(v, line) {
							t.Errorf("%s carries rule line %q", where, line)
						}
					}
				}
			})
		}
	}
}

// ruleLines are the non-blank lines of a rules text, trimmed.
func ruleLines(rules string) []string {
	var out []string
	for _, line := range strings.Split(rules, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func canaryReconciler(c client.Client, reader client.Reader, f testutil.Fixture) *Reconciler {
	return &Reconciler{Client: c, APIReader: reader, Templates: client.ObjectKeyFromObject(f.Template), APIURL: "https://operator.example", ManagedCodexTasks: true, PrivateHistoryRetention: true, PrivateProjectCanaries: f.Policy, PrivateProjectCloneOwner: testutil.Owner}
}

func TestProjectCanaryIndependentAuthorityAndLatchBeforeOnePod(t *testing.T) {
	f := testutil.New(t, "codex", "dev/dev-env")
	base := privateHistoryClient(f.Session, f.Catalog, f.Template)
	podCreates := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		switch typed := o.(type) {
		case *corev1.PersistentVolumeClaim:
			typed.UID = "original-home"
		case *corev1.Pod:
			podCreates++
			typed.UID = "original-pod"
			var retained v1alpha1.AgentSession
			if err := base.Get(ctx, client.ObjectKeyFromObject(f.Session), &retained); err != nil {
				t.Fatal(err)
			}
			h := retained.Status.PrivateHistory
			if h == nil || !h.ExecutorAttempted || h.ExecutorIntent == "" || h.HomeUID != "original-home" || typed.Annotations[privateHistoryExecutorAnnotation] != h.ExecutorIntent || !slices.Contains(retained.Finalizers, v1alpha1.ProjectCanaryFinalizer) || !slices.Contains(retained.Finalizers, Finalizer) {
				t.Fatal("Pod created before retained/latch confirmation")
			}
		}
		return c.Create(ctx, o, opts...)
	}})
	r := canaryReconciler(c, base, f)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := base.Get(context.Background(), request.NamespacedName, &pod); err != nil || podCreates != 1 {
		t.Fatal("first exact canary not admitted", err, podCreates)
	}
	if err := base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	// Reconstructing with an absent original executor cannot start another.
	r = canaryReconciler(c, base, f)
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if podCreates != 1 {
		t.Fatal("missing original Pod rearmed canary")
	}
	if err := base.Get(context.Background(), request.NamespacedName, &pod); !apierrors.IsNotFound(err) {
		t.Fatal("lost original was recreated", err)
	}
	var kept v1alpha1.AgentSession
	if err := base.Get(context.Background(), request.NamespacedName, &kept); err != nil || kept.Status.PrivateHistory.StopState != "Unknown" || !kept.Status.PrivateHistory.ExecutorAttempted {
		t.Fatal("missing Pod inferred stop/reset latch", err)
	}
}

func TestProjectCanaryDriftAndUnconfirmedRetentionCreateNoPod(t *testing.T) {
	for _, name := range []string{"disabled", "retention-off", "catalog-drift", "template-drift", "status-unconfirmed"} {
		t.Run(name, func(t *testing.T) {
			f := testutil.New(t, "claude", "dev/dev-env")
			if name == "catalog-drift" {
				f.Catalog.ResourceVersion = "2"
			}
			if name == "template-drift" {
				f.Template.Data["templates.yaml"] = "invalid"
			}
			base := privateHistoryClient(f.Session, f.Catalog, f.Template)
			creates := 0
			c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				creates++
				return c.Create(ctx, o, opts...)
			}})
			r := canaryReconciler(c, base, f)
			switch name {
			case "disabled":
				r.PrivateProjectCanaries = nil
			case "retention-off":
				r.PrivateHistoryRetention = false
			case "status-unconfirmed":
				reads := 0
				r.APIReader = retentionReader{Reader: base, beforeGet: func(o client.Object) error {
					if _, ok := o.(*v1alpha1.AgentSession); ok {
						reads++
						if reads > 1 {
							return errors.New("synthetic retention confirmation unavailable")
						}
					}
					return nil
				}}
			}
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)})
			if name == "status-unconfirmed" && err == nil {
				t.Fatal("unknown retained write succeeded")
			}
			if creates != 0 {
				t.Fatal("failed independent admission created resources", creates)
			}
			var kept v1alpha1.AgentSession
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.Session), &kept); err != nil || !slices.Contains(kept.Finalizers, v1alpha1.ProjectCanaryFinalizer) {
				t.Fatal("refusal lost permanent identity", err)
			}
		})
	}
}

func TestProjectCanaryEarlyDeletePreservesOriginalWithoutRetentionStatus(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	base := privateHistoryClient(f.Session, f.Catalog, f.Template)
	if err := base.Delete(context.Background(), f.Session); err != nil {
		t.Fatal(err)
	}
	r := canaryReconciler(base, base, f)
	r.PrivateHistoryRetention = false
	r.PrivateProjectCanaries = nil
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)}); err != nil {
		t.Fatal(err)
	}
	var retained v1alpha1.AgentSession
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.Session), &retained); err != nil || retained.UID != f.Session.UID || retained.DeletionTimestamp.IsZero() || !slices.Contains(retained.Finalizers, v1alpha1.ProjectCanaryFinalizer) || retained.Status.PrivateHistory.Admission != "Unknown" || retained.Status.PrivateHistory.StopState != "Unknown" {
		t.Fatal("delete-before-reconcile released/guessed original", err)
	}
	if err := releaseSession(context.Background(), base, &retained); err == nil {
		t.Fatal("missing retention finalizer escaped permanent canary guard")
	}
	var pods corev1.PodList
	if err := base.List(context.Background(), &pods); err != nil || len(pods.Items) != 0 {
		t.Fatal("early-delete canary executed", err)
	}
}

func TestProjectCanaryMissingStatusStillBlocksPrivateHomeCleanup(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	s := f.Session
	s.Finalizers = append(s.Finalizers, Finalizer)
	home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: HomeClaimName(s.Name), Namespace: s.Namespace, UID: "original-home", Finalizers: []string{Finalizer}, OwnerReferences: []metav1.OwnerReference{ownerRef(s)}}}
	base := privateHistoryClient(s, home)
	if err := deleteVolume(context.Background(), base, s, home, false, true); err == nil {
		t.Fatal("missing status permitted HOME delete")
	}
	if err := releaseVolume(context.Background(), base, s, home, false, true); err == nil {
		t.Fatal("missing status permitted HOME release")
	}
	if err := releaseSession(context.Background(), base, s); err == nil {
		t.Fatal("missing status permitted Session release")
	}
	var kept corev1.PersistentVolumeClaim
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(home), &kept); err != nil || kept.UID != home.UID {
		t.Fatal("retained HOME lost", err)
	}
}

func TestProjectCanaryAuthorityLossAfterIntentNeverRearms(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	base := privateHistoryClient(f.Session, f.Catalog, f.Template)
	creates := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if home, ok := o.(*corev1.PersistentVolumeClaim); ok {
			home.UID = "original-home"
		}
		if _, ok := o.(*corev1.Pod); ok {
			creates++
		}
		return c.Create(ctx, o, opts...)
	}})
	reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, o, opts...); err != nil {
			return err
		}
		if cm, ok := o.(*corev1.ConfigMap); ok && key == client.ObjectKeyFromObject(f.Catalog) {
			var retained v1alpha1.AgentSession
			if err := base.Get(ctx, client.ObjectKeyFromObject(f.Session), &retained); err != nil {
				return err
			}
			if retained.Status.PrivateHistory != nil && retained.Status.PrivateHistory.ExecutorAttempted {
				cm.ResourceVersion = "2"
			}
		}
		return nil
	}})
	r := canaryReconciler(c, reader, f)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var retained v1alpha1.AgentSession
	if err := base.Get(context.Background(), request.NamespacedName, &retained); err != nil {
		t.Fatal(err)
	}
	if creates != 0 || retained.Status.PrivateHistory == nil || !retained.Status.PrivateHistory.ExecutorAttempted || retained.Status.PrivateHistory.ExecutorIntent == "" {
		t.Fatal("late authority loss executed or discarded reserved intent")
	}
	// Healthy authority later cannot turn that reserved attempt into a fresh one.
	r = canaryReconciler(c, base, f)
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if creates != 0 {
		t.Fatal("restored authority rearmed unconfirmed original attempt")
	}
	var home corev1.PersistentVolumeClaim
	if err := base.Get(context.Background(), client.ObjectKey{Namespace: f.Session.Namespace, Name: HomeClaimName(f.Session.Name)}, &home); err != nil || home.UID != "original-home" || !slices.Contains(home.Finalizers, Finalizer) {
		t.Fatal("late refusal lost retained original HOME", err)
	}
}

// history-01 (DESIGN-019): a transient read failure after the executor latch
// is a known non-attempt. No Pod create was called for the intent, so a later
// healthy reconcile resumes that same intent once instead of burning the one
// canary attempt. A changed authority still never rearms (the test above).
func TestProjectCanaryTransientFailureAfterIntentResumesSameIntentOnce(t *testing.T) {
	for _, gap := range []string{"revalidation-read", "latch-confirmation"} {
		t.Run(gap, func(t *testing.T) {
			f := testutil.New(t, "claude", "dev/dev-env")
			base := privateHistoryClient(f.Session, f.Catalog, f.Template)
			creates := 0
			var createdIntent string
			c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				switch typed := o.(type) {
				case *corev1.PersistentVolumeClaim:
					typed.UID = "original-home"
				case *corev1.Pod:
					creates++
					typed.UID = "original-pod"
					createdIntent = typed.Annotations[privateHistoryExecutorAnnotation]
					var retained v1alpha1.AgentSession
					if err := base.Get(ctx, client.ObjectKeyFromObject(f.Session), &retained); err != nil {
						t.Fatal(err)
					}
					if h := retained.Status.PrivateHistory; h == nil || !h.ExecutorAttempted || h.ExecutorNotCreated || h.ExecutorIntent != createdIntent {
						t.Fatal("Pod created without a confirmed re-armed latch", h)
					}
				}
				return c.Create(ctx, o, opts...)
			}})
			failed := false
			reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, o, opts...); err != nil {
					return err
				}
				if failed {
					return nil
				}
				var retained v1alpha1.AgentSession
				if err := base.Get(ctx, client.ObjectKeyFromObject(f.Session), &retained); err != nil {
					return err
				}
				if h := retained.Status.PrivateHistory; h == nil || !h.ExecutorAttempted {
					return nil
				}
				_, catalog := o.(*corev1.ConfigMap)
				_, session := o.(*v1alpha1.AgentSession)
				if gap == "revalidation-read" && catalog && key == client.ObjectKeyFromObject(f.Catalog) || gap == "latch-confirmation" && session {
					failed = true
					return apierrors.NewServiceUnavailable("transient")
				}
				return nil
			}})
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)}
			if _, err := canaryReconciler(c, reader, f).Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			var retained v1alpha1.AgentSession
			if err := base.Get(context.Background(), request.NamespacedName, &retained); err != nil {
				t.Fatal(err)
			}
			h := retained.Status.PrivateHistory
			if !failed || creates != 0 || h == nil || !h.ExecutorAttempted || h.ExecutorIntent == "" || !h.ExecutorNotCreated || h.LastExecutor != nil {
				t.Fatalf("transient gap: failed %v, creates %d, history %+v; want a recorded non-attempt", failed, creates, h)
			}
			intent := h.ExecutorIntent
			for range 2 {
				if _, err := canaryReconciler(c, reader, f).Reconcile(context.Background(), request); err != nil {
					t.Fatal(err)
				}
			}
			if creates != 1 || createdIntent != intent {
				t.Fatalf("resume: creates %d with intent %q; want exactly one with the original %q", creates, createdIntent, intent)
			}
			if err := base.Get(context.Background(), request.NamespacedName, &retained); err != nil {
				t.Fatal(err)
			}
			if h := retained.Status.PrivateHistory; h.ExecutorNotCreated || h.ExecutorIntent != intent || !h.ExecutorAttempted {
				t.Fatalf("resumed latch %+v", h)
			}
		})
	}
}

// history-01 review: a read failure that persists after the mark is caught by
// the validation that runs before admission and before any re-arm write, so a
// marked canary makes no status writes while the failure lasts. Only after the
// reads heal does it re-arm and create its one Pod.
func TestProjectCanaryPersistentReadFailureAfterMarkWritesNothing(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	base := privateHistoryClient(f.Session, f.Catalog, f.Template)
	creates, writes := 0, 0
	c := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			switch typed := o.(type) {
			case *corev1.PersistentVolumeClaim:
				typed.UID = "original-home"
			case *corev1.Pod:
				creates++
			}
			return c.Create(ctx, o, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if _, ok := o.(*v1alpha1.AgentSession); ok && sub == "status" {
				writes++
			}
			return c.SubResource(sub).Update(ctx, o, opts...)
		},
	})
	broken := true
	reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, o, opts...); err != nil {
			return err
		}
		if _, catalog := o.(*corev1.ConfigMap); !broken || !catalog || key != client.ObjectKeyFromObject(f.Catalog) {
			return nil
		}
		var retained v1alpha1.AgentSession
		if err := base.Get(ctx, client.ObjectKeyFromObject(f.Session), &retained); err != nil {
			return err
		}
		if h := retained.Status.PrivateHistory; h != nil && h.ExecutorAttempted {
			return apierrors.NewTooManyRequests("throttled", 1)
		}
		return nil
	}})
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)}
	if _, err := canaryReconciler(c, reader, f).Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var retained v1alpha1.AgentSession
	if err := base.Get(context.Background(), request.NamespacedName, &retained); err != nil || !retained.Status.PrivateHistory.ExecutorNotCreated || creates != 0 {
		t.Fatal("first throttled reconcile did not record a non-attempt", err, creates)
	}
	// One more reconcile settles the conditions observed before HOME was bound.
	if _, err := canaryReconciler(c, reader, f).Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	marked := writes
	for range 3 {
		if _, err := canaryReconciler(c, reader, f).Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if writes != marked || creates != 0 {
		t.Fatalf("a lasting read failure made %d status writes and %d creates; want none", writes-marked, creates)
	}
	broken = false
	if _, err := canaryReconciler(c, reader, f).Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if creates != 1 {
		t.Fatalf("healed reads created %d Pods, want 1", creates)
	}
}

// history-01 review (#222): a reconcile records a known non-attempt only over
// its own acknowledged latch write. Two reconcilers race to re-arm one marked
// intent: A's clear lands first and A goes on to its one Create. B's clear
// either conflicts or loses its response, so B has no write of its own and
// must not put the mark back over A's re-armed intent. A third case moves the
// Session after B's own confirmed write: B then cannot tell that write from
// a later one, so the attempt stays latched.
func TestProjectCanaryNonAttemptMarkCoversOnlyItsOwnLatchWrite(t *testing.T) {
	for _, race := range []string{"conflict", "lost-response", "moved-after-latch"} {
		t.Run(race, func(t *testing.T) {
			f := testutil.New(t, "claude", "dev/dev-env")
			base := privateHistoryClient(f.Session, f.Catalog, f.Template)
			key := client.ObjectKeyFromObject(f.Session)
			creates := 0
			peerCleared := false
			c := interceptor.NewClient(base, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
					switch typed := o.(type) {
					case *corev1.PersistentVolumeClaim:
						typed.UID = "original-home"
					case *corev1.Pod:
						creates++
					}
					return c.Create(ctx, o, opts...)
				},
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
					s, ok := o.(*v1alpha1.AgentSession)
					if !ok || sub != "status" || race == "moved-after-latch" || peerCleared || s.Status.PrivateHistory == nil {
						return c.SubResource(sub).Update(ctx, o, opts...)
					}
					var stored v1alpha1.AgentSession
					if err := base.Get(ctx, key, &stored); err != nil {
						return err
					}
					if h := stored.Status.PrivateHistory; h == nil || !h.ExecutorNotCreated || s.Status.PrivateHistory.ExecutorNotCreated {
						return c.SubResource(sub).Update(ctx, o, opts...)
					}
					// Reconciler A clears the same mark first, with a
					// confirmed write, and goes on to Create.
					peerCleared = true
					stored.Status.PrivateHistory.ExecutorNotCreated = false
					if err := base.Status().Update(ctx, &stored); err != nil {
						return err
					}
					if race == "lost-response" {
						return apierrors.NewServerTimeout(v1alpha1.GroupVersion.WithResource("agentsessions").GroupResource(), "update", 1)
					}
					return c.SubResource(sub).Update(ctx, o, opts...)
				},
			})
			failures := 1
			reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, k, o, opts...); err != nil {
					return err
				}
				if _, catalog := o.(*corev1.ConfigMap); failures == 0 || !catalog || k != client.ObjectKeyFromObject(f.Catalog) {
					return nil
				}
				var stored v1alpha1.AgentSession
				if err := base.Get(ctx, key, &stored); err != nil {
					return err
				}
				if h := stored.Status.PrivateHistory; h == nil || !h.ExecutorAttempted || h.ExecutorNotCreated {
					return nil
				}
				failures--
				if race == "moved-after-latch" {
					// Another writer changes the Session after this
					// reconcile's own confirmed latch write.
					stored.Labels = map[string]string{"fixture/peer-write": "true"}
					if err := base.Update(ctx, &stored); err != nil {
						return err
					}
				}
				return apierrors.NewServiceUnavailable("transient")
			}})
			request := ctrl.Request{NamespacedName: key}
			reconcile := func() {
				t.Helper()
				if _, err := canaryReconciler(c, reader, f).Reconcile(context.Background(), request); err != nil {
					t.Fatal(err)
				}
			}
			stored := func() *v1alpha1.PrivateHistoryStatus {
				t.Helper()
				var s v1alpha1.AgentSession
				if err := base.Get(context.Background(), key, &s); err != nil {
					t.Fatal(err)
				}
				return s.Status.PrivateHistory
			}
			reconcile()
			if race == "moved-after-latch" {
				if h := stored(); failures != 0 || creates != 0 || h == nil || !h.ExecutorAttempted || h.ExecutorNotCreated {
					t.Fatalf("a Session moved after the latch: failures left %d, creates %d, history %+v; want a latched attempt with no mark", failures, creates, h)
				}
				return
			}
			if h := stored(); failures != 0 || creates != 0 || h == nil || !h.ExecutorNotCreated {
				t.Fatalf("setup: failures left %d, creates %d, history %+v; want a recorded non-attempt", failures, creates, h)
			}
			intent := stored().ExecutorIntent
			// Reconciler B re-arms the marked intent and loses the race.
			reconcile()
			if h := stored(); !peerCleared || creates != 0 || h.ExecutorNotCreated || !h.ExecutorAttempted || h.ExecutorIntent != intent {
				t.Fatalf("%s: peer cleared %v, creates %d, history %+v; the mark must not cover the peer's re-armed intent", race, peerCleared, creates, h)
			}
		})
	}
}

func TestProjectCanaryFinalExecutorRefusesForeignPVCAndMountAliases(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	pod, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, false, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*corev1.Pod){
		func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "foreign", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "other-home"}}})
		},
		func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "alias", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: HomeClaimName(f.Session.Name)}}})
		},
		func(p *corev1.Pod) {
			p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "home", MountPath: "/alias"})
		},
		func(p *corev1.Pod) {
			p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "tmp", MountPath: "/home/dev/.shared"})
		},
		func(p *corev1.Pod) { p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "workspace"}) },
	} {
		copy := pod.DeepCopy()
		mutate(copy)
		if validateProjectCanaryExecutor(f.Session, copy) == nil {
			t.Fatal("foreign/alias executor storage accepted")
		}
	}
}
