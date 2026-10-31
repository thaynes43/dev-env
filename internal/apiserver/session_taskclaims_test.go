package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func sessionClaimsRead(t *testing.T, f *fixture, name, token string) apiv1.Session {
	t.Helper()
	w := f.doHeader(http.MethodGet, apiv1.SessionPath(name), token, nil, http.Header{apiv1.SessionTaskClaimsHeader: {apiv1.SessionTaskClaimsV1}})
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	out := decode[apiv1.Session](t, w)
	if out.TaskClaims == nil {
		t.Fatal("negotiated session omitted claim observation")
	}
	return out
}

// This fixture exposes saved, explicitly bound workflow evidence; no production
// reader or second claim registry is introduced by the projection.
type claimJoinSnapshot struct {
	id         pages.AlertIdentity
	order      pages.OrderReference
	claim      apiv1.TaskClaim
	at         time.Time
	claimCalls int
}

func (s *claimJoinSnapshot) FindOrders(_ context.Context, ids []pages.AlertIdentity) (map[pages.AlertIdentity]pages.OrderObservation, error) {
	out := map[pages.AlertIdentity]pages.OrderObservation{}
	for _, id := range ids {
		out[id] = pages.OrderObservation{SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic order", ObservedAt: s.at}, Order: &pages.ResponderOrder{Identity: s.id, Order: s.order, Status: "claimed", Updated: s.order.Created}}
	}
	return out, nil
}

func (s *claimJoinSnapshot) FindClaims(_ context.Context, lookups []pages.ClaimLookup) (map[pages.ClaimLookup]pages.ClaimObservation, error) {
	s.claimCalls++
	out := map[pages.ClaimLookup]pages.ClaimObservation{}
	for _, lookup := range lookups {
		out[lookup] = pages.ClaimObservation{SourceObservation: pages.SourceObservation{Available: true, Source: "Synthetic claim evidence", ObservedAt: s.at}, Claim: &pages.ClaimEvidence{Identity: s.id, Order: s.order, Reference: pages.CanonicalClaimReference{Kind: "TaskClaim", ID: "dev-env-system/" + s.claim.Name + "/" + s.claim.UID}, ClaimOwner: pages.ClaimOwner(s.claim.Owner), Owner: "Synthetic responder", State: "claimed", StateAt: s.at, EvidenceURL: "https://example.test/evidence"}}
	}
	return out, nil
}

func TestSessionTaskClaimsCanonicalPageJoinAndContenders(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	a := claimSession(t, f, "claim-owner", humanSA)
	b := claimSession(t, f, "claim-contender", clientSA)
	winner := f.do(http.MethodPost, apiv1.TaskClaimsPath, a, claimRequest("issue:322"))
	contender := f.do(http.MethodPost, apiv1.TaskClaimsPath, b, claimRequest("issue:322"))
	if winner.Code != http.StatusCreated || contender.Code != http.StatusConflict {
		t.Fatal("ownership not atomic", winner.Code, contender.Code)
	}
	claim := decode[apiv1.TaskClaimResult](t, winner).Claim
	second := f.do(http.MethodPost, apiv1.TaskClaimsPath, a, claimRequest("issue:323"))
	if second.Code != http.StatusCreated {
		t.Fatal("second distinct task", second.Code, second.Body.String())
	}
	// Retain legacy project-only labels: an owner-label-only query would lie.
	stored, err := f.srv.TaskClaims.Get(context.Background(), claim.Name)
	if err != nil {
		t.Fatal(err)
	}
	stored.Labels = map[string]string{taskclaim.ProjectLabel: "sample"}
	if err := f.c.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	lists, gets := 0, 0
	// The live caller fence reads one Pod and one AgentSession.
	liveGets, sessionGets := 0, 0
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		liveGets++
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			sessionGets++
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	f.srv.TaskClaims.Live = interceptor.NewClient(f.c, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*coord.TaskClaimList); ok {
				lists++
				o := &client.ListOptions{}
				o.ApplyOptions(opts)
				if o.Limit != 4096 || o.Namespace != "dev-env-system" || o.Continue != "" || o.LabelSelector.String() != taskclaim.ProjectLabel+"=sample" {
					t.Fatal("unbounded or wrong claim list", o)
				}
			}
			return c.List(ctx, list, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			gets++
			return c.Get(ctx, key, obj, opts...)
		},
	})
	owned := sessionClaimsRead(t, f, "claim-owner", a).TaskClaims
	byUID := map[string]apiv1.TaskClaim{}
	for _, item := range owned.Items {
		byUID[item.UID] = item
	}
	joined := byUID[claim.UID]
	if owned.State != "Available" || len(owned.Items) != 2 || !reflect.DeepEqual(joined, claim) || lists != 1 || gets != 0 || liveGets != 2 || sessionGets != 1 {
		t.Fatalf("single uncached join lost immutable metadata: %+v, lists=%d gets=%d liveGets=%d sessionGets=%d", owned, lists, gets, liveGets, sessionGets)
	}
	liveGets, sessionGets = 0, 0
	lost := sessionClaimsRead(t, f, "claim-contender", b).TaskClaims
	if lost.State != "Unknown" || len(lost.Items) != 0 || lists != 2 || gets != 0 || liveGets != 2 || sessionGets != 1 {
		t.Fatal("contender gained custody", lost, lists, gets, liveGets, sessionGets)
	}
	snapshot := &claimJoinSnapshot{id: pages.AlertIdentity{Fingerprint: "synthetic", StartsAt: f.now.Add(-time.Minute).Format(time.RFC3339)}, order: pages.OrderReference{Key: "rem-responder-0123abcd", Created: strconv.FormatInt(f.now.Add(-time.Minute).Unix(), 10)}, claim: claim, at: f.now}
	refire := snapshot.id
	refire.StartsAt = f.now.Format(time.RFC3339)
	projected := pages.ProjectResponses(context.Background(), []pages.AlertIdentity{snapshot.id, refire}, pages.ResponseSources{Orders: snapshot, Claims: snapshot, MaxAge: time.Minute, EvidenceOrigins: []string{"https://example.test"}}, f.now)
	page := projected[snapshot.id]
	if page.Claim.ID != "dev-env-system/"+joined.Name+"/"+joined.UID || page.ClaimOwner != pages.ClaimOwner(joined.Owner) || page.ClaimCondition != "Available" || snapshot.claimCalls != 1 {
		t.Fatal("Page/session canonical claim identity or single batch drift", page, owned, snapshot.claimCalls)
	}
	if projected[refire].Claim.ID != "Unknown" || projected[refire].ClaimOwner.UID != "Unknown" {
		t.Fatal("refire inherited old custody", projected[refire])
	}
	// A stopped phase and a later HEAD do not release custody or rewrite the
	// original reported provenance into a new pin/success assertion.
	suspended := f.session("claim-owner")
	suspended.Status.Phase = "Suspended"
	suspended.Status.Agent.Head = strings.Repeat("b", 40)
	if err := f.c.Status().Update(context.Background(), suspended); err != nil {
		t.Fatal(err)
	}
	liveGets, sessionGets = 0, 0
	retained := sessionClaimsRead(t, f, "claim-owner", a).TaskClaims
	if retained.State != "Available" || len(retained.Items) != 2 || lists != 3 || gets != 0 || liveGets != 2 || sessionGets != 1 {
		t.Fatal("stopped custody lost or fanned out", retained, lists, gets, liveGets, sessionGets)
	}
	for _, item := range retained.Items {
		if item.OwnerState != "Suspended" || item.StopState != "Unknown" || item.Provenance.ReportedHead != strings.Repeat("a", 40) || item.Provenance.InitialPinState != "Unknown" {
			t.Fatal("stopped state or reported head became authority", item)
		}
	}
	// Reusing the name with a different UID never inherits old claims.
	sess := f.session("claim-owner")
	sess.UID = "replacement-uid"
	if got := f.srv.sessionTaskClaims(context.Background(), sess, &caller{kind: kindSession, session: f.session("claim-contender"), pod: sessionPodObject(t, f, "claim-contender"), identity: f.auth[b]}); got.State != "Unknown" || len(got.Items) != 0 {
		t.Fatal("name reuse inherited claim", got)
	}
}

