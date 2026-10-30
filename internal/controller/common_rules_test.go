package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/commonrules"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func commonCanaryController(t *testing.T, f *testutil.Fixture, text string) commonrules.Snapshot {
	t.Helper()
	return commonCanaryControllerWith(t, f, text, "HEADER")
}

// commonCanaryControllerWith binds a retained common snapshot of the given
// common and Codex header text to the fixture's record, policy and session.
func commonCanaryControllerWith(t *testing.T, f *testutil.Fixture, text, header string) commonrules.Snapshot {
	t.Helper()
	snapshot := commonfixture.Snapshot(t, []byte(f.Session.Annotations[v1alpha1.AnnotationProjectSnapshot]), text, header)
	f.Record.CommonRevision = snapshot.CommonRevision
	raw, _ := json.Marshal([]projectcanary.Record{f.Record})
	var err error
	f.Policy, err = projectcanary.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	f.Session.Spec.CommonRules, err = snapshot.Binding()
	if err != nil {
		t.Fatal(err)
	}
	f.Session.Spec.ProjectCanary.CommonRevision = snapshot.CommonRevision
	f.Session.Spec.ProjectCanary.PolicyDigest = f.Record.Digest()
	return snapshot
}

func TestCommonCanaryFixedFileTransportKeepsWholeExecutorBounds(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := testutil.New(t, provider, "dev/dev-env")
			accepted := commonCanaryController(t, &f, "COMMON_VALIDATION_MARKER")
			pod, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, false, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			c := pod.Spec.Containers[0]
			if envOf(c, protocol.SessionEnv).Value != "" || envOf(c, protocol.SessionFileEnv).Value != protocol.SessionInputPath || envOf(c, "AGENTD_ENABLE_RETAINED_COMMON_RULES").Value != "true" {
				t.Fatal("mutable/env common transport")
			}
			var projected *corev1.DownwardAPIVolumeSource
			for _, v := range pod.Spec.Volumes {
				if v.Name == "session-input" {
					projected = v.DownwardAPI
					if v.ConfigMap != nil {
						t.Fatal("unexpected input ConfigMap")
					}
				}
			}
			if projected == nil || len(projected.Items) != 1 || projected.Items[0].Path != "session.json" || projected.Items[0].FieldRef.FieldPath != "metadata.annotations['"+protocol.SessionInputAnnotation+"']" || *projected.DefaultMode != 0444 {
				t.Fatal("wrong source-owned projection")
			}
			mounted := false
			for _, m := range c.VolumeMounts {
				if m.Name == "session-input" {
					mounted = m.MountPath == "/opt/dev-env/session" && m.ReadOnly && m.SubPath == ""
				}
			}
			if !mounted {
				t.Fatal("input mount caller-controlled")
			}
			parsed, err := protocol.ParseSessionFile([]byte(pod.Annotations[protocol.SessionInputAnnotation]))
			if err != nil || parsed.SessionUID != string(f.Session.UID) {
				t.Fatal("bad original session document", err)
			}
			common, err := commonrules.Parse(parsed.CommonRules)
			if err != nil || common.CommonRevision != accepted.CommonRevision || common.ProjectDigest != commonrules.Digest(parsed.ProjectSnapshot) {
				t.Fatal("projection lost exact accepted source", err)
			}
			total := 0
			for k, v := range pod.Annotations {
				total += len(k) + len(v)
			}
			if total > 256<<10 || pod.Spec.RestartPolicy != corev1.RestartPolicyNever || *pod.Spec.ActiveDeadlineSeconds != 600 || *pod.Spec.TerminationGracePeriodSeconds != 5 || pod.Annotations["k8tz.io/inject"] != "false" || len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.EphemeralContainers) != 0 {
				t.Fatal("file route widened executor")
			}
			f.Session.Spec.CommonRules = nil
			f.Session.Spec.ProjectCanary.CommonRevision = ""
			f.Record.CommonRevision = ""
			f.Session.Spec.ProjectCanary.PolicyDigest = f.Record.Digest()
			old, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, false, nil, false)
			if err != nil || envOf(old.Spec.Containers[0], protocol.SessionEnv).Value == "" || envOf(old.Spec.Containers[0], protocol.SessionFileEnv).Value != "" {
				t.Fatal("legacy env route changed", err)
			}
		})
	}
}

