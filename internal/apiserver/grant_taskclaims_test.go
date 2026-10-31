package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const grantClaimsTestHeader = "X-Dev-Env-Grant-Task-Claims"

func TestMaintenanceGrantTaskClaims(t *testing.T) {
	for _, mode := range []string{"same UID", "peer caller", "cross-project caller", "unauthorized", "missing requester", "replaced requester", "missing UID", "missing snapshot", "no envelope reference", "non-maintenance", "legacy", "old header", "future header", "replaced requester, cross-project caller", "missing requester, unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			bindCatalogFixture(t, f)
			enableClaims(f)
			token := claimSession(t, f, "maintenance-owner", humanSA)
			created := f.do(http.MethodPost, apiv1.TaskClaimsPath, token, claimRequest("issue:322"))
			if created.Code != http.StatusCreated {
				t.Fatal(created.Code, created.Body.String())
			}
			claim := decode[apiv1.TaskClaimResult](t, created).Claim
			sessionClaims := sessionClaimsRead(t, f, "maintenance-owner", token).TaskClaims
			sess := f.session("maintenance-owner")
			g := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Name: "grant-maint", Namespace: sessionNS}, Spec: v1alpha1.AccessGrantSpec{Type: v1alpha1.GrantMaintenance, Requester: v1alpha1.GrantRequester{Session: sess.Name, SessionUID: sess.UID}, Maintenance: &v1alpha1.MaintenanceTask{Operation: "issue:322", Resource: claim.UID}}}
			reader := token
			header := http.Header{grantClaimsTestHeader: {"v1"}}
			switch mode {
			case "peer caller":
				reader = claimSession(t, f, "maintenance-reader", clientSA)
			case "cross-project caller":
				reader = claimSession(t, f, "maintenance-reader", clientSA)
				bindSecondProjectSnapshot(t, f, "maintenance-reader")
			case "unauthorized":
				reader = tokClient
			case "missing requester":
				reader = claimSession(t, f, "maintenance-reader", clientSA)
				// Remove the fixture finalizer so this is an absent requester,
				// rather than a retained deleting session that still owns claims.
				sess.Finalizers = nil
				if err := f.c.Update(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
			case "replaced requester":
				g.Spec.Requester.SessionUID = "old-uid"
			case "missing UID":
				g.Spec.Requester.SessionUID = ""
			case "missing snapshot":
				sess.Annotations = nil
				if err := f.c.Update(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
			case "non-maintenance":
				g.Spec.Type = v1alpha1.GrantKube
				g.Spec.Maintenance = nil
			case "legacy":
				header = nil
			case "old header":
				header.Set(grantClaimsTestHeader, "v0")
			case "future header":
				header.Set(grantClaimsTestHeader, "v2")
			case "replaced requester, cross-project caller":
				reader = claimSession(t, f, "maintenance-reader", clientSA)
				bindSecondProjectSnapshot(t, f, "maintenance-reader")
				g.Spec.Requester.SessionUID = "old-uid"
			case "missing requester, unauthorized":
				reader = tokClient
				// Remove the fixture finalizer so this is an absent requester,
				// rather than a retained deleting session that still owns claims.
				sess.Finalizers = nil
				if err := f.c.Update(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.c.Create(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			lists := 0
			f.srv.TaskClaims.Live = interceptor.NewClient(f.c, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				lists++
				return c.List(ctx, list, opts...)
			}})
			w := f.doHeader(http.MethodGet, apiv1.GrantPath(g.Name), reader, nil, header)
			if w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			// Raw decode keeps this regression executable against the pre-feature main.
			var wire struct {
				TaskClaims *apiv1.SessionTaskClaims `json:"taskClaims"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "non-maintenance", "legacy", "old header", "future header":
				if wire.TaskClaims != nil || lists != 0 {
					t.Fatal("legacy or non-maintenance response did claim IO", w.Body.String(), lists)
				}
				baseline := f.do(http.MethodGet, apiv1.GrantPath(g.Name), reader, nil)
				if baseline.Body.String() != w.Body.String() {
					t.Fatal("unnegotiated/non-maintenance bytes changed")
				}
				fields := []reflect.StructField{}
				typ := reflect.TypeFor[apiv1.Grant]()
				for i := 0; i < typ.NumField(); i++ {
					field := typ.Field(i)
					if field.Name != "TaskClaims" {
						fields = append(fields, field)
					}
				}
				d := json.NewDecoder(strings.NewReader(w.Body.String()))
				d.DisallowUnknownFields()
				if err := d.Decode(reflect.New(reflect.StructOf(fields)).Interface()); err != nil {
					t.Fatal("strict older client", err)
				}
			case "unauthorized", "missing requester", "replaced requester", "missing UID", "missing snapshot", "cross-project caller", "replaced requester, cross-project caller", "missing requester, unauthorized":
				reason := "IdentityUnconfirmed"
				if mode == "unauthorized" || mode == "missing snapshot" || mode == "cross-project caller" {
					reason = "ScopeUnconfirmed"
				}
				if wire.TaskClaims == nil || wire.TaskClaims.State != "Unknown" || wire.TaskClaims.Reason != reason || len(wire.TaskClaims.Items) != 0 || lists != 0 {
					t.Fatal("unsafe or absent Unknown", w.Body.String(), lists)
				}
			default:
				if !reflect.DeepEqual(wire.TaskClaims, sessionClaims) || lists != 1 || len(wire.TaskClaims.Items) != 1 || wire.TaskClaims.Items[0].UID != claim.UID {
					t.Fatal("session/grant identity drift", w.Body.String(), sessionClaims, lists)
				}
				// Text resembling a task key or UID is not an explicit envelope binding.
				var raw map[string]json.RawMessage
				_ = json.Unmarshal(w.Body.Bytes(), &raw)
				if len(raw["envelopeClaim"]) > 0 {
					t.Fatal("inferred envelope reference")
				}
			}
		})
	}
}

// secondProjectCatalog names project "other" and selects the same alias
// repository the package catalog uses for "sample". A live caller saved
// with it is outside the requester session's project.
const secondProjectCatalog = `{"version":1,"repositories":{"alias":{"github":"thaynes43/actual-source","defaultBranch":"stable"}},"projects":{"other":{"repositories":[{"name":"alias"}],"rules":"OTHER_PROJECT"}}}`

// bindSecondProjectSnapshot saves project "other" on a live session. The
// selected repository stays "alias", matching claimSession.
func bindSecondProjectSnapshot(t *testing.T, f *fixture, name string) {
	t.Helper()
	catalog, err := projectcatalog.Parse([]byte(secondProjectCatalog))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("other", "alias")
	if err != nil {
		t.Fatal(err)
	}
	data, err := snapshot.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	s := f.session(name)
	s.Annotations[v1alpha1.AnnotationProjectSnapshot] = string(data)
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}
