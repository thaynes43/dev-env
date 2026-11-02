package apiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

// claimsProbe pages TaskClaim lists by name (the fake client has no
// continue tokens), counts store lists and owner Gets, and injects failures.
type claimsProbe struct {
	mu        sync.Mutex
	lists     int
	gets      map[string]int
	listErr   error
	getErrFor map[string]error
}

func (p *claimsProbe) funcs() interceptor.Funcs {
	return interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			out, ok := list.(*coord.TaskClaimList)
			if !ok {
				return c.List(ctx, list, opts...)
			}
			p.mu.Lock()
			p.lists++
			listErr := p.listErr
			p.mu.Unlock()
			if listErr != nil {
				return listErr
			}
			lo := (&client.ListOptions{}).ApplyOptions(opts)
			var all coord.TaskClaimList
			if err := c.List(ctx, &all, client.InNamespace(lo.Namespace), client.MatchingLabelsSelector{Selector: lo.LabelSelector}); err != nil {
				return err
			}
			sort.Slice(all.Items, func(i, j int) bool { return all.Items[i].Name < all.Items[j].Name })
			start := 0
			if lo.Continue != "" {
				n, err := strconv.Atoi(strings.TrimPrefix(lo.Continue, "page-"))
				if err != nil || !strings.HasPrefix(lo.Continue, "page-") || n > len(all.Items) {
					return apierrors.NewResourceExpired("the continue token expired")
				}
				start = n
			}
			end := len(all.Items)
			if lo.Limit > 0 && start+int(lo.Limit) < end {
				end = start + int(lo.Limit)
			}
			out.Items = all.Items[start:end]
			out.Continue = ""
			if end < len(all.Items) {
				out.Continue = "page-" + strconv.Itoa(end)
			}
			return nil
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*v1alpha1.AgentSession); ok {
				p.mu.Lock()
				p.gets[key.String()]++
				err := p.getErrFor[key.Name]
				p.mu.Unlock()
				if err != nil {
					return err
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}
}

func (p *claimsProbe) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lists, p.gets = 0, map[string]int{}
}

// portalClaimsFixture enables task claims for "sample" and the portal reader.
// on sets the portal task-claims route with projects.
func portalClaimsFixture(t *testing.T, on bool, projects ...string) (*fixture, *claimsProbe) {
	t.Helper()
	probe := &claimsProbe{gets: map[string]int{}, getErrFor: map[string]error{}}
	f := newFixtureWith(t, probe.funcs())
	bindCatalogFixture(t, f)
	enableClaims(f)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: types.UID("portal-pod-uid")}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	f.srv.Policy.PortalReaderEnabled = true
	f.srv.PortalTaskClaims = PortalTaskClaimsConfig{Enabled: on, Projects: projects}
	f.h = f.srv.Handler()
	return f, probe
}

var claimDigest = strings.Repeat("d", 64)