func TestCommonCanaryIndependentSourceDriftCreatesNoExecutor(t *testing.T) {
	for _, kind := range []string{"uid", "resourceVersion", "text", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			f := testutil.New(t, "codex", "dev/dev-env")
			commonCanaryController(t, &f, "ACCEPTED_COMMON")
			a, h := commonfixture.Sources("ACCEPTED_COMMON", "HEADER")
			switch kind {
			case "uid":
				a.UID = "replacement"
			case "resourceVersion":
				a.ResourceVersion = "2"
			case "text":
				a.Data[commonrules.CommonKey] = "changed"
			}
			base := privateHistoryClient(f.Session, f.Catalog, f.Template, a, h)
			pods, pvcs := 0, 0
			c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				switch o.(type) {
				case *corev1.Pod:
					pods++
				case *corev1.PersistentVolumeClaim:
					pvcs++
				}
				return c.Create(ctx, o, opts...)
			}})
			r := canaryReconciler(c, base, f)
			r.RetainedCommonRules = kind != "disabled"
			_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)})
			if pods != 0 || pvcs != 0 {
				t.Fatal("drift created resources", pods, pvcs)
			}
		})
	}
}

// A session whose bound common text or Codex header repeats its prompt gets no
// executor: the controller asks CheckCommon before it starts one, as the API
// does before it creates the session, so a marker the prompt supplies never
// becomes canary evidence. The accepted case shows the same setup starts one.
func TestCommonCanaryPromptRepeatingCommonCreatesNoExecutor(t *testing.T) {
	for _, kind := range []string{"accepted", "common", "header"} {
		t.Run(kind, func(t *testing.T) {
			f := testutil.New(t, "codex", "dev/dev-env")
			common, header := "ACCEPTED_COMMON", "HEADER"
			switch kind {
			case "common":
				common += "\n" + f.Record.Prompt
			case "header":
				header += "\n" + f.Record.Prompt
			}
			commonCanaryControllerWith(t, &f, common, header)
			a, h := commonfixture.Sources(common, header)
			base := privateHistoryClient(f.Session, f.Catalog, f.Template, a, h)
			pods := 0
			c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				switch typed := o.(type) {
				case *corev1.PersistentVolumeClaim:
					typed.UID = "original-home"
				case *corev1.Pod:
					pods++
					typed.UID = "original-pod"
				}
				return c.Create(ctx, o, opts...)
			}})
			r := canaryReconciler(c, base, f)
			r.RetainedCommonRules = true
			_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.Session)})
			if want := map[bool]int{true: 1, false: 0}[kind == "accepted"]; pods != want {
				t.Fatalf("%s: %d executor pods, want %d", kind, pods, want)
			}
		})
	}
}

func TestCommonSessionSerializedExactAndOversizeBounds(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	commonCanaryController(t, &f, "")
	base, err := sessionDocument(f.Session, false)
	if err != nil {
		t.Fatal(err)
	}
	extra := protocol.MaxSessionFileBytes - len(base)
	text := strings.Repeat("\x01", extra/6) + strings.Repeat("x", extra%6)
	commonCanaryController(t, &f, text)
	exact, err := sessionDocument(f.Session, false)
	if err != nil || len(exact) != protocol.MaxSessionFileBytes {
		t.Fatal("exact serialized bound refused", len(exact), err)
	}
	commonCanaryController(t, &f, text+"x")
	if _, err := sessionDocument(f.Session, false); err == nil {
		t.Fatal("over serialized bound accepted")
	}
	commonCanaryController(t, &f, strings.Repeat("\x01", commonrules.MaxCommonBytes))
	if _, err := sessionDocument(f.Session, false); err == nil {
		t.Fatal("raw accepted worst escaping exceeded serialized cap")
	}
	// The API reserves the eventual original UID before admitting a new Session.
	commonCanaryController(t, &f, text)
	f.Session.UID = ""
	if CheckAgentdSession(f.Session) == nil {
		t.Fatal("admission omitted future UID size")
	}
	f.Session.Spec.ProjectCanary = nil
	if CheckAgentdSession(f.Session) == nil {
		t.Fatal("common rules widened general admission")
	}
}
