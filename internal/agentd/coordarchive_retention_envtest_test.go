package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
	"github.com/thaynes43/dev-env/internal/coordretention"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/taskclaim"
	"github.com/thaynes43/dev-env/internal/testenv"
)

// TestEnvtestArchiveBeforeDelete is DESIGN-024 E1 for one claim. The archive is
// the real coordination archive on a temp directory. The mount check is stubbed
// the same way the archive tests stub it. Put, mark, and lookup cross the ctl
// JSON boundary. A message, an ack, a process exec, and the archived-read API
// remain. The published-context proof is TestEnvtestArchiveBeforeDeleteContext.
func TestEnvtestArchiveBeforeDelete(t *testing.T) {
	old := coordArchiveIsMounted
	coordArchiveIsMounted = func(string, string) error { return nil }
	t.Cleanup(func() { coordArchiveIsMounted = old })

	env, err := testenv.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Log("envtest stop:", err)
		}
	})
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{coord.AddToScheme, devv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	const ns = "dev-env-system"
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	spec := retentionClaimSpec()
	if !taskclaim.ValidSpec(spec) {
		t.Fatal("claim spec")
	}
	claim := &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: taskclaim.Name(spec.Identity)}, Spec: spec}
	if err := admin.Create(ctx, claim); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	root := filepath.Join(home, protocol.CoordArchiveDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 17, 17, 0, 0, 0, time.UTC)
	clock := start
	archive := CoordArchive{Root: root, Home: home, Now: func() time.Time { return clock }}
	var order []string
	store := &fsArchive{a: archive, order: &order}
	deletes := &recordingClient{Client: admin, order: &order}
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace, Client: deletes, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{spec.Owner.Namespace: true},
		Kinds: map[coordretention.Kind]bool{coordretention.TaskClaim: true},
		Pods:  shelfPod{}, Archive: store, Slots: shelf.NewExecSlots(2),
		Now: func() time.Time { return clock },
	}
	pass := func(at time.Time) string {
		t.Helper()
		clock = at
		return runnable.Pass(ctx)
	}
	if got := pass(start); got != "complete" || len(order) != 0 {
		t.Fatalf("mark pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); err != nil {
		t.Fatal(err)
	}
	id := protocol.CoordArchiveIdentity{Resource: "taskclaims", Project: spec.Identity.Project, Name: claim.Name, UID: string(claim.UID)}
	looked, err := archive.Lookup([]protocol.CoordArchiveIdentity{id})
	if err != nil || len(looked) != 1 || looked[0].GraceStart == nil || !looked[0].Holds {
		t.Fatalf("marked archive %#v %v", looked, err)
	}
	if got := pass(start.Add(coordretention.DefaultGrace - time.Minute)); got != "complete" || len(order) != 0 {
		t.Fatalf("grace pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); err != nil {
		t.Fatal(err)
	}
	live := liveClaimBytes(t, ctx, admin, claim)
	if got := pass(start.Add(coordretention.DefaultGrace)); got != "complete" || !verifyBeforeDelete(order) {
		t.Fatalf("delete pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("claim after grace: %v", err)
	}
	stored, err := archive.GetSelected(protocol.CoordArchiveGetQuery{Resource: id.Resource, Name: id.Name, UID: id.UID})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(stored.Object)
	if hex.EncodeToString(sum[:]) != stored.Manifest.Object.SHA256 || !bytes.Equal(stored.Object, live) {
		t.Fatal("archived bytes are not the claim read before the delete")
	}
}

// TestEnvtestArchiveBeforeDeleteContext is DESIGN-024 E1 for one published
// context. The source session is absent, so the context is released. No claim
// is created.
func TestEnvtestArchiveBeforeDeleteContext(t *testing.T) {
	old := coordArchiveIsMounted
	coordArchiveIsMounted = func(string, string) error { return nil }
	t.Cleanup(func() { coordArchiveIsMounted = old })

	env, err := testenv.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Log("envtest stop:", err)
		}
	})
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{coord.AddToScheme, devv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	const ns = "dev-env-system"
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	spec := retentionContextSpec()
	if !contextpublication.ValidSpec(spec) {
		t.Fatal("context spec")
	}
	published := &coord.PublishedContext{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: contextpublication.Name(spec.Identity)}, Spec: spec}
	if err := admin.Create(ctx, published); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	root := filepath.Join(home, protocol.CoordArchiveDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 17, 19, 0, 0, 0, time.UTC)
	clock := start
	archive := CoordArchive{Root: root, Home: home, Now: func() time.Time { return clock }}
	var order []string
	store := &fsArchive{a: archive, order: &order}
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace, Client: &recordingClient{Client: admin, order: &order}, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{spec.Source.Namespace: true},
		Kinds: map[coordretention.Kind]bool{coordretention.PublishedContext: true},
		Pods:  shelfPod{}, Archive: store, Slots: shelf.NewExecSlots(2),
		Now: func() time.Time { return clock },
	}
	pass := func(at time.Time) string {
		t.Helper()
		clock = at
		return runnable.Pass(ctx)
	}
	if got := pass(start); got != "complete" || len(order) != 0 {
		t.Fatalf("mark pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(published), &coord.PublishedContext{}); err != nil {
		t.Fatal(err)
	}
	id := protocol.CoordArchiveIdentity{Resource: "publishedcontexts", Project: spec.Project, Name: published.Name, UID: string(published.UID)}
	looked, err := archive.Lookup([]protocol.CoordArchiveIdentity{id})
	if err != nil || len(looked) != 1 || looked[0].GraceStart == nil || !looked[0].Holds {
		t.Fatalf("marked archive %#v %v", looked, err)
	}
	if got := pass(start.Add(coordretention.DefaultGrace - time.Minute)); got != "complete" || len(order) != 0 {
		t.Fatalf("grace pass %s order %v", got, order)
	}
	live := liveKindBytes(t, ctx, admin, client.ObjectKeyFromObject(published), "PublishedContext")
	if got := pass(start.Add(coordretention.DefaultGrace)); got != "complete" || !verifyBeforeDelete(order) {
		t.Fatalf("delete pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(published), &coord.PublishedContext{}); !apierrors.IsNotFound(err) {
		t.Fatalf("context after grace: %v", err)
	}
	stored, err := archive.GetSelected(protocol.CoordArchiveGetQuery{Resource: id.Resource, Name: id.Name, UID: id.UID})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(stored.Object)
	if hex.EncodeToString(sum[:]) != stored.Manifest.Object.SHA256 || !bytes.Equal(stored.Object, live) {
		t.Fatal("archived bytes are not the context read before the delete")
	}
}

func liveKindBytes(t *testing.T, ctx context.Context, c client.Client, key client.ObjectKey, kind string) []byte {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(coord.GroupVersion.WithKind(kind))
	if err := c.Get(ctx, key, u); err != nil {
		t.Fatal(err)
	}
	if u.GetAPIVersion() == "" {
		u.SetAPIVersion(coord.GroupVersion.String())
	}
	if u.GetKind() == "" {
		u.SetKind(kind)
	}
	raw, err := protocol.CanonicalJSON(u.Object)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func liveClaimBytes(t *testing.T, ctx context.Context, c client.Client, claim *coord.TaskClaim) []byte {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(coord.GroupVersion.WithKind("TaskClaim"))
	if err := c.Get(ctx, client.ObjectKeyFromObject(claim), u); err != nil {
		t.Fatal(err)
	}
	if u.GetAPIVersion() == "" {
		u.SetAPIVersion(coord.GroupVersion.String())
	}
	if u.GetKind() == "" {
		u.SetKind("TaskClaim")
	}
	raw, err := protocol.CanonicalJSON(u.Object)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func verifyBeforeDelete(order []string) bool {
	sawGet := false
	for _, step := range order {
		switch step {
		case "get":
			sawGet = true
		case "delete":
			return sawGet
		}
	}
	return false
}

func retentionClaimSpec() coord.TaskClaimSpec {
	owner := coord.SessionIdentity{Namespace: "dev-agents", Name: "sender", UID: "11111111-1111-4111-8111-111111111111"}
	return coord.TaskClaimSpec{
		Identity: coord.TaskIdentity{Project: "sample", Repository: "repo", TaskKey: "issue:301"},
		Owner:    owner,
		Actor:    coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "sender", PodUID: "33333333-3333-4333-8333-333333333333"},
		Provenance: coord.ClaimProvenance{
			CatalogRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RulesRevision:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			GitHub:          "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown",
		},
	}
}

func retentionContextSpec() coord.PublishedContextSpec {
	claim := retentionClaimSpec()
	const text = "finite synthetic context"
	return coord.PublishedContextSpec{
		Identity: coord.ContextIdentity{SessionUID: claim.Owner.UID, Kind: "handoff", Key: "retention-301"},
		Source:   claim.Owner, SourceGeneration: 1, SourceParent: "dev-env-system/human",
		Project: claim.Identity.Project, Repository: claim.Identity.Repository,
		Actor: claim.Actor, Provenance: claim.Provenance,
		Attribution: coord.ContextAttribution{
			Provider: "claude", RequestedModel: "claude-haiku-4-5",
			ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown",
		},
		Origin: "ExplicitPublished", Completeness: "ExplicitExcerpt", StopState: "Unknown",
		Text: text, ContentSHA256: apiv1.ContextHash(text), ByteCount: int32(len(text)),
	}
}

type shelfPod struct{}

func (shelfPod) Pod(context.Context) (string, error) { return "shelf", nil }

type fsArchive struct {
	a     CoordArchive
	order *[]string
}

func (f *fsArchive) Put(_ context.Context, req protocol.CoordArchivePutRequest, mark bool) (protocol.CoordArchivePutResult, error) {
	req, err := decodeCtl(req)
	if err != nil {
		return protocol.CoordArchivePutResult{}, err
	}
	if mark != (req.Event != nil) {
		return protocol.CoordArchivePutResult{}, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--mark and absent event must be supplied together")
	}
	return f.a.PutRecord(req.Manifest, bytes.NewReader(req.Object), req.Event)
}

func (f *fsArchive) Mark(_ context.Context, req []protocol.CoordArchiveMarkRequest) ([]protocol.CoordArchiveMarkResult, error) {
	req, err := decodeCtl(req)
	if err != nil {
		return nil, err
	}
	return f.a.Mark(req)
}

func (f *fsArchive) Lookup(_ context.Context, ids []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error) {
	ids, err := decodeCtl(ids)
	if err != nil {
		return nil, err
	}
	return f.a.Lookup(ids)
}

// decodeCtl is the coord-archive ctl JSON boundary: the request byte bound,
// UTF-8, one canonical object, and no trailing value. A process exec remains
// outside this test.
func decodeCtl[T any](v T) (T, error) {
	var zero T
	raw, err := protocol.CanonicalJSON(v)
	if err != nil {
		return zero, err
	}
	if len(raw) > protocol.MaxCoordArchiveRequestBytes || !utf8.Valid(raw) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "request read, byte bound or UTF-8 refused")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var out T
	var extra any
	if err := dec.Decode(&out); err != nil || !errors.Is(dec.Decode(&extra), io.EOF) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "one complete JSON request required")
	}
	return out, nil
}

