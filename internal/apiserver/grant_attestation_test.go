package apiserver

import (
	"context"
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

func attestFixture(t *testing.T) (*fixture, string, apiv1.Grant) {
	t.Helper()
	f := newFixture(t)
	f.srv.GrantAttestation = true
	f.h = f.srv.Handler()
	tok := f.sessionPod("haynes-ops-1006-100000", "full", 0)
	g := f.request(tok, apiv1.CreateGrantRequest{Type: "credential", Credential: "proxmox", Reason: "maintain a guest", TTL: "1h"}, http.StatusCreated)
	if g.ScopeDigest == "" {
		t.Fatal("the grant view has no scope digest")
	}
	withUID(f, g.Name)
	return f, tok, g
}

// withUID gives a grant the UID a real API server assigns.
func withUID(f *fixture, name string) {
	f.t.Helper()
	live := f.grant(name)
	live.UID = uuid.NewUUID()
	if err := f.c.Update(context.Background(), live); err != nil {
		f.t.Fatal(err)
	}
}

func body(g apiv1.Grant) apiv1.AttestGrantRequest {
	return apiv1.AttestGrantRequest{ApprovedBy: "coordinator:meatball", Ref: "msg-123", ScopeDigest: g.ScopeDigest}
}

// Default off: the route does not exist.
func TestGrantAttestationRouteIsAbsentByDefault(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "full", 0)
	g := f.request(tok, kubeReq(), http.StatusCreated)
	if w := f.do(http.MethodPost, apiv1.GrantAttestationPath(g.Name), tok, body(g)); w.Code != http.StatusNotFound {
		t.Fatalf("route answered %d with the flag off", w.Code)
	}
	if _, ok := f.grant(g.Name).Annotations[grantattest.Annotation]; ok {
		t.Fatal("an attestation was written with the flag off")
	}
}

// The attestor comes from the token, the statement binds the exact grant, and a
// retry of the same claim is idempotent.
func TestGrantAttestationRecordsTheCallersClaim(t *testing.T) {
	f, tok, g := attestFixture(t)
	w := f.do(http.MethodPost, apiv1.GrantAttestationPath(g.Name), tok, body(g))
	if w.Code != http.StatusCreated {
		t.Fatalf("attest: %d %s", w.Code, w.Body.String())
	}
	st, err := grantattest.Parse(f.grant(g.Name).Annotations[grantattest.Annotation])
	if err != nil {
		t.Fatal(err)
	}
	live := f.grant(g.Name)
	pod := f.auth[tok].PodUID
	if st.Attestor.Session != "haynes-ops-1006-100000" || st.Attestor.SessionUID != string(live.Spec.Requester.SessionUID) || st.Attestor.PodUID != pod ||
		st.GrantUID != string(live.UID) || st.ScopeDigest != g.ScopeDigest || st.Approver.String() != "coordinator:meatball" || st.Ref != "msg-123" {
		t.Fatalf("statement %+v", st)
	}
	if v := decode[apiv1.Grant](t, w); v.Attestation == nil || v.Attestation.ApprovedBy != "coordinator:meatball" || v.Phase != apiv1.GrantPending {
		t.Fatalf("view %+v", v)
	}
	if live.Status.Phase != "" && live.Status.Phase != v1alpha1.GrantPending || live.Status.ApprovedBy != "" {
		t.Fatalf("the route wrote a decision: %+v", live.Status)
	}
	if w := f.do(http.MethodPost, apiv1.GrantAttestationPath(g.Name), tok, body(g)); w.Code != http.StatusOK {
		t.Fatalf("identical retry: %d", w.Code)
	}
	other := body(g)
	other.Ref = "msg-999"
	wantError(t, f.do(http.MethodPost, apiv1.GrantAttestationPath(g.Name), tok, other), http.StatusConflict, apiv1.CodeConflict) // one per grant
}

func TestGrantAttestationRefusals(t *testing.T) {
	f, tok, g := attestFixture(t)
	path := apiv1.GrantAttestationPath(g.Name)
	stranger := f.sessionPod("haynes-ops-1006-100001", "full", 0)

	// Forged actor: the body cannot name an attestor, and another session or a
	// human or client token is refused.
	wantError(t, f.do(http.MethodPost, path, tok, `{"approvedBy":"user","ref":"r1","scopeDigest":"`+g.ScopeDigest+`","attestor":{"session":"someone-else"}}`), http.StatusBadRequest, apiv1.CodeBadRequest)
	wantError(t, f.do(http.MethodPost, path, stranger, body(g)), http.StatusForbidden, apiv1.CodeForbidden)
	for _, other := range []string{tokHuman, tokClient} {
		wantError(t, f.do(http.MethodPost, path, other, body(g)), http.StatusForbidden, apiv1.CodeForbidden)
	}
	// Widened or changed scope: a digest the session did not read from this grant.
	wide := body(g)
	wide.ScopeDigest = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	wantError(t, f.do(http.MethodPost, path, tok, wide), http.StatusConflict, apiv1.CodeConflict)
	// Malformed claims.
	for _, b := range []apiv1.AttestGrantRequest{{ApprovedBy: "tom", Ref: "r", ScopeDigest: g.ScopeDigest}, {ApprovedBy: "user", Ref: "has space", ScopeDigest: g.ScopeDigest}, {ApprovedBy: "coordinator:", Ref: "r", ScopeDigest: g.ScopeDigest}} {
		wantError(t, f.do(http.MethodPost, path, tok, b), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	}
	if _, ok := f.grant(g.Name).Annotations[grantattest.Annotation]; ok {
		t.Fatal("a refused attestation was written")
	}

	// A grant no longer waiting: released, decided, or past its window.
	f.do(http.MethodDelete, apiv1.GrantPath(g.Name), tok, nil)
	wantError(t, f.do(http.MethodPost, path, tok, body(g)), http.StatusConflict, apiv1.CodeConflict)
	late := f.request(tok, apiv1.CreateGrantRequest{Type: "credential", Credential: "proxmox", Reason: "again", TTL: "30m"}, http.StatusCreated)
	withUID(f, late.Name)
	f.now = f.now.Add(grantattest.PendingWindow)
	wantError(t, f.do(http.MethodPost, apiv1.GrantAttestationPath(late.Name), tok, body(late)), http.StatusConflict, apiv1.CodeConflict)
	f.now = f.now.Add(-grantattest.PendingWindow)
	f.setGrantStatus(late.Name, v1alpha1.GrantDenied, nil)
	wantError(t, f.do(http.MethodPost, apiv1.GrantAttestationPath(late.Name), tok, body(late)), http.StatusConflict, apiv1.CodeConflict)
}
