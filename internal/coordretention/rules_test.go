package coordretention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func retentionScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := coord.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := devv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}
func realSummaries(t *testing.T) []Summary {
	t.Helper()
	got, err := Summaries(context.Background(), ListConfig{Reader: &fixtureReader{objects: validObjects(t)}, Namespace: retentionNS, Enabled: map[Kind]bool{TaskClaim: true, PublishedContext: true, InboxMessage: true, InboxReadAck: true}})
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func summaryOf(t *testing.T, kind Kind) Summary {
	t.Helper()
	for _, s := range realSummaries(t) {
		if s.Kind == kind {
			return s
		}
	}
	t.Fatalf("missing %s", kind)
	return Summary{}
}
func config(c client.Reader) EvalConfig {
	return EvalConfig{Reader: c, OperatorNamespace: retentionNS, ReconciledNamespaces: map[string]bool{"dev-agents": true}}
}

func TestEvaluateSessionAbsenceAndSameUIDUseRealSummary(t *testing.T) {
	s := summaryOf(t, TaskClaim)
	for name, objects := range map[string][]client.Object{
		"not found releases":             nil,
		"same UID is kept even deleting": {&devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: s.Owner.Namespace, Name: s.Owner.Name, UID: types.UID(s.Owner.UID), DeletionTimestamp: &metav1.Time{}, Finalizers: []string{"test/preserve"}}}},
		"replacement UID releases":       {&devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: s.Owner.Namespace, Name: s.Owner.Name, UID: "88888888-8888-4888-8888-888888888888"}}},
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(objects...).Build()
			got := Evaluate(context.Background(), config(c), []Summary{s})[0].Decision
			want := Released
			if name == "same UID is kept even deleting" {
				want = Kept
			}
			if got != want {
				t.Fatalf("decision = %s, want %s", got, want)
			}
		})
	}
}

func TestEvaluateInboxMessageRequiresBoundAck(t *testing.T) {
	s := summaryOf(t, InboxMessage)
	ackName := inbox.AckName(s.UID, s.Recipient.UID)
	valid := &coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: ackName, UID: "99999999-9999-4999-8999-999999999999", ResourceVersion: "1"}, Spec: coord.InboxReadAckSpec{Project: s.Project, MessageName: s.Name, MessageUID: s.UID, Recipient: coord.SessionIdentity{Namespace: s.Recipient.Namespace, Name: s.Recipient.Name, UID: s.Recipient.UID}, Actor: actor()}}
	for name, ack := range map[string]*coord.InboxReadAck{"matching ack releases": valid, "foreign metadata is unknown": func() *coord.InboxReadAck { x := valid.DeepCopy(); x.Namespace = "foreign"; return x }(), "malformed UID is unknown": func() *coord.InboxReadAck { x := valid.DeepCopy(); x.UID = ""; return x }()} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(retentionScheme(t)).Build()
			r := &hookReader{Reader: c, get: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
				if target, ok := obj.(*coord.InboxReadAck); ok {
					*target = *ack.DeepCopy()
					return nil
				}
				return c.Get(ctx, key, obj)
			}}
			got := Evaluate(context.Background(), config(r), []Summary{s})[0].Decision
			want := Released
			if name != "matching ack releases" {
				want = Unknown
			}
			if got != want {
				t.Fatalf("decision = %s, want %s", got, want)
			}
		})
	}
}

