package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const captureConversation = "11111111-1111-4111-8111-111111111111"

func captureLaunch(t *testing.T) (Settings, Launch) {
	t.Helper()
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	l := Launch{CaptureOutput: true, CaptureModel: "claude-opus-5-5", Provider: protocol.AgentClaude, Session: "capture", SessionUID: "original-session", PodUID: "original-pod", ConversationID: captureConversation, Argv: []string{"fake"}, BootID: "original-boot", CreatedAt: time.Now().UTC()}
	return s, l
}

func TestManagedOutputInitialLaunchOnlyKeepsOriginalIdentity(t *testing.T) {
	for _, provider := range []string{protocol.AgentClaude, protocol.AgentCodex} {
		t.Run(provider, func(t *testing.T) {
			s, sess, ws, now := managedCodexFixture(t)
			s.PodUID = "original-pod"
			sess.CaptureOutput, sess.Agent = true, provider
			if provider == protocol.AgentClaude {
				sess.Model = "claude-opus-5-5"
			}
			first, err := BuildLaunch(s, sess, ws, "first", now)
			if err != nil || !first.CaptureOutput || first.Provider != provider || first.SessionUID != sess.SessionUID || first.PodUID != s.PodUID || first.CaptureModel != sess.Model {
				t.Fatal("initial capture identity", err)
			}
			first.ConversationID, first.NativeThreadConfirmed = captureConversation, true
			resume, err := BuildResume(s, sess, ws, first, "later", now)
			if err != nil || resume.CaptureOutput || !resume.TUI || !resume.Resume {
				t.Fatal("resume captured or replayed initial output", err)
			}
			s.PodUID = ""
			if _, err := BuildLaunch(s, sess, ws, "unbound", now); err == nil {
				t.Fatal("initial capture accepted without original Pod")
			}
		})
	}
}

func TestManagedOutputCommonInputsRemainInitialOnly(t *testing.T) {
	for _, provider := range []string{protocol.AgentClaude, protocol.AgentCodex} {
		t.Run(provider, func(t *testing.T) {
			s, sess, ws, now := managedCodexFixture(t)
			s.PodUID = "original-pod"
			sess.CaptureOutput, sess.Agent = true, provider
			if provider == protocol.AgentClaude {
				sess.Model = "claude-opus-5-5"
			}
			common := withCommonRules(t, &s, &sess)
			if err := prepareProjectTask(&s, sess); err != nil {
				t.Fatal(err)
			}
			first, err := BuildLaunch(s, sess, ws, "first", now)
			if err != nil || first.CommonInputs == nil || first.CommonInputs.CommonRevision != common.CommonRevision || !first.CaptureOutput || first.SessionUID != sess.SessionUID || first.PodUID != s.PodUID || first.CaptureModel != sess.Model {
				t.Fatal("common and capture original identity", err)
			}
			first.ConversationID, first.NativeThreadConfirmed = captureConversation, true
			resume, err := BuildResume(s, sess, ws, first, "later", now)
			if err != nil || resume.CommonInputs == nil || *resume.CommonInputs != *first.CommonInputs || resume.CaptureOutput || resume.Prompt != "" || !resume.TUI || !resume.Resume {
				t.Fatal("resume recaptured or lost common identity", err)
			}
		})
	}
}

