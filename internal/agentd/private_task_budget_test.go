package agentd

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivateBudgetWatchStopsOnLatchOrUncertainty(t *testing.T) {
	for _, scenario := range []string{"latch", "uncertain", "cancel", "closed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ticks := make(chan time.Time, 1)
			calls, exit := 0, 0
			switch scenario {
			case "cancel":
				cancel()
			case "closed":
				close(ticks)
			default:
				ticks <- time.Time{}
			}
			watchPrivateBudget(ctx, ticks, func(context.Context) (bool, error) {
				calls++
				if scenario == "uncertain" {
					return false, errors.New("unknown")
				}
				return true, nil
			}, func(code int) { exit = code })
			if scenario == "latch" || scenario == "uncertain" {
				if calls != 1 || exit != 124 {
					t.Fatalf("calls=%d exit=%d", calls, exit)
				}
			} else if calls != 0 || exit != 0 {
				t.Fatal("cancel/close changed execution")
			}
		})
	}
}

func TestPrivateBudgetPrebootGateUsesTLSBindingAndRefusesBeforeSideEffects(t *testing.T) {
	for _, scenario := range []string{"ready", "latched", "unavailable", "wrong-binding", "missing-campaign"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC()
			binding := apiv1.TaskBudgetBinding{TaskUID: "campaign", Epoch: 1, Deadline: now.Add(2 * time.Minute), HostID: "root", PodUID: "root-pod"}
			calls := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req apiv1.TaskBudgetRequest
				if r.URL.Path != apiv1.TaskBudgetPath(binding.TaskUID)+"/observe" || r.Header.Get("Authorization") != "Bearer fixture" || json.NewDecoder(r.Body).Decode(&req) != nil || req.Binding != binding {
					t.Error("wrong observation binding")
				}
				if scenario == "unavailable" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				st := apiv1.TaskBudgetStatus{Binding: binding, Observed: true, Admitted: true, Latched: scenario == "latched"}
				if scenario == "wrong-binding" {
					st.Binding.PodUID = "replacement"
				}
				_ = json.NewEncoder(w).Encode(st)
			}))
			defer srv.Close()
			dir := t.TempDir()
			ca, token := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "token")
			writeFile(t, ca, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})))
			writeFile(t, token, "fixture\n")
			b := protocol.TaskBudgetDeadline{Version: 1, TaskUID: binding.TaskUID, Epoch: binding.Epoch, Deadline: now.Add(time.Minute), CampaignDeadline: binding.Deadline, HostID: binding.HostID, RootPodUID: binding.PodUID, SessionUID: "session"}
			if scenario == "missing-campaign" {
				b.CampaignDeadline = time.Time{}
			}
			raw, _ := json.Marshal(b)
			t.Setenv(protocol.TaskBudgetEnv, string(raw))
			d := &Daemon{S: Settings{PodUID: "private-pod", APIURL: srv.URL, APICAFile: ca, APITokenFile: token, StateDir: filepath.Join(dir, "untouched-state")}, Session: protocol.Session{Name: "private", SessionUID: "session"}, Now: func() time.Time { return now }}
			if scenario == "ready" {
				stop, err := d.armPrivateTaskBudgetWatch(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				stop()
			} else if err := d.Run(context.Background()); err == nil {
				t.Fatal("unsafe preboot accepted")
			}
			if _, err := os.Stat(d.S.StateDir); !os.IsNotExist(err) {
				t.Fatal("preboot touched state", err)
			}
			want := 1
			if scenario == "missing-campaign" {
				want = 0
			}
			if calls != want {
				t.Fatalf("calls=%d want=%d", calls, want)
			}
		})
	}
}