func (f *fsArchive) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	if f.order != nil {
		*f.order = append(*f.order, "get")
	}
	return f.a.GetSelected(q)
}

type recordingClient struct {
	client.Client
	order *[]string
}

func (c recordingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	*c.order = append(*c.order, "delete")
	return c.Client.Delete(ctx, obj, opts...)
}

// TestEnvtestTwoPassesDeleteOnce is DESIGN-024 E7. Two passes reach the delete
// together. Exactly one API delete succeeds, and the archive keeps one revision.
func TestEnvtestTwoPassesDeleteOnce(t *testing.T) {
	old := coordArchiveIsMounted
	coordArchiveIsMounted = func(string, string) error { return nil }
	t.Cleanup(func() { coordArchiveIsMounted = old })

	env, err := testenv.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Log("envtest stop:", err)
		}
	})
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{coord.AddToScheme, devv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	const ns = "dev-env-system"
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	spec := retentionClaimSpec()
	spec.Identity.TaskKey = "issue:302"
	if !taskclaim.ValidSpec(spec) {
		t.Fatal("claim spec")
	}
	claim := &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: taskclaim.Name(spec.Identity)}, Spec: spec}
	if err := admin.Create(ctx, claim); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	root := filepath.Join(home, protocol.CoordArchiveDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 17, 18, 0, 0, 0, time.UTC)
	clock := start
	archive := CoordArchive{Root: root, Home: home, Now: func() time.Time { return clock }}
	store := &fsArchive{a: archive}
	slots := shelf.NewExecSlots(8)
	deletes := &recordingClient{Client: admin, order: new([]string)}
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace, Client: deletes, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{spec.Owner.Namespace: true},
		Kinds: map[coordretention.Kind]bool{coordretention.TaskClaim: true},
		Pods:  shelfPod{}, Archive: store, Slots: slots,
		Now: func() time.Time { return clock },
	}
	if got := func() string { clock = start; return runnable.Pass(ctx) }(); got != "complete" {
		t.Fatalf("mark pass %s", got)
	}
	if got := func() string {
		clock = start.Add(coordretention.DefaultGrace - time.Minute)
		return runnable.Pass(ctx)
	}(); got != "complete" {
		t.Fatalf("grace pass %s", got)
	}
	gate := &deleteGate{ready: make(chan struct{})}
	shared := &meetingClient{Client: admin, gate: gate}
	at := start.Add(coordretention.DefaultGrace)
	passes := [2]*coordretention.Runnable{}
	for i := range passes {
		passes[i] = &coordretention.Runnable{
			Enabled: true, Grace: coordretention.DefaultGrace, Client: shared, Reader: admin,
			Namespace: ns, Reconciled: map[string]bool{spec.Owner.Namespace: true},
			Kinds: map[coordretention.Kind]bool{coordretention.TaskClaim: true},
			Pods:  shelfPod{}, Archive: store, Slots: slots,
			Now: func() time.Time { return at },
		}
	}
	var wg sync.WaitGroup
	results := make([]string, 2)
	wg.Add(2)
	for i := range passes {
		go func(i int) {
			defer wg.Done()
			results[i] = passes[i].Pass(ctx)
		}(i)
	}
	wg.Wait()
	if results[0] != "complete" || results[1] != "complete" || gate.seen() != 2 || shared.okCount() != 1 {
		t.Fatalf("results %q seen %d deletes %d", results, gate.seen(), shared.okCount())
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("claim after the two passes: %v", err)
	}
	id := protocol.CoordArchiveIdentity{Resource: "taskclaims", Project: spec.Identity.Project, Name: claim.Name, UID: string(claim.UID)}
	looked, err := archive.Lookup([]protocol.CoordArchiveIdentity{id})
	if err != nil || len(looked) != 1 || len(looked[0].Revisions) != 1 {
		t.Fatalf("archive %#v %v", looked, err)
	}
}

type deleteGate struct {
	mu    sync.Mutex
	n     int
	ready chan struct{}
	once  sync.Once
}

func (g *deleteGate) arrive() {
	g.mu.Lock()
	g.n++
	if g.n >= 2 {
		g.once.Do(func() { close(g.ready) })
	}
	g.mu.Unlock()
	select {
	case <-g.ready:
	case <-time.After(15 * time.Second):
	}
}

func (g *deleteGate) seen() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

type meetingClient struct {
	client.Client
	gate *deleteGate
	mu   sync.Mutex
	ok   int
}

func (c *meetingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.gate.arrive()
	err := c.Client.Delete(ctx, obj, opts...)
	if err == nil {
		c.mu.Lock()
		c.ok++
		c.mu.Unlock()
	}
	return err
}

func (c *meetingClient) okCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ok
}
