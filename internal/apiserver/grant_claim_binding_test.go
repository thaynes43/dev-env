package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/grantattest"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func TestMaintenanceTaskClaimLegacyJSONAndDigest(t *testing.T) {
	var legacy apiv1.MaintenanceTask
	if err := json.Unmarshal([]byte(`{"operation":"maintain","resource":"device"}`), &legacy); err != nil || legacy.TaskClaim != nil {
		t.Fatal(err, legacy.TaskClaim)
	}
	name := canonicalBindingName('a', 'b')
	uid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	got := maintenanceView(maintenanceSpec(&apiv1.MaintenanceTask{Operation: "maintain", TaskClaim: &apiv1.MaintenanceTaskClaim{Name: name, UID: uid, Project: "sample"}}))
	if got.TaskClaim == nil || got.TaskClaim.Name != name || got.TaskClaim.UID != uid || got.TaskClaim.Project != "sample" || got.Operation != "maintain" {
		t.Fatal(got)
	}
	g := &v1alpha1.AccessGrant{Spec: v1alpha1.AccessGrantSpec{Type: v1alpha1.GrantMaintenance, TTL: metav1.Duration{Duration: 30 * time.Minute}, Requester: v1alpha1.GrantRequester{Session: "maintenance-owner", SessionUID: "uid"}, Maintenance: &v1alpha1.MaintenanceTask{Operation: "maintain"}}}
	raw, _ := json.Marshal(g.Spec)
	if bytes.Contains(raw, []byte("taskClaim")) {
		t.Fatal("absent association entered the envelope document")
	}
	plain, plainScope := broker.MaintenanceDigest(g), grantattest.ScopeDigest(g)
	g.Spec.Maintenance.TaskClaim = &v1alpha1.MaintenanceTaskClaim{Name: name, UID: uid, Project: "sample"}
	if broker.MaintenanceDigest(g) == plain || grantattest.ScopeDigest(g) == plainScope {
		t.Fatal("present association left an existing envelope digest unchanged")
	}
	cloned := g.DeepCopy()
	cloned.Spec.Maintenance.TaskClaim.Project = "other"
	if g.Spec.Maintenance.TaskClaim.Project != "sample" {
		t.Fatal("deepcopy aliased the association")
	}
}

