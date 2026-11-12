package summonoutcome

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestWriteRejectsPrunedTerminalRecord(t *testing.T) {
	for _, missing := range []string{"closed", "summon"} {
		t.Run(missing, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "rem-pruned", Namespace: "sessions", UID: "pruned-session"}, Spec: v1alpha1.AgentSessionSpec{Caller: "upgrade-agent.alert-responder", Lane: v1alpha1.LaneRemediation}}
			base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(s).WithObjects(s).Build()
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			patches := 0
			c := interceptor.NewClient(base, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				patches++
				options := (&client.SubResourcePatchOptions{}).ApplyOptions(opts)
				response := obj
				if options.SubResourceBody != nil {
					response = options.SubResourceBody
					options.SubResourceBody = nil
				}
				data, err := patch.Data(obj)
				if err != nil {
					return err
				}
				var wire map[string]any
				if err := json.Unmarshal(data, &wire); err != nil {
					return err
				}
				status := wire["status"].(map[string]any)
				if missing == "summon" {
					delete(status, "summon")
				} else {
					delete(status["summon"].(map[string]any), "closed")
				}
				data, err = json.Marshal(wire)
				if err != nil {
					return err
				}
				stored := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: obj.GetName(), Namespace: obj.GetNamespace()}}
				if err := c.SubResource(sub).Patch(ctx, stored, client.RawPatch(types.MergePatchType, data), options); err != nil {
					return err
				}
				// The real client unmarshals the response into the supplied object.
				// If it still holds the submitted status, omitted fields survive.
				data, err = json.Marshal(stored)
				if err != nil {
					return err
				}
				return json.Unmarshal(data, response)
			}})
			stdoutPath := filepath.Join(t.TempDir(), "events")
			stdout, err := os.Create(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Stdout
			os.Stdout = stdout
			t.Cleanup(func() { os.Stdout = previous; _ = stdout.Close() })
			var logs strings.Builder
			ctx := logr.NewContext(context.Background(), funcr.New(func(prefix, args string) { logs.WriteString(prefix + args) }, funcr.Options{}))
			changed, err := Write(ctx, c, s, Decision{State: v1alpha1.OutcomeEscalated, Note: "synthetic private result", Source: Report, Reason: "escalate"}, time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC))
			if changed || err == nil || !strings.Contains(err.Error(), "status.summon.closed") {
				t.Errorf("pruned terminal record accepted: changed=%v err=%v", changed, err)
			}
			if !strings.Contains(logs.String(), "status.summon.closed") || !strings.Contains(logs.String(), s.Name) || strings.Contains(logs.String(), "synthetic private result") {
				t.Errorf("missing safe pruning diagnostic: %q", logs.String())
			}
			data, err := os.ReadFile(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "event=outcome") || strings.Contains(string(data), "event=filing") {
				t.Error("pruned terminal record emitted an outcome or filing event")
			}
			var stored v1alpha1.AgentSession
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &stored); err != nil {
				t.Fatal(err)
			}
			if patches != 1 || stored.Status.Outcome == nil || stored.Status.Summon != nil && stored.Status.Summon.Closed != nil {
				t.Fatalf("pruning fixture did not store the partial outcome: patches=%d status=%+v", patches, stored.Status)
			}
		})
	}
}