func TestEvaluateReadAckRequiresProvedAbsenceAndArchiveEvidence(t *testing.T) {
	s := summaryOf(t, InboxReadAck)
	for name, archive := range map[string]map[MessageRef]ArchiveEvidence{
		"missing proof requests exact message": nil,
		"unknown proof remains unknown":        {s.Message: {Known: false}},
		"no hold keeps":                        {s.Message: {Known: true, Holds: false}},
		"hold releases after absence":          {s.Message: {Known: true, Holds: true}},
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(retentionScheme(t)).Build()
			got := Evaluate(context.Background(), EvalConfig{Reader: c, OperatorNamespace: retentionNS, Archive: archive}, []Summary{s})[0]
			want := Unknown
			if name == "no hold keeps" {
				want = Kept
			}
			if name == "hold releases after absence" {
				want = Released
			}
			if got.Decision != want {
				t.Fatalf("decision = %s, want %s", got.Decision, want)
			}
			if (name == "missing proof requests exact message" || name == "unknown proof remains unknown") && (got.RequiredMessage == nil || *got.RequiredMessage != s.Message) {
				t.Fatalf("required message = %#v, want %#v", got.RequiredMessage, s.Message)
			}
		})
	}
}

func TestEvaluateRejectsForgedSummaryAndCancellationMemoHit(t *testing.T) {
	forged := Summary{Kind: TaskClaim, Namespace: retentionNS, Valid: true}
	if got := Evaluate(context.Background(), config(fake.NewClientBuilder().WithScheme(retentionScheme(t)).Build()), []Summary{forged})[0].Decision; got != Unknown {
		t.Fatalf("forged summary = %s", got)
	}
	s := summaryOf(t, TaskClaim)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Evaluate(ctx, config(fake.NewClientBuilder().WithScheme(retentionScheme(t)).Build()), []Summary{s, s}); got[0].Decision != Unknown || got[1].Decision != Unknown {
		t.Fatalf("cancelled results = %#v", got)
	}
}

// hookReader can return malformed successful responses without relying on a
// fake store to violate its own key lookup semantics.
type hookReader struct {
	client.Reader
	get  func(context.Context, client.ObjectKey, client.Object) error
	list func(context.Context, client.ObjectList, ...client.ListOption) error
}

func (r *hookReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	return r.get(ctx, key, obj)
}
func (r *hookReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if r.list != nil {
		return r.list(ctx, list, opts...)
	}
	return r.Reader.List(ctx, list, opts...)
}
func absentObject(key client.ObjectKey) error {
	return apierrors.NewNotFound(schema.GroupResource{Resource: "synthetic"}, key.Name)
}

func TestEvaluateSessionRuleMatrix(t *testing.T) {
	for _, kind := range []Kind{TaskClaim, PublishedContext} {
		s := summaryOf(t, kind)
		ref := s.Owner
		if kind == PublishedContext {
			ref = s.Source
		}
		for _, tc := range []struct {
			name string
			want Decision
			mode string
		}{
			{"absent", Released, "notfound"}, {"replacement", Released, "replacement"}, {"same terminal UID", Kept, "same"},
			{"same deleting UID", Kept, "deleting"}, {"forbidden", Unknown, "forbidden"}, {"timeout", Unknown, "timeout"}, {"unserved", Unknown, "unserved"},
			{"empty UID", Unknown, "empty"}, {"malformed UID", Unknown, "baduid"}, {"foreign namespace", Unknown, "namespace"}, {"foreign name", Unknown, "name"},
		} {
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				r := &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
					if key != (client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}) {
						t.Fatalf("foreign query: %#v", key)
					}
					switch tc.mode {
					case "notfound":
						return absentObject(key)
					case "forbidden":
						return apierrors.NewForbidden(schema.GroupResource{Resource: "agentsessions"}, key.Name, errors.New("fixture"))
					case "timeout":
						return apierrors.NewTimeoutError("fixture", 1)
					case "unserved":
						return &meta.NoKindMatchError{GroupKind: devv1.GroupVersion.WithKind("AgentSession").GroupKind()}
					}
					got := obj.(*devv1.AgentSession)
					got.ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: types.UID(ref.UID)}
					got.Status.Phase = devv1.PhaseArchived
					switch tc.mode {
					case "replacement":
						got.UID = "88888888-8888-4888-8888-888888888888"
					case "empty":
						got.UID = ""
					case "baduid":
						got.UID = "not-a-kubernetes-uid"
					case "namespace":
						got.Namespace = "foreign"
					case "name":
						got.Name = "foreign"
					case "deleting":
						got.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
					}
					return nil
				}}
				got := Evaluate(context.Background(), config(r), []Summary{s})[0]
				if got.Decision != tc.want || got.RequiredMessage != nil {
					t.Fatalf("%s = %#v", tc.name, got)
				}
			})
		}
		t.Run(string(kind)+"/nil and out-of-scope", func(t *testing.T) {
			for _, cfg := range []EvalConfig{config(nil), {Reader: &hookReader{get: func(context.Context, client.ObjectKey, client.Object) error { t.Fatal("out-of-scope GET"); return nil }}, OperatorNamespace: retentionNS, ReconciledNamespaces: map[string]bool{}}} {
				if got := Evaluate(context.Background(), cfg, []Summary{s})[0]; got.Decision != Unknown {
					t.Fatalf("scope = %#v", got)
				}
			}
		})
	}
}

