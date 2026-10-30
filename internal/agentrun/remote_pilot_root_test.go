package agentrun

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/homeguard"
	fixture "github.com/thaynes43/dev-env/internal/homeguard/testutil"
)

// Stage C uses real CLI parsing, serialization, TLS transport and response
// decoding. The server simulates the controller stop; no live API/native client
// is contacted. Lifecycle routes carry a name, not a UID precondition: the UID
// assertions below concern fixture resolution/observations, not a wire fence.
func TestRemotePilotRootStageC(t *testing.T) {
	for _, refuseResume := range []bool{false, true} {
		t.Run(map[bool]string{false: "same identity", true: "resume binding changed"}[refuseResume], func(t *testing.T) {
			remotePilotRootLifecycle(t, refuseResume)
		})
	}
}

func remotePilotRootLifecycle(t *testing.T, refuseResume bool) {
	t.Helper()
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
	// The route's name resolves an object on every request. The observation
	// column records that object's UID, including replacement before resume.
	sessions := map[string]*apiv1.Session{s.Name: &s}
	stopping, stopped, observedStop, resumed := false, false, false, false
	podPresent := true
	stopReads := 0
	type target struct{ Method, Path, Name, UID string }
	var targets []target
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if len(body) != 0 || len(r.URL.Query()) != 0 {
			t.Error("unexpected lifecycle payload")
		}
		route := strings.TrimPrefix(r.URL.Path, apiv1.SessionsPath+"/")
		requestedName, _, _ := strings.Cut(route, "/")
		resolved, ok := sessions[requestedName]
		if !ok {
			t.Error("route did not resolve fixture", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		targets = append(targets, target{r.Method, r.URL.Path, resolved.Name, resolved.UID})
		s := resolved
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionSuspendPath(b.Session):
			if stopping || stopped || resumed {
				t.Error("unexpected repeated suspend")
			}
			stopping = true
			s.OperatingMode, s.Phase = "Suspended", "Running"
			writeTestJSON(w, http.StatusAccepted, *s)
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
			writeTestJSON(w, http.StatusOK, *s)
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionResumePath(b.Session):
			if !stopped || !observedStop || podPresent || s.Phase != "Suspended" {
				t.Error("resume preceded observed stop")
				w.WriteHeader(http.StatusConflict)
				return
			}
			if s.UID != b.SessionUID {
				apiError(w, http.StatusConflict, apiv1.CodeConflict, homeguard.ErrUnsafe.Error())
				return
			}
			resumed = true
			s.OperatingMode, s.Phase = "Running", "Pending"
			writeTestJSON(w, http.StatusAccepted, *s)
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
	if refuseResume {
		replacement := s
		replacement.UID = "replacement-session-uid"
		sessions[b.Session] = &replacement
		h.mustRun(ExitFailed, "resume", b.Session, "--wait=1m", "-o", "json")
		contains(t, "stderr", h.stderr.String(), homeguard.ErrUnsafe.Error())
		if resumed || podPresent || h.stdout.Len() != 0 {
			t.Fatal("refused resume changed state or printed success")
		}
	} else {
		h.mustRun(ExitOK, "resume", b.Session, "--wait=1m", "-o", "json")
		if got := decode(); got.Phase != "Running" || got.OperatingMode != "Running" {
			t.Fatal("resume did not finish on same fixture")
		}
	}
	want := []target{
		{http.MethodPost, apiv1.SessionSuspendPath(b.Session), b.Session, b.SessionUID},
		{http.MethodGet, apiv1.SessionPath(b.Session), b.Session, b.SessionUID},
		{http.MethodGet, apiv1.SessionPath(b.Session), b.Session, b.SessionUID},
		{http.MethodPost, apiv1.SessionResumePath(b.Session), b.Session, b.SessionUID},
		{http.MethodGet, apiv1.SessionPath(b.Session), b.Session, b.SessionUID},
	}
	if refuseResume {
		want = want[:4] // refusal must not enter the post-resume polling path
		want[3].UID = "replacement-session-uid"
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