func addPortalClaim(t *testing.T, f *fixture, key string, owner coord.SessionIdentity) *coord.TaskClaim {
	t.Helper()
	identity := coord.TaskIdentity{Project: "sample", Repository: "alias", TaskKey: key}
	claim := &coord.TaskClaim{
		ObjectMeta: metav1.ObjectMeta{Name: taskclaim.Name(identity), Namespace: "dev-env-system", Labels: map[string]string{taskclaim.ProjectLabel: "sample"}},
		Spec: coord.TaskClaimSpec{Identity: identity, Owner: owner,
			Actor:      coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/secret-actor-sa", PodName: "secret-actor-pod", PodUID: "secret-actor-pod-uid"},
			Provenance: coord.ClaimProvenance{CatalogRevision: claimDigest, RulesRevision: claimDigest, GitHub: "thaynes43/dev-env", DefaultBranch: "secret-default-branch", InitialPinState: "Unknown"}},
	}
	if err := f.c.Create(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	return claim
}

func addOwnerSession(t *testing.T, f *fixture, name, uid string, phase v1alpha1.SessionPhase, beat *time.Time, deleting bool) coord.SessionIdentity {
	t.Helper()
	ctx := context.Background()
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: types.UID(uid)},
		Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "SECRET-PROMPT", Parent: humanSA}}
	if deleting {
		s.Finalizers = []string{"test.dev-env.haynesops.com/hold"}
	}
	if err := f.c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.Status.Phase = phase
	if beat != nil {
		stamp := metav1.NewTime(*beat)
		s.Status.Agent = &v1alpha1.AgentStatus{LastHeartbeat: &stamp}
	}
	if err := f.c.Status().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if deleting {
		if err := f.c.Delete(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	return coord.SessionIdentity{Namespace: sessionNS, Name: name, UID: uid}
}

func claimsURL(values map[string]string) string {
	q := url.Values{}
	for k, v := range values {
		q.Set(k, v)
	}
	return apiv1.PortalTaskClaimsPath + "?" + q.Encode()
}

// readPortalClaims checks the status, refuses unknown keys, and requires the
// document to pass the shared validator.
func readPortalClaims(t *testing.T, f *fixture, raw string) (apiv1.PortalTaskClaims, string) {
	t.Helper()
	w := f.do(http.MethodGet, raw, "tok-portal", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", raw, w.Code, w.Body.String())
	}
	dec := json.NewDecoder(strings.NewReader(w.Body.String()))
	dec.DisallowUnknownFields()
	var page apiv1.PortalTaskClaims
	if err := dec.Decode(&page); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	if err := apiv1.ValidatePortalTaskClaims(page); err != nil {
		t.Fatalf("produced page fails the contract: %s", w.Body.String())
	}
	if len(w.Body.Bytes()) > apiv1.PortalTaskClaimsMaxBytes {
		t.Fatalf("page over its bound: %d", len(w.Body.Bytes()))
	}
	return page, w.Body.String()
}

func TestPortalTaskClaimsConstantsMatchOperator(t *testing.T) {
	if apiv1.PortalTaskClaimsFreshForSeconds != int64(portalFreshFor/time.Second) {
		t.Fatalf("fresh-for %d differs from portalFreshFor %s", apiv1.PortalTaskClaimsFreshForSeconds, portalFreshFor)
	}
	if apiv1.PortalTaskClaimsDefaultLimit != portalPageDefault || apiv1.PortalTaskClaimsMaxLimit != portalPageMax || apiv1.PortalTaskClaimsCursorMax != portalCursorMax {
		t.Fatal("paging constants differ from the portal reader's")
	}
}

func TestPortalTaskClaimsDefaultOffAndUnregistered(t *testing.T) {
	cases := map[string]func(f *fixture){
		"off":             func(f *fixture) { f.srv.PortalTaskClaims = PortalTaskClaimsConfig{} },
		"no projects":     func(f *fixture) { f.srv.PortalTaskClaims = PortalTaskClaimsConfig{Enabled: true} },
		"projects only":   func(f *fixture) { f.srv.PortalTaskClaims = PortalTaskClaimsConfig{Projects: []string{"sample"}} },
		"portal disabled": func(f *fixture) { f.srv.Policy.PortalReaderEnabled = false },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f, probe := portalClaimsFixture(t, true, "sample")
			change(f)
			f.h = f.srv.Handler()
			probe.reset()
			for _, tok := range []string{"tok-portal", tokHuman} {
				if w := f.do(http.MethodGet, claimsURL(map[string]string{"project": "sample"}), tok, nil); w.Code != http.StatusNotFound {
					t.Fatalf("%s: unregistered route answered %d %s", tok, w.Code, w.Body.String())
				}
			}
			if probe.lists != 0 {
				t.Fatal("an unregistered route read the store")
			}
		})
	}
}