func TestSessionMemoUIDCancellationAndFreshPass(t *testing.T) {
	s := summaryOf(t, TaskClaim)
	other := s
	other.Owner.UID = "88888888-8888-4888-8888-888888888888"
	calls := 0
	r := &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
		calls++
		obj.(*devv1.AgentSession).ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: types.UID(s.Owner.UID)}
		return nil
	}}
	cfg := config(r)
	out := Evaluate(context.Background(), cfg, []Summary{other, s})
	if out[0].Decision != Released || out[1].Decision != Kept || calls != 1 {
		t.Fatalf("memo UID/calls: %#v /%d", out, calls)
	}
	_ = Evaluate(context.Background(), cfg, []Summary{s})
	if calls != 2 {
		t.Fatalf("memo escaped pass: %d", calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	memo := map[string]memoSession{}
	r.get = func(_ context.Context, key client.ObjectKey, _ client.Object) error {
		calls++
		return absentObject(key)
	}
	if got := session(ctx, cfg, s.Owner, memo); got != sessionAbsent {
		t.Fatal("initial absence not cached")
	}
	before := calls
	cancel()
	if got := session(ctx, cfg, s.Owner, memo); got != sessionUnknown || calls != before {
		t.Fatal("cancelled memo became absence or queried again")
	}
	late, stop := context.WithCancel(context.Background())
	defer stop()
	r.get = func(_ context.Context, key client.ObjectKey, _ client.Object) error { stop(); return absentObject(key) }
	for _, got := range Evaluate(late, cfg, []Summary{s, s}) {
		if got.Decision != Unknown {
			t.Fatal("late read cancellation became eligible")
		}
	}
}

func TestInboxMessageSenderRecipientAndAckMatrix(t *testing.T) {
	s := summaryOf(t, InboxMessage)
	for _, tc := range []struct {
		name, mode string
		want       Decision
	}{
		{"sender present", "sender", Kept}, {"sender unknown", "sender-error", Unknown},
		{"sender absent recipient absent", "absent", Released}, {"recipient present no ack", "recipient", Kept}, {"recipient unknown", "recipient-error", Unknown},
		{"ack error", "ack-error", Unknown}, {"ack wrong project", "project", Unknown}, {"ack wrong message UID", "message", Unknown}, {"ack wrong recipient", "recipient-binding", Unknown},
		{"ack wrong name", "name", Unknown}, {"ack missing RV", "rv", Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
				switch got := obj.(type) {
				case *devv1.AgentSession:
					if key.Name == s.Sender.Name && tc.mode == "sender-error" || key.Name == s.Recipient.Name && tc.mode == "recipient-error" {
						return errors.New("fixture unavailable")
					}
					if key.Name == s.Sender.Name && tc.mode == "sender" || key.Name == s.Recipient.Name && tc.mode == "recipient" {
						uid := s.Sender.UID
						if key.Name == s.Recipient.Name {
							uid = s.Recipient.UID
						}
						got.ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: types.UID(uid)}
						return nil
					}
					return absentObject(key)
				case *coord.InboxReadAck:
					if tc.mode == "ack-error" {
						return errors.New("ack unavailable")
					}
					if tc.mode == "absent" || tc.mode == "recipient" || tc.mode == "recipient-error" {
						return absentObject(key)
					}
					got.ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: "99999999-9999-4999-8999-999999999999", ResourceVersion: "1"}
					got.Spec = coord.InboxReadAckSpec{Project: s.Project, MessageName: s.Name, MessageUID: s.UID, Recipient: coord.SessionIdentity{Namespace: s.Recipient.Namespace, Name: s.Recipient.Name, UID: s.Recipient.UID}, Actor: actor()}
					switch tc.mode {
					case "project":
						got.Spec.Project = "foreign"
					case "message":
						got.Spec.MessageUID = "88888888-8888-4888-8888-888888888888"
					case "recipient-binding":
						got.Spec.Recipient.Name = "foreign"
					case "name":
						got.Name = "foreign"
					case "rv":
						got.ResourceVersion = ""
					}
					return nil
				default:
					t.Fatalf("unexpected GET %T", obj)
					return nil
				}
			}}
			if got := Evaluate(context.Background(), config(r), []Summary{s})[0]; got.Decision != tc.want || got.RequiredMessage != nil {
				t.Fatalf("result %#v want %s", got, tc.want)
			}
		})
	}
}

