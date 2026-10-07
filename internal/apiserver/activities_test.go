package apiserver

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// declare-activity's API enforces v1's rules (D-17, D-66): a scope, a 45m
// default, an 8h cap, a 2h cap for cluster; the declarer comes from the token.
func TestDeclareActivity(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1007-171500", "full", 0)
	w := f.do(http.MethodPost, apiv1.ActivitiesPath, tok, apiv1.DeclareActivityRequest{
		Description: " restarting z2m ", Scope: []string{"home-automation", "zigbee2mqtt", "home-automation"}})
	if w.Code != http.StatusCreated {
		t.Fatalf("declare: %d %s", w.Code, w.Body.String())
	}
	a := decode[apiv1.Activity](t, w)
	if !strings.HasPrefix(a.Name, "act-172226-") || a.Description != "restarting z2m" || strings.Join(a.Scope, ",") != "home-automation,zigbee2mqtt" ||
		a.DeclaredBy != "session/haynes-ops-1007-171500" || a.Session != "haynes-ops-1007-171500" || !a.ExpiresAt.Equal(f.now.Add(45*time.Minute)) {
		t.Errorf("activity %+v", a)
	}
	w = f.do(http.MethodPost, apiv1.ActivitiesPath, tokHuman, apiv1.DeclareActivityRequest{Description: "drain", Scope: []string{"cluster"}, TTL: "2h"})
	if w.Code != http.StatusCreated || decode[apiv1.Activity](t, w).Session != "" {
		t.Fatalf("a human's cluster declaration: %d %s", w.Code, w.Body.String())
	}

	l := decode[apiv1.ActivityList](t, f.do(http.MethodGet, apiv1.ActivitiesPath, tokClient, nil))
	if len(l.Activities) != 2 {
		t.Errorf("list %+v", l)
	}
	// An expired one is not listed.
	f.now = f.now.Add(3 * time.Hour)
	if l := decode[apiv1.ActivityList](t, f.do(http.MethodGet, apiv1.ActivitiesPath, tokClient, nil)); len(l.Activities) != 0 {
		t.Errorf("expired ones listed: %+v", l)
	}
	f.now = f.now.Add(-3 * time.Hour)

	if w := f.do(http.MethodDelete, apiv1.ActivityPath(a.Name), tokHuman, nil); w.Code != http.StatusOK {
		t.Errorf("end: %d %s", w.Code, w.Body.String())
	}
	wantError(t, f.do(http.MethodDelete, apiv1.ActivityPath(a.Name), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)

	for name, c := range map[string]struct {
		req   apiv1.DeclareActivityRequest
		field string
	}{
		"no scope":              {apiv1.DeclareActivityRequest{Description: "x"}, "scope"},
		"the star":              {apiv1.DeclareActivityRequest{Description: "x", Scope: []string{"*"}}, "scope[0]"},
		"two words":             {apiv1.DeclareActivityRequest{Description: "x", Scope: []string{"two words"}}, "scope[0]"},
		"no description":        {apiv1.DeclareActivityRequest{Scope: []string{"a"}}, "description"},
		"over 8h":               {apiv1.DeclareActivityRequest{Description: "x", Scope: []string{"a"}, TTL: "9h"}, "ttl"},
		"cluster over 2h":       {apiv1.DeclareActivityRequest{Description: "x", Scope: []string{"a", "cluster"}, TTL: "3h"}, "ttl"},
		"a TTL under a minute":  {apiv1.DeclareActivityRequest{Description: "x", Scope: []string{"a"}, TTL: "10s"}, "ttl"},
		"a TTL that is not one": {apiv1.DeclareActivityRequest{Description: "x", Scope: []string{"a"}, TTL: "soon"}, "ttl"},
	} {
		t.Run(name, func(t *testing.T) {
			e := wantError(t, f.do(http.MethodPost, apiv1.ActivitiesPath, tokHuman, c.req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			found := false
			for _, fe := range e.Fields {
				found = found || fe.Field == c.field
			}
			if !found {
				t.Errorf("fields %+v lack %q", e.Fields, c.field)
			}
		})
	}
	wantError(t, f.do(http.MethodPost, apiv1.ActivitiesPath, tokStranger, apiv1.DeclareActivityRequest{Description: "x", Scope: []string{"a"}}), http.StatusForbidden, apiv1.CodeForbidden)
}