func TestManagedOutputUTF8ChunksAndDurableOriginalIdentity(t *testing.T) {
	s, l := captureLaunch(t)
	c, err := beginManagedCapture(l, s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	c.confirmed = true
	text := strings.Repeat("界", (4*apiv1.MaxContextBytes)/3) + "é"
	c.text(text)
	joined := strings.Join(c.chunks, "")
	if len(c.chunks) != 4 || len(joined) > 4*apiv1.MaxContextBytes || !strings.HasPrefix(text, joined) || !c.unknown {
		t.Fatal("byte/chunk bound or truthful overflow")
	}
	for _, chunk := range c.chunks {
		if len(chunk) > apiv1.MaxContextBytes || !utf8.ValidString(chunk) {
			t.Fatal("invalid UTF8 split")
		}
	}
	intent, err := c.retain(l, true)
	if err != nil || intent.Completeness != "Unknown" || len(intent.Chunks) != 4 {
		t.Fatal("retention", intent, err)
	}
	for _, ch := range intent.Chunks {
		if ch.Key != apiv1.ManagedOutputKey(apiv1.ManagedInvocation(l.SessionUID, l.PodUID), ch.Ordinal) {
			t.Fatal("key drift")
		}
		fi, err := os.Stat(filepath.Join(c.dir, ch.Key+".txt"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatal("private chunk mode")
		}
	}
	if _, err := beginManagedCapture(l, s.StateDir); err == nil {
		t.Fatal("existing invocation rearmed provider")
	}
}

func TestManagedOutputForeignAndMissingFilesPreserved(t *testing.T) {
	for _, change := range []string{"invocation-missing", "invocation-mode", "invocation-symlink", "chunk-foreign"} {
		t.Run(change, func(t *testing.T) {
			s, l := captureLaunch(t)
			c, err := beginManagedCapture(l, s.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			c.confirmed = true
			c.text("accepted")
			p := filepath.Join(c.dir, "invocation.json")
			switch change {
			case "invocation-missing":
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			case "invocation-mode":
				if err := os.Chmod(p, 0o644); err != nil {
					t.Fatal(err)
				}
			case "invocation-symlink":
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), p); err != nil {
					t.Fatal(err)
				}
			case "chunk-foreign":
				p = filepath.Join(c.dir, apiv1.ManagedOutputKey(c.invocation.Invocation, 0)+".txt")
				if err := os.WriteFile(p, []byte("foreign"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.retain(l, true); err == nil {
				t.Fatal("ambiguous private state accepted")
			}
			if change == "chunk-foreign" {
				b, err := os.ReadFile(p)
				if err != nil || string(b) != "foreign" {
					t.Fatal("foreign file overwritten")
				}
			}
			if _, err := beginManagedCapture(l, s.StateDir); err == nil {
				t.Fatal("ambiguous state reconstructed")
			}
		})
	}
}

func TestManagedOutputUnknownACKHasOneAttemptAndOriginalBytes(t *testing.T) {
	s, l := captureLaunch(t)
	c, err := beginManagedCapture(l, s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	c.confirmed = true
	c.text("  exact\noutput  ")
	intent, err := c.retain(l, true)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req apiv1.PublishContextRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Text != "  exact\noutput  " || req.Key != intent.Chunks[0].Key || req.ManagedOutput == nil {
			t.Error("original intent lost")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &HeartbeatClient{URL: srv.URL, Session: l.Session, TokenFile: token, HTTP: srv.Client()}
	if c.publish(context.Background(), l, intent, h) == nil || calls != 1 {
		t.Fatal("uncertain ACK not retained")
	}
	if c.publish(context.Background(), l, intent, h) == nil || calls != 1 {
		t.Fatal("automatic second attempt")
	}
	b, err := ownedCaptureRead(c.dir, intent.Chunks[0].Key+".txt", apiv1.MaxContextBytes)
	if err != nil || string(b) != "  exact\noutput  " {
		t.Fatal("original bytes lost")
	}
}

// A permanent limit refusal is reported as such; the one attempt and the
// original bytes stay as for any other refusal (#223 review).
func TestManagedOutputLimitRefusalIsReportedOnce(t *testing.T) {
	s, l := captureLaunch(t)
	c, err := beginManagedCapture(l, s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	c.confirmed = true
	c.text("limited output")
	intent, err := c.retain(l, true)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(apiv1.Error{Code: apiv1.CodeLimitExceeded, Message: "limit"})
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &HeartbeatClient{URL: srv.URL, Session: l.Session, TokenFile: token, HTTP: srv.Client()}
	if err := c.publish(context.Background(), l, intent, h); !errors.Is(err, ErrContextLimit) || calls != 1 {
		t.Fatal("limit refusal not reported", err, calls)
	}
	if c.publish(context.Background(), l, intent, h) == nil || calls != 1 {
		t.Fatal("automatic second attempt after a limit refusal")
	}
	if b, err := ownedCaptureRead(c.dir, intent.Chunks[0].Key+".txt", apiv1.MaxContextBytes); err != nil || string(b) != "limited output" {
		t.Fatal("original bytes lost")
	}
}

func TestManagedOutputExactACKIsPrivateAndCannotRearmDelivery(t *testing.T) {
	s, l := captureLaunch(t)
	c, err := beginManagedCapture(l, s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	c.confirmed = true
	c.text("accepted stdout")
	intent, err := c.retain(l, true)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, m := contextClientFixture(t)
	ch := intent.Chunks[0]
	m.Source.Name, m.Source.UID, m.Actor.PodUID = l.Session, l.SessionUID, l.PodUID
	m.Kind, m.Key = "transcript-excerpt", ch.Key
	m.Name = apiv1.ContextName(l.SessionUID, m.Kind, m.Key)
	m.Attribution.Provider, m.Attribution.RequestedModel = l.Provider, l.CaptureModel
	m.ContentSHA256, m.ByteCount = ch.SHA256, int32(ch.Bytes)
	m.Origin, m.Completeness = "ManagedOutputAssertion", intent.Completeness
	m.ManagedOutput = &apiv1.ManagedOutputAssertion{Invocation: intent.Invocation, Ordinal: ch.Ordinal, ConversationID: intent.ConversationID}
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(apiv1.PublishContextResult{Outcome: "Created", Metadata: m})
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &HeartbeatClient{URL: srv.URL, Session: l.Session, TokenFile: token, HTTP: srv.Client()}
	if err := c.publish(context.Background(), l, intent, h); err != nil || calls != 1 {
		t.Fatal("original ACK", err, calls)
	}
	b, err := ownedCaptureRead(c.dir, ch.Key+"-ack.json", 8192)
	if err != nil || bytes.Contains(b, []byte("accepted stdout")) || !bytes.Contains(b, []byte(m.UID)) {
		t.Fatal("private metadata receipt", err)
	}
	if c.publish(context.Background(), l, intent, h) == nil || calls != 1 {
		t.Fatal("confirmed intent automatically repeated")
	}
}

func TestManagedOutputRunAgentIgnoresStderrAssistantEvents(t *testing.T) {
	s, l := captureLaunch(t)
	if err := os.MkdirAll(s.WorkDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	cli := fakeCLI(t, t.TempDir(), `cat <<'EOF'
{"type":"system","subtype":"init","session_id":"`+captureConversation+`"}
{"type":"assistant","message":{"content":[{"type":"text","text":"  trusted stdout  "},{"type":"tool_use","name":"Bash","input":{"command":"not-captured"}}]}}
{"type":"result","subtype":"success","num_turns":1}
EOF
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"STDERR_FAKE"}]}}' >&2`)
	l.Argv = []string{cli}
	l.Dir = filepath.Dir(cli)
	l.LogPath = s.LogPath(l.Session)
	l.EventsPath = s.statePath(eventsFile)
	var out bytes.Buffer
	if code := RunAgent(writeLaunch(t, s, l), &out, make(chan os.Signal), time.Second); code != 0 {
		t.Fatal("fake process", code, out.String())
	}
	dir := filepath.Join(s.StateDir, "managed-output", apiv1.ManagedInvocation(l.SessionUID, l.PodUID))
	b, err := ownedCaptureRead(dir, apiv1.ManagedOutputKey(apiv1.ManagedInvocation(l.SessionUID, l.PodUID), 0)+".txt", apiv1.MaxContextBytes)
	if err != nil || string(b) != "  trusted stdout  " {
		t.Fatal("stdout capture", string(b), err)
	}
	events, err := os.ReadFile(l.EventsPath)
	if err != nil || strings.Contains(string(events), "STDERR_FAKE") || !strings.Contains(out.String(), "STDERR_FAKE") {
		t.Fatal("stderr lost or parsed")
	}
}

func TestManagedOutputCodexRequiresConfirmedThread(t *testing.T) {
	s, l := captureLaunch(t)
	l.Provider = protocol.AgentCodex
	l.CaptureModel = "gpt-6.1-sol"
	l.ConversationID = ""
	p := writeLaunch(t, s, l)
	c, err := beginManagedCapture(l, s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	stream := codexStream{capture: c}
	var result streamResult
	if stream.line([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"premature"}}`), p, &l, io.Discard, &result) == nil || len(c.chunks) != 0 {
		t.Fatal("unconfirmed output captured")
	}
	for _, line := range []string{`{"type":"thread.started","thread_id":"` + captureConversation + `"}`, `{"type":"turn.started"}`, `{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"not captured"}}`, `{"type":"item.completed","item":{"type":"agent_message","text":"codex stdout"}}`} {
		if err := stream.line([]byte(line), p, &l, io.Discard, &result); err != nil {
			t.Fatal(err)
		}
	}
	if !c.confirmed || strings.Join(c.chunks, "") != "codex stdout" || l.ConversationID != captureConversation {
		t.Fatal("native confirmed output")
	}
	intent, err := c.retain(l, false)
	if err != nil || intent.Completeness != "Unknown" {
		t.Fatal("protocol gap claimed complete", err)
	}
}

func TestManagedOutputCodexInvalidUTF8PreservesConfirmedPrefixAsUnknown(t *testing.T) {
	s, l := captureLaunch(t)
	l.Provider, l.CaptureModel, l.ConversationID = protocol.AgentCodex, "gpt-6.1-sol", ""
	p := writeLaunch(t, s, l)
	c, err := beginManagedCapture(l, s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	stream := codexStream{capture: c}
	var result streamResult
	for _, line := range []string{`{"type":"thread.started","thread_id":"` + captureConversation + `"}`, `{"type":"turn.started"}`, `{"type":"item.completed","item":{"type":"agent_message","text":"  accepted 界\n  "}}`} {
		if err := stream.line([]byte(line), p, &l, io.Discard, &result); err != nil {
			t.Fatal(err)
		}
	}
	invalid := append([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"`), 0xff)
	invalid = append(invalid, []byte(`"}}`)...)
	if utf8.Valid(invalid) || stream.line(invalid, p, &l, io.Discard, &result) == nil {
		t.Fatal("invalid raw UTF-8 protocol accepted")
	}
	if !c.confirmed || !c.unknown || strings.Join(c.chunks, "") != "  accepted 界\n  " {
		t.Fatal("original output lost or invalid text substituted")
	}
	intent, err := c.retain(l, true)
	if err != nil || intent.Completeness != "Unknown" || len(intent.Chunks) != 1 {
		t.Fatal("parsing gap retained as a confirmed partial capture", err)
	}
	b, err := ownedCaptureRead(c.dir, intent.Chunks[0].Key+".txt", apiv1.MaxContextBytes)
	if err != nil || string(b) != "  accepted 界\n  " || !utf8.Valid(b) {
		t.Fatal("private confirmed prefix changed", err)
	}
}

// history-02: a gap ends Claude capture. Text after it is never spliced onto
// the prefix accepted before it.
func TestManagedOutputClaudeGapFreezesAcceptedPrefix(t *testing.T) {
	assistant := func(text string) []byte {
		return []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`)
	}
	invalidUTF8 := append([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}]}}`)...)
	for name, gap := range map[string][]byte{
		"invalid UTF-8":  invalidUTF8,
		"malformed JSON": []byte(`{"type":"assistant",`),
		"foreign init":   []byte(`{"type":"system","subtype":"init","session_id":"22222222-2222-4222-8222-222222222222"}`),
		"nil message":    []byte(`{"type":"assistant"}`),
	} {
		t.Run(name, func(t *testing.T) {
			s, l := captureLaunch(t)
			c, err := beginManagedCapture(l, s.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range [][]byte{
				[]byte(`{"type":"system","subtype":"init","session_id":"` + captureConversation + `"}`),
				assistant("Rule A applies. "),
				gap,
				assistant("Rule B does not apply."),
			} {
				c.claude(line)
			}
			if got := strings.Join(c.chunks, ""); got != "Rule A applies. " || !c.unknown {
				t.Fatalf("capture after a gap = %q, unknown %v; want only the accepted prefix, Unknown", got, c.unknown)
			}
			intent, err := c.retain(l, true)
			if err != nil || intent.Completeness != "Unknown" || len(intent.Chunks) != 1 {
				t.Fatal("gap retained as a confirmed capture", err)
			}
		})
	}
}

// history-02: an overflow that drops a multibyte rune is a gap too. A later
// short ASCII text must not fill the bytes left in the last chunk.
func TestManagedOutputOverflowGapFreezesCapture(t *testing.T) {
	s, l := captureLaunch(t)
	c, err := beginManagedCapture(l, s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	c.confirmed = true
	c.text(strings.Repeat("a", 4*apiv1.MaxContextBytes-2))
	if c.unknown || len(c.chunks) != 4 {
		t.Fatal("fill below the bound must stay known")
	}
	c.text("界")
	if !c.unknown {
		t.Fatal("dropped rune not marked Unknown")
	}
	before := strings.Join(c.chunks, "")
	c.text("OK")
	if got := strings.Join(c.chunks, ""); got != before {
		t.Fatalf("text after the overflow gap appended %q", strings.TrimPrefix(got, before))
	}
}
