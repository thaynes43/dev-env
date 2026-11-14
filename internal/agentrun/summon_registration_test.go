package agentrun

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func registrationSession() apiv1.Session {
	return apiv1.Session{Name: "esc-registration", UID: exportUIDA, Repo: "dev-env", Agent: "claude", Mode: "remote", Model: "claude-opus-5-5", CreatedAt: testNow.Add(-5 * time.Minute), Phase: "Running", RemoteControl: &apiv1.RemoteControl{State: "starting"}}
}

func TestShowSummonRegistration(t *testing.T) {
	seen, at := testNow.Add(-5*time.Minute), testNow
	link := "https://" + "claude.ai/code/session_" + "registration_fixture"
	registered := &apiv1.RemoteControl{State: "registered", URL: link}
	for _, tc := range []struct {
		name   string
		rc     *apiv1.RemoteControl
		record apiv1.SummonRegistration
		want   string
	}{
		{"registered", registered, apiv1.SummonRegistration{AgentSeenAt: &seen, Result: "registered", At: &at}, "remote control registered; registered at 2026-10-06T21:00:00Z; link in -o json (remoteControl.url)"},
		{"registered before seen", registered, apiv1.SummonRegistration{Result: "registered", At: &at}, "remote control registered; registered at 2026-10-06T21:00:00Z; link in -o json (remoteControl.url)"},
		{"unavailable", &apiv1.RemoteControl{State: "login-required"}, apiv1.SummonRegistration{Result: "unavailable", Reason: "login-required", At: &at}, "remote control login-required; unavailable at 2026-10-06T21:00:00Z (login-required); no link reported in -o json (remoteControl.url)"},
		{"seen not settled", &apiv1.RemoteControl{State: "starting"}, apiv1.SummonRegistration{AgentSeenAt: &seen}, "remote control starting; not yet settled; agent seen at 2026-10-06T20:55:00Z; no link reported in -o json (remoteControl.url)"},
		{"registered now offline", &apiv1.RemoteControl{State: "offline"}, apiv1.SummonRegistration{Result: "registered", At: &at}, "remote control offline; registered at 2026-10-06T21:00:00Z; no link reported in -o json (remoteControl.url)"},
		{"registered after unavailable", registered, apiv1.SummonRegistration{Result: "unavailable", Reason: "offline", At: &at}, "remote control registered; unavailable at 2026-10-06T21:00:00Z (offline); link in -o json (remoteControl.url)"},
		{"no current remote record", nil, apiv1.SummonRegistration{Result: "unavailable", Reason: "not-reported", At: &at}, "remote control not-reported; unavailable at 2026-10-06T21:00:00Z (not-reported); no link reported in -o json (remoteControl.url)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			s := registrationSession()
			s.Caller, s.Lane, s.RemoteControl, s.SummonRegistration = "test-caller", "escalation", tc.rc, &tc.record
			serveSession(h, s)
			h.mustRun(ExitOK, "show", s.Name)
			rows := 0
			for _, line := range strings.Split(h.stdout.String(), "\n") {
				if strings.HasPrefix(line, "registration:") {
					rows++
					if strings.TrimSpace(strings.TrimPrefix(line, "registration:")) != tc.want {
						t.Fatal("registration row differs from the expected current state, settlement and link location")
					}
				}
			}
			if rows != 1 || strings.Contains(h.stdout.String(), link) {
				t.Fatal("show must print one registration row and no Remote Control link")
			}
			for _, flags := range [][]string{{"-o", "json"}, {"--json", "--once"}, {"--captured-usage", "-o", "json"}} {
				h.mustRun(ExitOK, append([]string{"show", s.Name}, flags...)...)
				var got apiv1.Session
				if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got.SummonRegistration, s.SummonRegistration) || !reflect.DeepEqual(got.RemoteControl, s.RemoteControl) {
					t.Fatal("JSON changed the registration or existing Remote Control member")
				}
			}
			checkRegistrationUsageExport(t, h, s)
		})
	}
}

func TestShowWithoutRegistrationGolden(t *testing.T) {
	for _, kind := range []string{"ordinary", "summoned"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			s := registrationSession()
			if kind == "summoned" {
				s.Caller, s.Lane = "test-caller", "escalation"
			}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.URL.Path == apiv1.SessionsPath {
					writeTestJSON(w, http.StatusOK, apiv1.SessionList{Sessions: []apiv1.Session{s}})
				} else {
					writeTestJSON(w, http.StatusOK, s)
				}
			}
			for _, check := range []struct {
				name string
				args []string
			}{
				{"show-text", []string{"show", s.Name}},
				{"show-json", []string{"show", s.Name, "-o", "json"}},
				{"show-once", []string{"show", s.Name, "--json", "--once"}},
				{"list-text", []string{"list"}},
				{"list-json", []string{"list", "-o", "json"}},
			} {
				h.mustRun(ExitOK, check.args...)
				if h.stdout.String() != registrationCLIGoldens[kind+"/"+check.name] {
					t.Fatalf("%s output changed without a registration record", check.name)
				}
			}
			checkRegistrationUsageExport(t, h, s)
		})
	}
}

func checkRegistrationUsageExport(t *testing.T, h *harness, s apiv1.Session) {
	t.Helper()
	manifest := exportManifest(t, h, usageExportSelection{Name: s.Name, ExpectedUID: s.UID})
	out := filepath.Join(h.dir, "registration-export")
	h.mustRun(ExitOK, "usage-export", "--manifest", manifest, "--output-dir", out)
	for _, name := range []string{"report.json", "summary.txt"} {
		raw, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("summonRegistration")) || bytes.Contains(raw, []byte("remoteControl")) {
			t.Fatal("usage export carried registration or Remote Control data")
		}
	}
}