func TestPortalTaskClaimsCallersAndRoutes(t *testing.T) {
	f, probe := portalClaimsFixture(t, true, "sample")
	owner := addOwnerSession(t, f, "owner-a", "owner-a-uid", v1alpha1.PhaseRunning, &f.now, false)
	claim := addPortalClaim(t, f, "issue:1", owner)
	f.auth[tokCoordinator] = saIdentity(coordinatorSA)
	session := f.sessionPod("bystander", "dev", 0)
	raw := claimsURL(map[string]string{"project": "sample"})
	for _, tok := range []string{tokHuman, tokClient, tokStranger, tokCoordinator, session} {
		if w := f.do(http.MethodGet, raw, tok, nil); w.Code != http.StatusForbidden {
			t.Fatalf("%s answered %d %s", tok, w.Code, w.Body.String())
		}
	}
	if w := f.do(http.MethodPost, raw, "tok-portal", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %d", w.Code)
	}
	// The portal reader is still refused on the coordination routes.
	for _, path := range []string{apiv1.TaskClaimsPath + "?project=sample", apiv1.TaskClaimsPath + "/" + claim.Name + "?project=sample", apiv1.PortalTaskClaimsPath + "/extra?project=sample"} {
		w := f.do(http.MethodGet, path, "tok-portal", nil)
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), claim.Name) {
			t.Fatalf("portal reader read %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample", "tok-portal", nil); w.Code != http.StatusForbidden {
		t.Fatalf("/v1/task-claims answered the portal reader %d", w.Code)
	}
	probe.reset()
	page, _ := readPortalClaims(t, f, raw)
	if page.State != apiv1.PortalTaskClaimsStateAvailable || page.Reason != apiv1.PortalTaskClaimsReasonComplete || len(page.Items) != 1 || page.Items[0].Name != claim.Name || probe.lists != 1 {
		t.Fatalf("portal page %+v lists=%d", page, probe.lists)
	}
	// A replaced portal pod is refused.
	id := f.auth["tok-portal"]
	id.PodUID = "another-pod-uid"
	f.auth["tok-portal"] = id
	if w := f.do(http.MethodGet, raw, "tok-portal", nil); w.Code != http.StatusForbidden {
		t.Fatalf("replaced portal pod answered %d", w.Code)
	}
}