func TestReadAckPendingOnlyAfterScopedAbsence(t *testing.T) {
	s := summaryOf(t, InboxReadAck)
	for _, tc := range []struct {
		name, mode string
		want       Decision
		pending    bool
	}{
		{"NotFound missing proof", "absent", Unknown, true}, {"replacement missing proof", "replacement", Unknown, true}, {"present despite archive holds", "present", Kept, false},
		{"message read failure", "error", Unknown, false}, {"empty response UID", "empty", Unknown, false}, {"foreign namespace", "namespace", Unknown, false}, {"foreign name", "name", Unknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
				if tc.mode == "error" {
					return apierrors.NewTimeoutError("fixture", 1)
				}
				if tc.mode == "absent" {
					return absentObject(key)
				}
				got := obj.(*coord.InboxMessage)
				got.ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: types.UID(s.Message.UID)}
				switch tc.mode {
				case "replacement":
					got.UID = "88888888-8888-4888-8888-888888888888"
				case "empty":
					got.UID = ""
				case "namespace":
					got.Namespace = "foreign"
				case "name":
					got.Name = "foreign"
				}
				return nil
			}}
			cfg := config(r)
			if tc.mode == "present" {
				cfg.Archive = map[MessageRef]ArchiveEvidence{s.Message: {Known: true, Holds: true}}
			}
			got := Evaluate(context.Background(), cfg, []Summary{s})[0]
			if got.Decision != tc.want || (got.RequiredMessage != nil) != tc.pending {
				t.Fatalf("result %#v want %s pending%v", got, tc.want, tc.pending)
			}
		})
	}
	for _, mutate := range []func(*Summary){func(s *Summary) { s.Message.Namespace = "foreign" }, func(s *Summary) { s.Message.Project = "foreign" }, func(s *Summary) { s.Name = "bad" }, func(s *Summary) { s.UID = "bad" }, func(s *Summary) { s.Resource = "taskclaims" }, func(s *Summary) { s.ResourceVersion = "" }} {
		bad := s
		mutate(&bad)
		cfg := config(&hookReader{get: func(context.Context, client.ObjectKey, client.Object) error {
			t.Fatal("malformed summary queried")
			return nil
		}})
		cfg.Archive = map[MessageRef]ArchiveEvidence{bad.Message: {Known: true, Holds: true}}
		got := Evaluate(context.Background(), cfg, []Summary{bad})[0]
		if got.Decision != Unknown || got.RequiredMessage != nil {
			t.Fatalf("malformed scope scheduled work: %#v", got)
		}
	}
	cfg := config(nil)
	if got := Evaluate(context.Background(), cfg, []Summary{s})[0]; got.Decision != Unknown || got.RequiredMessage != nil {
		t.Fatal("nil Reader scheduled work")
	}
	cfg = config(&hookReader{get: func(context.Context, client.ObjectKey, client.Object) error {
		t.Fatal("foreign scope queried")
		return nil
	}})
	cfg.OperatorNamespace = "foreign"
	if got := Evaluate(context.Background(), cfg, []Summary{s})[0]; got.Decision != Unknown || got.RequiredMessage != nil {
		t.Fatal("foreign namespace scheduled work")
	}
}