func TestSessionTaskClaimsUnknownAndNoPartialResults(t *testing.T) {
	for _, mode := range []string{"missing store", "unreadable", "truncated", "oversize", "invalid", "no scope", "ordinary client", "missing project"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			bindCatalogFixture(t, f)
			enableClaims(f)
			token := claimSession(t, f, "unknown-claims", humanSA)
			created := f.do(http.MethodPost, apiv1.TaskClaimsPath, token, claimRequest("issue:322"))
			if created.Code != http.StatusCreated {
				t.Fatal(created.Code, created.Body.String())
			}
			calls := 0
			f.srv.TaskClaims.Live = interceptor.NewClient(f.c, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				calls++
				if err := c.List(ctx, list, opts...); err != nil {
					return err
				}
				switch mode {
				case "unreadable":
					return errors.New("private backend error")
				case "truncated":
					list.(*coord.TaskClaimList).Continue = "more"
				case "oversize":
					claims := list.(*coord.TaskClaimList)
					if len(claims.Items) != 1 {
						t.Fatal("missing valid owned claim")
					}
					valid := claims.Items[0]
					claims.Items = make([]coord.TaskClaim, int(taskclaim.DefaultLimits.PerProject)+1)
					for i := range claims.Items {
						claims.Items[i] = *valid.DeepCopy()
					}
				case "invalid":
					list.(*coord.TaskClaimList).Items = []coord.TaskClaim{{}}
				}
				return nil
			}})
			switch mode {
			case "missing store":
				f.srv.TaskClaims = nil
			case "no scope":
				f.srv.TaskClaimReadProjects = nil
			case "ordinary client":
				token = tokClient
			case "missing project":
				sess := f.session("unknown-claims")
				sess.Annotations = nil
				if err := f.c.Update(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
			}
			got := sessionClaimsRead(t, f, "unknown-claims", token).TaskClaims
			wantCalls := 0
			if mode == "unreadable" || mode == "truncated" || mode == "oversize" || mode == "invalid" {
				wantCalls = 1
			}
			wantReason := "ScopeUnconfirmed"
			switch mode {
			case "missing store", "no scope":
				wantReason = "Unconfigured"
			case "unreadable", "truncated", "oversize", "invalid":
				wantReason = "Unavailable"
			}
			if got.Reason != wantReason {
				t.Fatalf("reason=%q, want %q", got.Reason, wantReason)
			}
			if got.State != "Unknown" || len(got.Items) != 0 || strings.Contains(got.Reason, "private") || calls != wantCalls {
				t.Fatalf("failed read became empty/partial claims or leaked errors: %+v, calls=%d", got, calls)
			}
		})
	}
}

