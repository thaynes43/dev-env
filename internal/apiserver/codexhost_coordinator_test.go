package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexhostpool"
	"github.com/thaynes43/dev-env/internal/codexworkbench"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestCoordinatorPoolFactsAndLease(t *testing.T) {
	for _, shape := range []string{"reserved", "workbench-alias", "workbench-default", "workbench-pod", "unknown-profile"} {
		t.Run(shape, func(t *testing.T) {
			f := newCodexFixture(t)
			f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: "reserved", Namespace: sessionNS}
			f.tmpl.Profiles["wb-alias"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
			profile := "dev"
			name := "ordinary"
			switch shape {
			case "reserved":
				name = "reserved"
			case "workbench-alias":
				profile = "wb-alias"
			case "workbench-default":
				profile = ""
				f.tmpl.DefaultProfile = "wb-alias"
			case "unknown-profile":
				profile = "removed"
			}
			f.addCodexHost(name, codexHostOpt{profile: profile})
			if shape == "workbench-default" {
				creationProfile(t, f, name, "")
			}
			if shape == "workbench-pod" {
				var p corev1.Pod
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: name}, &p); err != nil {
					t.Fatal(err)
				}
				p.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
				if err := f.c.Update(context.Background(), &p); err != nil {
					t.Fatal(err)
				}
			}
			hosts, _, err := f.srv.codexHostFacts(context.Background(), f.tmpl)
			if err != nil || len(hosts) != 1 {
				t.Fatalf("facts: %v %+v", err, hosts)
			}
			h := hosts[0]
			if codexhostpool.Suitable(h, codexhostpool.Caller{Kind: codexhostpool.KindClient}, h.ResolvedProfile, "dev-env", f.now) {
				t.Fatalf("facts admitted %+v", h)
			}
			// Simulate the target changing after a usable list read. The lease must use
			// fresh Session/Pod facts, without an annotation or hands-off entry.
			s := f.session(name)
			got, _, err := f.srv.leaseCodexHost(context.Background(), codexLeaseTarget{name: name, uid: string(s.UID), profile: h.ResolvedProfile, repo: "dev-env", caller: codexhostpool.Caller{Kind: codexhostpool.KindClient, Name: "client", LaunchID: "launch-identifier"}, tmpl: f.tmpl})
			if err != nil || got == codexLeaseWritten || f.session(name).Annotations[codexHostLeaseAnnotation] != "" {
				t.Fatalf("lease crossed fence: %v %v", got, err)
			}
		})
	}
}

func TestCoordinatorLeaseRejectsProtectionRace(t *testing.T) {
	for _, race := range []string{"profile", "pod-account", "reserved-name"} {
		t.Run(race, func(t *testing.T) {
			f := newCodexFixture(t)
			f.addCodexHost("ordinary", codexHostOpt{profile: "dev"})
			hosts, _, err := f.srv.codexHostFacts(context.Background(), f.tmpl)
			who := codexhostpool.Caller{Kind: codexhostpool.KindClient, Name: "client", LaunchID: "launchidentifier0123"}
			if err != nil || len(hosts) != 1 || !codexhostpool.Suitable(hosts[0], who, "dev", "dev-env", f.now) {
				t.Fatalf("not a usable list snapshot: %v %+v", err, hosts)
			}
			s := f.session("ordinary")
			want := codexLeaseTarget{name: s.Name, uid: string(s.UID), profile: "dev", repo: "dev-env", caller: who, tmpl: f.tmpl}
			switch race {
			case "profile":
				f.tmpl.Profiles["wb-alias"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
				s.Spec.Profile = "wb-alias"
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			case "pod-account":
				var p corev1.Pod
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: s.Name}, &p); err != nil {
					t.Fatal(err)
				}
				p.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
				if err := f.c.Update(context.Background(), &p); err != nil {
					t.Fatal(err)
				}
			case "reserved-name":
				f.srv.CodexWorkbenchHosts = codexworkbench.Policy{Name: s.Name, Namespace: sessionNS}
			}
			got, _, err := f.srv.leaseCodexHost(context.Background(), want)
			if err != nil || got == codexLeaseWritten {
				t.Fatalf("lease raced into protection: %v %v", got, err)
			}
			var all v1alpha1.AgentSessionList
			if err := f.c.List(context.Background(), &all); err != nil {
				t.Fatal(err)
			}
			if len(all.Items) != 1 {
				t.Fatal("fallback created another host")
			}
		})
	}
}

func TestCoordinatorPoolRequestedAccount(t *testing.T) {
	for _, profile := range []string{"wb-alias", "default", "removed"} {
		t.Run(profile, func(t *testing.T) {
			f := newCodexFixture(t)
			f.tmpl.Profiles["wb-alias"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
			req := apiv1.CodexHostSelectRequest{Repo: "dev-env", Profile: profile, LaunchID: "launchidentifier0123"}
			if profile == "default" {
				f.tmpl.DefaultProfile = "wb-alias"
				req.Profile = ""
			}
			w := f.do(http.MethodPost, apiv1.CodexHostsSelectPath, tokClient, req)
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			var result codexhostpool.Result
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Host != "" || result.Action != "" || result.Reason != codexhostpool.ReasonProtected {
				t.Fatalf("pool account result %+v", result)
			}
		})
	}
}