func TestRegistrationOldStrictClient(t *testing.T) {
	// Model the pre-OP797 wire shape. Both old strict readers reject the
	// added member; omitempty preserves acceptance without a record.
	current := reflect.TypeFor[apiv1.Session]()
	var fields []reflect.StructField
	for i := 0; i < current.NumField(); i++ {
		f := current.Field(i)
		if f.Name != "SummonRegistration" {
			fields = append(fields, f)
		}
	}
	legacy := reflect.StructOf(fields)
	s := registrationSession()
	s.Caller, s.Lane = "test-caller", "escalation"
	for _, present := range []bool{false, true} {
		if present {
			s.SummonRegistration = &apiv1.SummonRegistration{AgentSeenAt: &testNow}
		}
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		err = dec.Decode(reflect.New(legacy).Interface())
		if present {
			if strictWireJSON(raw, legacy) || err == nil || !strings.Contains(err.Error(), `unknown field "summonRegistration"`) {
				t.Fatal("old strict client should reject the registration member")
			}
		} else if !strictWireJSON(raw, legacy) || err != nil {
			t.Fatal("old strict client refused a session without the record")
		}
	}
}

// Captured before the change at main 83f8a5bbca438eda74945bb3699fbbe322e7feec.
var registrationCLIGoldens = map[string]string{
	"ordinary/list-json": `{
  "sessions": [
    {
      "uid": "11111111-1111-1111-1111-111111111111",
      "name": "esc-registration",
      "repo": "dev-env",
      "agent": "claude",
      "mode": "remote",
      "model": "claude-opus-5-5",
      "createdAt": "2026-10-06T20:55:00Z",
      "phase": "Running",
      "remoteControl": {
        "state": "starting"
      }
    }
  ]
}
`,
	"ordinary/list-text": `NAME              PHASE    AGENT  NODE  AGE  PARENT  NOTE
esc-registration  Running  -      -     5m   -
`,
	"ordinary/show-json": `{
  "taskClaims": {
    "state": "Unknown",
    "reason": "NotReported",
    "items": []
  },
  "uid": "11111111-1111-1111-1111-111111111111",
  "name": "esc-registration",
  "repo": "dev-env",
  "agent": "claude",
  "mode": "remote",
  "model": "claude-opus-5-5",
  "createdAt": "2026-10-06T20:55:00Z",
  "phase": "Running",
  "remoteControl": {
    "state": "starting"
  }
}
`,
	"ordinary/show-once": `{
  "uid": "11111111-1111-1111-1111-111111111111",
  "name": "esc-registration",
  "repo": "dev-env",
  "agent": "claude",
  "mode": "remote",
  "model": "claude-opus-5-5",
  "createdAt": "2026-10-06T20:55:00Z",
  "phase": "Running",
  "remoteControl": {
    "state": "starting"
  }
}
`,
	"ordinary/show-text": `name:                esc-registration
phase:               Running
repo:                dev-env from origin/main
agent:               claude claude-opus-5-5, no effort level, remote mode
size:                M
parent:              -
created:             2026-10-06T20:55:00Z (5m ago)
remote control:      starting: not registered with the Claude app yet
Task claims:         Unknown (NotReported)
Actual model:        Unknown
Requested model:     claude-opus-5-5
Initial task cost:   Unknown
Input tokens:        Unknown
Output tokens:       Unknown
Cache read tokens:   Unknown
Cache create tokens: Unknown
`,
	"summoned/list-json": `{
  "sessions": [
    {
      "uid": "11111111-1111-1111-1111-111111111111",
      "name": "esc-registration",
      "repo": "dev-env",
      "agent": "claude",
      "mode": "remote",
      "model": "claude-opus-5-5",
      "caller": "test-caller",
      "lane": "escalation",
      "createdAt": "2026-10-06T20:55:00Z",
      "phase": "Running",
      "remoteControl": {
        "state": "starting"
      }
    }
  ]
}
`,
	"summoned/list-text": `NAME              PHASE    AGENT  NODE  AGE  PARENT  NOTE
esc-registration  Running  -      -     5m   -
`,
	"summoned/show-json": `{
  "taskClaims": {
    "state": "Unknown",
    "reason": "NotReported",
    "items": []
  },
  "uid": "11111111-1111-1111-1111-111111111111",
  "name": "esc-registration",
  "repo": "dev-env",
  "agent": "claude",
  "mode": "remote",
  "model": "claude-opus-5-5",
  "caller": "test-caller",
  "lane": "escalation",
  "createdAt": "2026-10-06T20:55:00Z",
  "phase": "Running",
  "remoteControl": {
    "state": "starting"
  }
}
`,
	"summoned/show-once": `{
  "uid": "11111111-1111-1111-1111-111111111111",
  "name": "esc-registration",
  "repo": "dev-env",
  "agent": "claude",
  "mode": "remote",
  "model": "claude-opus-5-5",
  "caller": "test-caller",
  "lane": "escalation",
  "createdAt": "2026-10-06T20:55:00Z",
  "phase": "Running",
  "remoteControl": {
    "state": "starting"
  }
}
`,
	"summoned/show-text": `name:                esc-registration
phase:               Running
repo:                dev-env from origin/main
agent:               claude claude-opus-5-5, no effort level, remote mode
size:                M
parent:              -
created:             2026-10-06T20:55:00Z (5m ago)
remote control:      starting: not registered with the Claude app yet
Task claims:         Unknown (NotReported)
Actual model:        Unknown
Requested model:     claude-opus-5-5
Initial task cost:   Unknown
Input tokens:        Unknown
Output tokens:       Unknown
Cache read tokens:   Unknown
Cache create tokens: Unknown
`,
}