func TestMaintenanceTaskClaimPersistsOnExistingCreate(t *testing.T) {
	f, parent, _, req := maintenanceAPI(t)
	name := canonicalBindingName('a', 'b')
	uid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	req.Maintenance.TaskClaim = &apiv1.MaintenanceTaskClaim{Name: name, UID: uid, Project: "sample"}
	req.Maintenance.Operation = "issue:322"
	got := f.request(parent, req, http.StatusCreated)
	if got.Maintenance == nil || got.Maintenance.TaskClaim == nil || *got.Maintenance.TaskClaim != *req.Maintenance.TaskClaim {
		t.Fatal(got.Maintenance)
	}
	if got.Maintenance.Coordinator.Name != "maintenance-parent" || got.Maintenance.Coordinator.UID == "" {
		t.Fatal("stored association replaced requester identity", got.Maintenance.Coordinator)
	}
	stored := f.grant(got.Name)
	if stored.Spec.Maintenance == nil || stored.Spec.Maintenance.TaskClaim == nil || stored.Spec.Maintenance.TaskClaim.Name != name {
		t.Fatal(stored.Spec.Maintenance)
	}
	cleared := stored.DeepCopy()
	cleared.Spec.Maintenance.TaskClaim = nil
	if broker.MaintenanceDigest(stored) == broker.MaintenanceDigest(cleared) {
		t.Fatal("create path dropped the association before the digest")
	}
	plain := f.do(http.MethodGet, apiv1.GrantPath(got.Name), parent, nil)
	if plain.Code != http.StatusOK || strings.Contains(plain.Body.String(), `"taskClaims"`) || strings.Contains(plain.Body.String(), "claimBinding") {
		t.Fatal(plain.Code, plain.Body.String())
	}
	if !strings.Contains(plain.Body.String(), name) {
		t.Fatal("default view dropped the stored association", plain.Body.String())
	}
	w := f.doHeader(http.MethodGet, apiv1.GrantPath(got.Name), parent, nil, http.Header{grantClaimsTestHeader: {"v1"}})
	var wire struct {
		TaskClaims *apiv1.SessionTaskClaims `json:"taskClaims"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &wire) != nil || wire.TaskClaims == nil || wire.TaskClaims.ClaimBinding == nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if wire.TaskClaims.ClaimBinding.State != "Unknown" || wire.TaskClaims.ClaimBinding.Reason != "Unconfigured" || wire.TaskClaims.ClaimBinding.Name != "" {
		t.Fatal("unconfigured observation was treated as a binding", wire.TaskClaims.ClaimBinding)
	}
}

func TestMaintenanceClaimBindingDetail(t *testing.T) {
	for _, mode := range []string{"exact", "absent", "stale-uid", "project-mismatch", "owner-mismatch", "other-owner", "unreadable", "unconfigured", "ambiguous", "two-claims-no-ref", "crossed-name-uid", "invalid", "header-off", "scope-unconfirmed", "identity-unconfirmed"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			bindCatalogFixture(t, f)
			enableClaims(f)
			token := claimSession(t, f, "maintenance-owner", humanSA)
			first := createBindingClaim(t, f, token, "issue:322")
			second := first
			if mode == "exact" || mode == "two-claims-no-ref" || mode == "crossed-name-uid" {
				second = createBindingClaim(t, f, token, "issue:323")
			}
			other := apiv1.TaskClaim{}
			if mode == "other-owner" {
				reader := claimSession(t, f, "maintenance-reader", clientSA)
				other = createBindingClaim(t, f, reader, "issue:324")
			}
			sess := f.session("maintenance-owner")
			var ref *v1alpha1.MaintenanceTaskClaim
			switch mode {
			case "absent", "two-claims-no-ref":
			case "stale-uid":
				ref = &v1alpha1.MaintenanceTaskClaim{Name: first.Name, UID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Project: "sample"}
			case "project-mismatch":
				ref = &v1alpha1.MaintenanceTaskClaim{Name: first.Name, UID: first.UID, Project: "other"}
			case "other-owner":
				ref = &v1alpha1.MaintenanceTaskClaim{Name: other.Name, UID: other.UID, Project: "sample"}
			case "crossed-name-uid":
				ref = &v1alpha1.MaintenanceTaskClaim{Name: first.Name, UID: second.UID, Project: "sample"}
			case "invalid":
				ref = &v1alpha1.MaintenanceTaskClaim{Name: "issue:322", UID: first.UID, Project: "sample"}
			case "exact":
				ref = &v1alpha1.MaintenanceTaskClaim{Name: second.Name, UID: second.UID, Project: second.Identity.Project}
			default:
				ref = &v1alpha1.MaintenanceTaskClaim{Name: first.Name, UID: first.UID, Project: first.Identity.Project}
			}
			g := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant-maint", Namespace: sessionNS}, Spec: v1alpha1.AccessGrantSpec{Type: v1alpha1.GrantMaintenance, Requester: v1alpha1.GrantRequester{Session: sess.Name, SessionUID: sess.UID}, Maintenance: &v1alpha1.MaintenanceTask{Operation: "issue:322", Resource: first.UID, TaskClaim: ref}}}
			if mode == "identity-unconfirmed" {
				g.Spec.Requester.SessionUID = ""
			}
			if mode == "scope-unconfirmed" {
				sess.Annotations = nil
				if err := f.c.Update(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unconfigured" {
				f.srv.TaskClaims = nil
			}
			if err := f.c.Create(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			baseline := (*apiv1.SessionTaskClaims)(nil)
			if mode != "unconfigured" && mode != "identity-unconfirmed" && mode != "scope-unconfirmed" {
				baseline = sessionClaimsRead(t, f, "maintenance-owner", token).TaskClaims
			}
			lists, gets := 0, 0
			foreign := false
			if mode != "unconfigured" {
				f.srv.TaskClaims.Live = interceptor.NewClient(f.c, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						gets++
						return c.Get(ctx, key, obj, opts...)
					},
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						lists++
						for _, opt := range opts {
							applied := &client.ListOptions{}
							opt.ApplyToList(applied)
							if applied.LabelSelector != nil && strings.Contains(applied.LabelSelector.String(), "=other") {
								foreign = true
							}
						}
						if mode == "unreadable" {
							return errors.New("synthetic unreadable observation")
						}
						if err := c.List(ctx, list, opts...); err != nil {
							return err
						}
						claims, _ := list.(*coord.TaskClaimList)
						if claims == nil {
							return nil
						}
						switch mode {
						case "owner-mismatch":
							if len(claims.Items) != 1 {
								return errors.New("synthetic owner fixture")
							}
							claims.Items[0].Spec.Owner.Name = "other-owner"
							claims.Items[0].Labels = map[string]string{taskclaim.ProjectLabel: claims.Items[0].Spec.Identity.Project}
						case "ambiguous":
							if len(claims.Items) != 1 {
								return errors.New("synthetic ambiguity fixture")
							}
							claims.Items = append(claims.Items, claims.Items[0])
						}
						return nil
					},
				})
			}
			header := http.Header{grantClaimsTestHeader: {"v1"}}
			if mode == "header-off" {
				header = nil
			}
			w := f.doHeader(http.MethodGet, apiv1.GrantPath(g.Name), token, nil, header)
			if w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			if gets != 0 || foreign {
				t.Fatal("association used a claim Get or a foreign project list", gets, foreign)
			}
			var wire struct {
				Maintenance *apiv1.MaintenanceTask   `json:"maintenance"`
				TaskClaims  *apiv1.SessionTaskClaims `json:"taskClaims"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(w.Body.String(), "Unclaimed") || strings.Contains(w.Body.String(), "Approved") {
				t.Fatal(w.Body.String())
			}
			if mode == "header-off" {
				if wire.TaskClaims != nil || lists != 0 || wire.Maintenance == nil || wire.Maintenance.TaskClaim == nil || wire.Maintenance.TaskClaim.UID != first.UID {
					t.Fatal("default detail joined or dropped the stored association", w.Body.String(), lists)
				}
				return
			}
			if wire.TaskClaims == nil {
				t.Fatal("negotiated detail omitted task claims", w.Body.String())
			}
			binding := wire.TaskClaims.ClaimBinding
			switch mode {
			case "exact":
				if lists != 1 || binding == nil || binding.State != "Observed" || binding.Reason != "Exact" || binding.UID != second.UID || binding.Name != second.Name || binding.Project != "sample" || len(wire.TaskClaims.Items) != 2 || !reflect.DeepEqual(wire.TaskClaims.Items, baseline.Items) {
					t.Fatal(binding, len(wire.TaskClaims.Items), lists)
				}
			case "absent", "two-claims-no-ref":
				wantItems := 1
				if mode == "two-claims-no-ref" {
					wantItems = 2
				}
				if lists != 1 || binding != nil || len(wire.TaskClaims.Items) != wantItems || !reflect.DeepEqual(wire.TaskClaims, baseline) {
					t.Fatal("absent association inferred a winner", wire.TaskClaims, lists)
				}
			case "stale-uid", "crossed-name-uid":
				if lists != 1 || binding == nil || binding.State != "Unknown" || binding.Reason != "Stale" || binding.UID != "" || wire.TaskClaims.State != "Available" {
					t.Fatal(binding, wire.TaskClaims.State, lists)
				}
			case "project-mismatch":
				if lists != 1 || binding == nil || binding.State != "Unknown" || binding.Reason != "ProjectMismatch" || len(wire.TaskClaims.Items) != 1 {
					t.Fatal(binding, lists)
				}
			case "owner-mismatch":
				if lists != 1 || binding == nil || binding.State != "Unknown" || binding.Reason != "OwnerMismatch" || len(wire.TaskClaims.Items) != 1 || wire.TaskClaims.Items[0].Owner.Name != "other-owner" {
					t.Fatal(binding, wire.TaskClaims.Items, lists)
				}
			case "other-owner":
				if lists != 1 || binding == nil || binding.State != "Unknown" || binding.Reason != "NotObserved" || len(wire.TaskClaims.Items) != 1 || wire.TaskClaims.Items[0].UID != first.UID {
					t.Fatal(binding, wire.TaskClaims.Items, lists)
				}
			case "unreadable":
				if lists != 1 || binding == nil || binding.State != "Unknown" || binding.Reason != "Unavailable" || wire.TaskClaims.State != "Unknown" || len(wire.TaskClaims.Items) != 0 {
					t.Fatal(binding, wire.TaskClaims, lists)
				}
			case "unconfigured":
				if lists != 0 || binding == nil || binding.State != "Unknown" || binding.Reason != "Unconfigured" || len(wire.TaskClaims.Items) != 0 {
					t.Fatal(binding, lists)
				}
			case "ambiguous":
				if lists != 1 || binding == nil || binding.State != "Unknown" || binding.Reason != "Ambiguous" || binding.Name != "" || len(wire.TaskClaims.Items) != 2 {
					t.Fatal(binding, len(wire.TaskClaims.Items), lists)
				}
			case "invalid":
				if lists != 1 || binding == nil || binding.State != "Unknown" || binding.Reason != "Invalid" || binding.UID != "" || len(wire.TaskClaims.Items) != 1 || !reflect.DeepEqual(wire.TaskClaims.Items, baseline.Items) {
					t.Fatal(binding, lists)
				}
			case "scope-unconfirmed":
				if lists != 0 || binding == nil || binding.State != "Unknown" || binding.Reason != "ScopeUnconfirmed" || wire.TaskClaims.Reason != "ScopeUnconfirmed" || len(wire.TaskClaims.Items) != 0 {
					t.Fatal(binding, wire.TaskClaims, lists)
				}
			case "identity-unconfirmed":
				if lists != 0 || binding == nil || binding.State != "Unknown" || binding.Reason != "IdentityUnconfirmed" || len(wire.TaskClaims.Items) != 0 {
					t.Fatal(binding, lists)
				}
			default:
				t.Fatal(mode)
			}
		})
	}
}

func createBindingClaim(t *testing.T, f *fixture, token, key string) apiv1.TaskClaim {
	t.Helper()
	created := f.do(http.MethodPost, apiv1.TaskClaimsPath, token, claimRequest(key))
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body.String())
	}
	return decode[apiv1.TaskClaimResult](t, created).Claim
}

func canonicalBindingName(left, right byte) string {
	return "task-claim." + strings.Repeat(string(left), 32) + "." + strings.Repeat(string(right), 32)
}