func TestSessionTaskClaimsOlderStrictClient(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	token := claimSession(t, f, "legacy-claims", humanSA)
	calls := 0
	f.srv.TaskClaims.Live = interceptor.NewClient(f.c, interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		calls++
		return errors.New("must not read")
	}})
	// Freeze the current Session shape minus this addition as an older reader.
	var fields []reflect.StructField
	typ := reflect.TypeFor[apiv1.Session]()
	for i := 0; i < typ.NumField(); i++ {
		if field := typ.Field(i); field.Name != "TaskClaims" {
			fields = append(fields, field)
		}
	}
	old := reflect.StructOf(fields)
	for _, header := range []string{"", "v0", "v2"} {
		w := f.doHeader(http.MethodGet, apiv1.SessionPath("legacy-claims"), token, nil, http.Header{apiv1.SessionTaskClaimsHeader: {header}})
		d := json.NewDecoder(w.Body)
		d.DisallowUnknownFields()
		if w.Code != http.StatusOK || d.Decode(reflect.New(old).Interface()) != nil || calls != 0 {
			t.Fatal("old strict client changed shape or caused claim IO", header, w.Code, calls)
		}
	}
	w := f.doHeader(http.MethodGet, apiv1.SessionPath("legacy-claims"), token, nil, http.Header{apiv1.SessionTaskClaimsHeader: {apiv1.SessionTaskClaimsV1}})
	d := json.NewDecoder(w.Body)
	d.DisallowUnknownFields()
	if w.Code != http.StatusOK || d.Decode(reflect.New(old).Interface()) == nil || calls != 1 {
		t.Fatal("strict legacy decoder did not detect the negotiated field", w.Code, calls)
	}
	w = f.doHeader(http.MethodGet, apiv1.SessionsPath, token, nil, http.Header{apiv1.SessionTaskClaimsHeader: {apiv1.SessionTaskClaimsV1}})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"taskClaims"`) || calls != 1 {
		t.Fatal("list added claims or multiplied reads", w.Code, calls, w.Body.String())
	}
}