func TestSummaryProjectionSubsetsAndMalformedRecords(t *testing.T) {
	objects := validObjects(t)
	r := &fixtureReader{objects: objects}
	out, err := Summaries(context.Background(), ListConfig{Reader: r, Namespace: retentionNS, Enabled: map[Kind]bool{PublishedContext: true}})
	if err != nil || len(out) != 1 || !out[0].Valid || len(r.calls) != 2 {
		t.Fatalf("subset %#v %v", out, err)
	}
	for i, call := range r.calls {
		want := ""
		if i == 1 {
			want = "next"
		}
		if call.kind != "PublishedContextList" || call.cursor != want {
			t.Fatalf("continuation/subset: %#v", call)
		}
	}
	encoded := fmt.Sprintf("%#v", out)
	if strings.Contains(encoded, contextSpec().Text) || strings.Contains(encoded, "ContentSHA256") || strings.Contains(encoded, "Object:") {
		t.Fatal("summary retained full object/text")
	}
	for _, listKind := range []string{"TaskClaimList", "PublishedContextList"} {
		for _, mutation := range []string{"name", "namespace", "uid", "rv", "project"} {
			t.Run(listKind+"/"+mutation, func(t *testing.T) {
				all := validObjects(t)
				item := all[listKind][0]
				switch mutation {
				case "name":
					item.SetName("bad")
				case "namespace":
					item.SetNamespace("foreign")
				case "uid":
					item.SetUID("bad")
				case "rv":
					item.SetResourceVersion("")
				case "project":
					if listKind == "TaskClaimList" {
						_ = unstructured.SetNestedField(item.Object, "bad/project", "spec", "identity", "project")
					} else {
						_ = unstructured.SetNestedField(item.Object, "bad/project", "spec", "project")
					}
				}
				k := TaskClaim
				if listKind == "PublishedContextList" {
					k = PublishedContext
				}
				rr := &fixtureReader{objects: map[string][]unstructured.Unstructured{listKind: {item}}}
				summaries, err := Summaries(context.Background(), ListConfig{Reader: rr, Namespace: retentionNS, Enabled: map[Kind]bool{k: true}})
				if err != nil || len(summaries) != 1 || summaries[0].Valid {
					t.Fatalf("malformed summary %#v %v", summaries, err)
				}
			})
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := &fixtureReader{objects: validObjects(t)}
	hook := &hookReader{list: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		err := base.List(ctx, list, opts...)
		cancel()
		return err
	}}
	if got, err := Summaries(ctx, ListConfig{Reader: hook, Namespace: retentionNS, Enabled: map[Kind]bool{TaskClaim: true}}); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("late List cancellation %#v %v", got, err)
	}
	tooMany := make([]unstructured.Unstructured, 51)
	hook.list = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
		list.(*unstructured.UnstructuredList).Items = tooMany
		return nil
	}
	if got, err := Summaries(context.Background(), ListConfig{Reader: hook, Namespace: retentionNS, Enabled: map[Kind]bool{TaskClaim: true}}); err == nil || got != nil {
		t.Fatal("oversized page accepted")
	}
}
