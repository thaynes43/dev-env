package agentrun

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	fixture "github.com/thaynes43/dev-env/internal/homeguard/testutil"
)

// Stage C uses real CLI parsing, serialization, TLS transport and response
// decoding. The server simulates the controller stop; no live API/native client
// is contacted. Lifecycle routes carry a name, not a UID precondition: the UID
// assertions below concern fixture resolution/observations, not a wire fence.
func TestRemotePilotRootStageC(t *testing.T) {
	f := fixture.Load(t)
	b := f.Binding(t)
	var source struct {
		Metadata struct{ Labels map[string]string }
		Spec     struct{ Repo, Base, Parent, Agent, Mode, Model, Effort, Size, Profile string }
	}
	fixture.Decode(t, f.Session, &source)
	s := apiv1.Session{
		UID: b.SessionUID, Name: b.Session, Repo: source.Spec.Repo, Base: source.Spec.Base,
		Parent: source.Spec.Parent, IdempotencyKey: source.Metadata.Labels["dev-env.haynesops.com/idempotency-key"],
		Agent: source.Spec.Agent, Mode: source.Spec.Mode, Model: source.Spec.Model, Effort: source.Spec.Effort,
		Size: source.Spec.Size, Profile: source.Spec.Profile, OperatingMode: "Running", Phase: "Running",
	}
	h := newHarness(t)
	stopping, stopped, observedStop, resumed := false, false, false, false
	podPresent := true
	stopReads := 0
	type target struct{ Method, Path, Name, UID string }
	var targets []target
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if len(body) != 0 || len(r.URL.Query()) != 0 {
			t.Error("unexpected lifecycle payload")
		}
		// Every supported route below resolves the same shared fixture object.
		targets = append(targets, target{r.Method, r.URL.Path, s.Name, s.UID})
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionSuspendPath(b.Session):
			if stopping || stopped || resumed {
				t.Error("unexpected repeated suspend")
			}
			stopping = true
			s.OperatingMode, s.Phase = "Suspended", "Running"
			writeTestJSON(w, http.StatusAccepted, s)
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(b.Session):
			if stopping {
				stopReads++
				if stopReads == 2 {
					// Simulated controller finishes the stop, preserving the UID.
					stopping, stopped = false, true
					s.Phase, podPresent = "Suspended", false
				}
			}
			if resumed {
				s.Phase, s.Node, podPresent = "Running", "offline-worker", true
			}
			writeTestJSON(w, http.StatusOK, s)
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionResumePath(b.Session):
			if !stopped || !observedStop || podPresent || s.Phase != "Suspended" {
				t.Error("resume preceded observed stop")
				w.WriteHeader(http.StatusConflict)
				return
			}
			resumed = true
			s.OperatingMode, s.Phase = "Running", "Pending"
			writeTestJSON(w, http.StatusAccepted, s)
		default:
			t.Error("request escaped Root lifecycle", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
	decode := func() apiv1.Session {
		var got apiv1.Session
		if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Name != b.Session || got.UID != b.SessionUID || got.Repo != source.Spec.Repo || got.Agent != source.Spec.Agent || got.Mode != source.Spec.Mode || got.Parent != source.Spec.Parent || got.IdempotencyKey != s.IdempotencyKey {
			t.Fatal("lifecycle identity changed", got)
		}
		return got
	}
	h.mustRun(ExitOK, "suspend", b.Session, "-o", "json")
	var ack apiv1.SessionList
	if err := json.Unmarshal(h.stdout.Bytes(), &ack); err != nil || len(ack.Sessions) != 1 || ack.Sessions[0].UID != b.SessionUID || ack.Sessions[0].Name != b.Session || ack.Sessions[0].Phase == "Suspended" {
		t.Fatal("suspend ACK confused with stopped evidence", err)
	}
	h.mustRun(ExitOK, "show", b.Session, "--json", "--once")
	if got := decode(); got.Phase == "Suspended" || !podPresent {
		t.Fatal("fake stop completed before simulated transition")
	}
	h.mustRun(ExitOK, "show", b.Session, "--json", "--once")
	if got := decode(); got.Phase != "Suspended" || podPresent {
		t.Fatal("stop not observed before resume")
	}
	observedStop = true
	h.mustRun(ExitOK, "resume", b.Session, "--wait=1m", "-o", "json")
	if got := decode(); got.Phase != "Running" || got.OperatingMode != "Running" {
		t.Fatal("resume did not finish on same fixture")
	}
	want := []target{
		{http.MethodPost, apiv1.SessionSuspendPath(b.Session), b.Session, b.SessionUID},
		{http.MethodGet, apiv1.SessionPath(b.Session), b.Session, b.SessionUID},
		{http.MethodGet, apiv1.SessionPath(b.Session), b.Session, b.SessionUID},
		{http.MethodPost, apiv1.SessionResumePath(b.Session), b.Session, b.SessionUID},
		{http.MethodGet, apiv1.SessionPath(b.Session), b.Session, b.SessionUID},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("lifecycle order/identity: got %+v want %+v", targets, want)
	}
	if len(h.ran) != 0 {
		t.Fatal("CLI lifecycle ran a local/native process", h.ran)
	}
	for _, req := range h.api.requests() {
		if req.auth != "Bearer "+testToken {
			t.Fatal("test transport identity changed")
		}
	}
}