func TestPortalTaskClaimsQueryStrict(t *testing.T) {
	f, probe := portalClaimsFixture(t, true, "sample")
	good, err := encodePortalClaimsCursor(portalClaimsCursor{Continue: "page-1", Project: "sample", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	extra := encodeTestCursor(`{"continue":"page-1","limit":50,"rv":"1"}`)
	trailing := encodeTestCursor(`{"continue":"page-1","limit":50}{}`)
	empty := encodeTestCursor(`{"continue":"","limit":50}`)
	base := apiv1.PortalTaskClaimsPath + "?"
	bad := []string{
		apiv1.PortalTaskClaimsPath,
		base + "limit=50",
		base + "project=sample&project=sample",
		base + "project=sample&extra=1",
		base + "project=sample;limit=50",
		base + "project=Sample",
		base + "project=",
		base + "project=sample&limit=0",
		base + "project=sample&limit=101",
		base + "project=sample&limit=050",
		base + "project=sample&limit=+5",
		base + "project=sample&limit=",
		base + "project=sample&cursor=",
		base + "project=sample&cursor=not+base64",
		base + "project=sample&cursor=" + strings.Repeat("A", apiv1.PortalTaskClaimsCursorMax+1),
		base + "project=sample&cursor=" + good + "&limit=40",
		base + "project=sample&cursor=" + extra,
		base + "project=sample&cursor=" + trailing,
		base + "project=sample&cursor=" + empty,
		base + "project=sample&archived=only",
		base + "project=sample&continue=page-1",
	}
	probe.reset()
	for _, raw := range bad {
		if w := f.do(http.MethodGet, raw, "tok-portal", nil); w.Code != http.StatusBadRequest {
			t.Fatalf("%s answered %d %s", raw, w.Code, w.Body.String())
		}
	}
	if probe.lists != 0 {
		t.Fatal("a refused query read the store")
	}
	encodedPath := httptest.NewRequest(http.MethodGet, "/v1/portal/task%2Dclaims?project=sample", nil)
	if _, err := parsePortalTaskClaimsQuery(encodedPath); err == nil {
		t.Fatal("a non-canonical RawPath was accepted")
	}
	if w := f.do(http.MethodGet, "/v1/portal/task%2Dclaims?project=sample", "tok-portal", nil); w.Code == http.StatusOK {
		t.Fatal("a non-canonical path was served")
	}
	page, _ := readPortalClaims(t, f, base+"project=sample&cursor="+good)
	if page.Limit != 50 {
		t.Fatalf("default limit %d", page.Limit)
	}
}

func encodeTestCursor(doc string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(doc))
}

func TestPortalTaskClaimsUnknownStates(t *testing.T) {
	t.Run("unconfigured", func(t *testing.T) {
		f, probe := portalClaimsFixture(t, true, "sample")
		f.srv.TaskClaims = nil
		probe.reset()
		page, _ := readPortalClaims(t, f, claimsURL(map[string]string{"project": "sample"}))
		if page.State != apiv1.PortalTaskClaimsStateUnknown || page.Reason != apiv1.PortalTaskClaimsReasonUnconfigured || page.Complete || len(page.Items) != 0 || probe.lists != 0 {
			t.Fatalf("unconfigured %+v", page)
		}
	})
	t.Run("scope", func(t *testing.T) {
		f, probe := portalClaimsFixture(t, true, "sample", "other")
		owner := addOwnerSession(t, f, "owner-a", "owner-a-uid", v1alpha1.PhaseRunning, &f.now, false)
		addPortalClaim(t, f, "issue:1", owner)
		for _, project := range []string{"other", "unlisted"} {
			probe.reset()
			page, body := readPortalClaims(t, f, claimsURL(map[string]string{"project": project, "limit": "100"}))
			if page.State != apiv1.PortalTaskClaimsStateUnknown || page.Reason != apiv1.PortalTaskClaimsReasonScopeUnconfirmed || page.Project != project || page.Limit != 100 || probe.lists != 0 || len(probe.gets) != 0 || strings.Contains(body, "task-claim.") {
				t.Fatalf("%s: %+v lists=%d", project, page, probe.lists)
			}
		}
	})
	t.Run("claim scope is not portal scope", func(t *testing.T) {
		f, probe := portalClaimsFixture(t, true, "other")
		owner := addOwnerSession(t, f, "owner-a", "owner-a-uid", v1alpha1.PhaseRunning, &f.now, false)
		addPortalClaim(t, f, "issue:1", owner)
		probe.reset()
		page, body := readPortalClaims(t, f, claimsURL(map[string]string{"project": "sample"}))
		if page.State != apiv1.PortalTaskClaimsStateUnknown || page.Reason != apiv1.PortalTaskClaimsReasonScopeUnconfirmed || probe.lists != 0 || strings.Contains(body, "task-claim.") {
			t.Fatalf("sample is readable for claims but not listed for the portal: %+v lists=%d", page, probe.lists)
		}
	})
	t.Run("cursor for another project", func(t *testing.T) {
		f, probe := portalClaimsFixture(t, true, "sample")
		other, err := encodePortalClaimsCursor(portalClaimsCursor{Continue: "page-1", Project: "other", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		probe.reset()
		w := f.do(http.MethodGet, claimsURL(map[string]string{"project": "sample", "cursor": other}), "tok-portal", nil)
		if w.Code != http.StatusBadRequest || probe.lists != 0 {
			t.Fatalf("cross-project cursor: %d lists=%d", w.Code, probe.lists)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		f, probe := portalClaimsFixture(t, true, "sample")
		owner := addOwnerSession(t, f, "owner-a", "owner-a-uid", v1alpha1.PhaseRunning, &f.now, false)
		addPortalClaim(t, f, "issue:1", owner)
		probe.listErr = errors.New("synthetic store failure")
		page, _ := readPortalClaims(t, f, claimsURL(map[string]string{"project": "sample"}))
		if page.State != apiv1.PortalTaskClaimsStateUnknown || page.Reason != apiv1.PortalTaskClaimsReasonUnavailable || len(page.Items) != 0 {
			t.Fatalf("store failure %+v", page)
		}
		probe.listErr = nil
		expired, err := encodePortalClaimsCursor(portalClaimsCursor{Continue: "expired-token", Project: "sample", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		page, _ = readPortalClaims(t, f, claimsURL(map[string]string{"project": "sample", "cursor": expired}))
		if page.State != apiv1.PortalTaskClaimsStateUnknown || page.Reason != apiv1.PortalTaskClaimsReasonUnavailable || len(page.Items) != 0 {
			t.Fatalf("expired continue %+v", page)
		}
	})
}

func TestPortalTaskClaimsEmptyOnlyOnACompleteFirstPage(t *testing.T) {
	f, _ := portalClaimsFixture(t, true, "sample")
	page, body := readPortalClaims(t, f, claimsURL(map[string]string{"project": "sample"}))
	if page.State != apiv1.PortalTaskClaimsStateEmpty || page.Reason != apiv1.PortalTaskClaimsReasonComplete || !page.Complete || page.NextCursor != "" || !strings.Contains(body, `"items":[]`) || !page.ObservedAt.Equal(f.now) {
		t.Fatalf("empty %+v %s", page, body)
	}
	owner := addOwnerSession(t, f, "owner-a", "owner-a-uid", v1alpha1.PhaseRunning, &f.now, false)
	for i := 1; i <= 3; i++ {
		addPortalClaim(t, f, "issue:"+strconv.Itoa(i), owner)
	}
	// A continued page with no items is Available/Complete, never Empty.
	past, err := encodePortalClaimsCursor(portalClaimsCursor{Continue: "page-3", Project: "sample", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	page, _ = readPortalClaims(t, f, claimsURL(map[string]string{"project": "sample", "cursor": past}))
	if page.State != apiv1.PortalTaskClaimsStateAvailable || page.Reason != apiv1.PortalTaskClaimsReasonComplete || len(page.Items) != 0 || !page.Complete {
		t.Fatalf("continued empty page %+v", page)
	}
}

func TestPortalTaskClaimsPagesWithoutDuplicates(t *testing.T) {
	f, probe := portalClaimsFixture(t, true, "sample")
	recent := f.now.Add(-time.Minute)
	owners := []coord.SessionIdentity{
		addOwnerSession(t, f, "owner-a", "owner-a-uid", v1alpha1.PhaseRunning, &recent, false),
		addOwnerSession(t, f, "owner-b", "owner-b-uid", v1alpha1.PhaseIdle, &recent, false),
		addOwnerSession(t, f, "owner-c", "owner-c-uid", v1alpha1.PhaseSuspended, nil, false),
	}
	for i := 1; i <= 120; i++ {
		addPortalClaim(t, f, "issue:"+strconv.Itoa(i), owners[i%3])
	}
	seen := map[string]bool{}
	raw := claimsURL(map[string]string{"project": "sample", "limit": "50"})
	sizes := []int{}
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("paging did not end")
		}
		probe.reset()
		page, _ := readPortalClaims(t, f, raw)
		if probe.lists != 1 {
			t.Fatalf("one request made %d store lists", probe.lists)
		}
		if len(probe.gets) != 3 {
			t.Fatalf("owner gets %v", probe.gets)
		}
		for key, n := range probe.gets {
			if n != 1 {
				t.Fatalf("owner %s read %d times on one page", key, n)
			}
		}
		for _, item := range page.Items {
			if seen[item.UID] {
				t.Fatalf("duplicate %s", item.Name)
			}
			seen[item.UID] = true
			if item.Owner.State == apiv1.PortalClaimOwnerUnknown {
				t.Fatalf("owner unknown %+v", item.Owner)
			}
		}
		sizes = append(sizes, len(page.Items))
		if page.NextCursor == "" {
			if page.Reason != apiv1.PortalTaskClaimsReasonComplete || !page.Complete {
				t.Fatalf("last page %+v", page)
			}
			break
		}
		if page.Reason != apiv1.PortalTaskClaimsReasonTruncated || page.Complete {
			t.Fatalf("truncated page %+v", page)
		}
		raw = claimsURL(map[string]string{"project": "sample", "limit": "50", "cursor": page.NextCursor})
	}
	if !slices.Equal(sizes, []int{50, 50, 20}) || len(seen) != 120 {
		t.Fatalf("pages %v total %d", sizes, len(seen))
	}
}

func TestPortalTaskClaimsOwnerStates(t *testing.T) {
	f, probe := portalClaimsFixture(t, true, "sample")
	recent, old, future := f.now.Add(-time.Minute), f.now.Add(-time.Hour), f.now.Add(time.Hour)
	edge := f.now.Add(-portalFreshFor)
	type want struct {
		state, freshness string
		beat             *time.Time
	}
	cases := map[string]struct {
		owner coord.SessionIdentity
		want  want
	}{
		"issue:1":  {addOwnerSession(t, f, "running", "running-uid", v1alpha1.PhaseRunning, &recent, false), want{apiv1.PortalClaimOwnerRunning, apiv1.PortalClaimFreshnessObserved, &recent}},
		"issue:2":  {addOwnerSession(t, f, "idle-stale", "idle-stale-uid", v1alpha1.PhaseIdle, &old, false), want{apiv1.PortalClaimOwnerIdle, apiv1.PortalClaimFreshnessStale, &old}},
		"issue:3":  {addOwnerSession(t, f, "draining-edge", "draining-edge-uid", v1alpha1.PhaseDraining, &edge, false), want{apiv1.PortalClaimOwnerDraining, apiv1.PortalClaimFreshnessObserved, &edge}},
		"issue:4":  {addOwnerSession(t, f, "future", "future-uid", v1alpha1.PhaseRunning, &future, false), want{apiv1.PortalClaimOwnerRunning, apiv1.PortalClaimFreshnessUnknown, nil}},
		"issue:5":  {addOwnerSession(t, f, "pending", "pending-uid", v1alpha1.PhasePending, nil, false), want{apiv1.PortalClaimOwnerPending, apiv1.PortalClaimFreshnessUnknown, nil}},
		"issue:6":  {addOwnerSession(t, f, "suspended", "suspended-uid", v1alpha1.PhaseSuspended, &old, false), want{apiv1.PortalClaimOwnerSuspended, apiv1.PortalClaimFreshnessStale, &old}},
		"issue:7":  {addOwnerSession(t, f, "archived", "archived-uid", v1alpha1.PhaseArchived, nil, false), want{apiv1.PortalClaimOwnerArchived, apiv1.PortalClaimFreshnessUnknown, nil}},
		"issue:8":  {addOwnerSession(t, f, "failed", "failed-uid", v1alpha1.PhaseFailed, &recent, false), want{apiv1.PortalClaimOwnerFailed, apiv1.PortalClaimFreshnessObserved, &recent}},
		"issue:9":  {addOwnerSession(t, f, "deleting", "deleting-uid", v1alpha1.PhaseRunning, &recent, true), want{apiv1.PortalClaimOwnerDeleting, apiv1.PortalClaimFreshnessObserved, &recent}},
		"issue:10": {addOwnerSession(t, f, "no-phase", "no-phase-uid", "", &recent, false), want{apiv1.PortalClaimOwnerUnknown, apiv1.PortalClaimFreshnessUnknown, nil}},
		"issue:11": {addOwnerSession(t, f, "odd-phase", "odd-phase-uid", "Exploded", &recent, false), want{apiv1.PortalClaimOwnerUnknown, apiv1.PortalClaimFreshnessUnknown, nil}},
		"issue:12": {coord.SessionIdentity{Namespace: sessionNS, Name: "replaced", UID: "replaced-old-uid"}, want{apiv1.PortalClaimOwnerReplaced, apiv1.PortalClaimFreshnessUnknown, nil}},
		"issue:13": {coord.SessionIdentity{Namespace: sessionNS, Name: "gone", UID: "gone-uid"}, want{apiv1.PortalClaimOwnerNotFound, apiv1.PortalClaimFreshnessUnknown, nil}},
		"issue:14": {coord.SessionIdentity{Namespace: sessionNS, Name: "broken", UID: "broken-uid"}, want{apiv1.PortalClaimOwnerUnknown, apiv1.PortalClaimFreshnessUnknown, nil}},
		"issue:15": {coord.SessionIdentity{Namespace: "elsewhere", Name: "running", UID: "running-uid"}, want{apiv1.PortalClaimOwnerUnknown, apiv1.PortalClaimFreshnessUnknown, nil}},
	}
	addOwnerSession(t, f, "replaced", "replaced-new-uid", v1alpha1.PhaseRunning, &recent, false)
	addOwnerSession(t, f, "broken", "broken-uid", v1alpha1.PhaseRunning, &recent, false)
	probe.getErrFor["broken"] = errors.New("synthetic get failure")
	byKey := map[string]string{}
	for key, tc := range cases {
		byKey[addPortalClaim(t, f, key, tc.owner).Name] = key
	}
	probe.reset()
	page, body := readPortalClaims(t, f, claimsURL(map[string]string{"project": "sample", "limit": "100"}))
	if len(page.Items) != len(cases) {
		t.Fatalf("items %d", len(page.Items))
	}
	if probe.gets[types.NamespacedName{Namespace: "elsewhere", Name: "running"}.String()] != 0 {
		t.Fatal("an owner outside the session namespace was read")
	}
	for _, item := range page.Items {
		key := byKey[item.Name]
		tc := cases[key]
		got := item.Owner
		if item.TaskKey != key || item.Repository != "alias" || item.GitHub != "thaynes43/dev-env" || !item.CreatedAt.Equal(f.now) || got.Name != tc.owner.Name || got.UID != tc.owner.UID {
			t.Fatalf("%s item %+v", key, item)
		}
		if got.State != tc.want.state || got.Freshness != tc.want.freshness || (got.HeartbeatAt == nil) != (tc.want.beat == nil) || (got.HeartbeatAt != nil && !got.HeartbeatAt.Equal(*tc.want.beat)) {
			t.Fatalf("%s owner %+v want %+v", key, got, tc.want)
		}
	}
	// Newest-first sorting is the portal's; the operator keeps store order.
	if !sort.SliceIsSorted(page.Items, func(i, j int) bool { return page.Items[i].Name < page.Items[j].Name }) {
		t.Fatal("store order changed")
	}
	for _, leak := range []string{"secret-actor", "SECRET", "secret-default-branch", claimDigest, "elsewhere", "dev-agents", "podUID", "serviceAccount", "stopState", "namespace"} {
		if strings.Contains(body, leak) {
			t.Fatalf("withheld %q appeared: %s", leak, body)
		}
	}
}

func TestPortalTaskClaimsJSONKeysAreTheAllowlist(t *testing.T) {
	f, _ := portalClaimsFixture(t, true, "sample")
	recent := f.now.Add(-time.Minute)
	owner := addOwnerSession(t, f, "owner-a", "owner-a-uid", v1alpha1.PhaseRunning, &recent, false)
	for i := 1; i <= 3; i++ {
		addPortalClaim(t, f, "issue:"+strconv.Itoa(i), owner)
	}
	gone := coord.SessionIdentity{Namespace: sessionNS, Name: "gone", UID: "gone-uid"}
	addPortalClaim(t, f, "task:no-owner", gone)
	_, body := readPortalClaims(t, f, claimsURL(map[string]string{"project": "sample", "limit": "2"}))
	checkKeys(t, body, true)
	page, body := readPortalClaims(t, f, claimsURL(map[string]string{"project": "sample", "limit": "100"}))
	checkKeys(t, body, false)
	sawOptional := false
	for _, item := range page.Items {
		sawOptional = sawOptional || item.Owner.HeartbeatAt != nil
	}
	if !sawOptional {
		t.Fatal("no owner carried a heartbeat")
	}
}

func checkKeys(t *testing.T, body string, truncated bool) {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(apiv1.PortalTaskClaimsKeys)
	if truncated {
		want = append(want, apiv1.PortalTaskClaimsOptional...)
	}
	if got := sortedKeys(doc); !slices.Equal(got, sortedCopy(want)) {
		t.Fatalf("page keys %v want %v", got, sortedCopy(want))
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(doc["items"], &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if got := sortedKeys(item); !slices.Equal(got, sortedCopy(apiv1.PortalTaskClaimItemKeys)) {
			t.Fatalf("item keys %v", got)
		}
		var owner map[string]json.RawMessage
		if err := json.Unmarshal(item["owner"], &owner); err != nil {
			t.Fatal(err)
		}
		got := sortedKeys(owner)
		withBeat := sortedCopy(append(slices.Clone(apiv1.PortalTaskClaimOwnerKeys), apiv1.PortalTaskClaimOwnerOptional...))
		if !slices.Equal(got, sortedCopy(apiv1.PortalTaskClaimOwnerKeys)) && !slices.Equal(got, withBeat) {
			t.Fatalf("owner keys %v", got)
		}
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

func TestPortalTaskClaimsResponseBound(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	page := apiv1.PortalTaskClaims{Version: apiv1.PortalTaskClaimsVersion, Project: "sample", State: apiv1.PortalTaskClaimsStateAvailable, Reason: apiv1.PortalTaskClaimsReasonComplete, Complete: true, Limit: 100, ObservedAt: now}
	for i := 0; i < 100; i++ {
		page.Items = append(page.Items, apiv1.PortalTaskClaimItem{Name: fmt.Sprintf("task-claim.%032x.%032x", i, i), UID: fmt.Sprintf("uid-%d", i), Repository: "alias", TaskKey: "issue:" + strconv.Itoa(i+1), GitHub: "thaynes43/dev-env", CreatedAt: now,
			Owner: apiv1.PortalTaskClaimOwner{Name: "owner", UID: "owner-uid", State: apiv1.PortalClaimOwnerNotFound, Freshness: apiv1.PortalClaimFreshnessUnknown}})
	}
	if got := boundPortalTaskClaims(page, apiv1.PortalTaskClaimsMaxBytes); got.State != apiv1.PortalTaskClaimsStateAvailable || len(got.Items) != 100 {
		t.Fatalf("a full page within the bound was refused: %s/%s", got.State, got.Reason)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, max := range []int{len(encoded), 1024} {
		got := boundPortalTaskClaims(page, max)
		if got.State != apiv1.PortalTaskClaimsStateUnknown || got.Reason != apiv1.PortalTaskClaimsReasonUnavailable || len(got.Items) != 0 || got.Items == nil || got.Complete || apiv1.ValidatePortalTaskClaims(got) != nil {
			t.Fatalf("over the bound (%d) was not Unavailable: %+v", max, got.State)
		}
	}
	invalid := page
	invalid.Items = append(slices.Clone(page.Items[:1]), page.Items[0])
	if got := boundPortalTaskClaims(invalid, apiv1.PortalTaskClaimsMaxBytes); got.State != apiv1.PortalTaskClaimsStateUnknown || got.Reason != apiv1.PortalTaskClaimsReasonUnavailable {
		t.Fatal("a page that fails the contract was served")
	}
}
