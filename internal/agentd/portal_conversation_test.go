package agentd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// History registration binds current identities without changing legacy resume.
func TestPortalConversationClaudeLaunchBinding(t *testing.T) {
	for _, mode := range []string{protocol.ModeLocal, protocol.ModeRemote} {
		t.Run(mode, func(t *testing.T) {
			settings, session, workspace := launchFixture(t)
			session.Mode = mode
			session.Prompt = ""
			session.Limits = nil
			session.SessionUID = "fixture-session-uid"
			settings.PodUID = "fixture-pod-uid"
			first, err := BuildLaunch(settings, session, workspace, "fixture-boot", time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal("fixture launch failed")
			}
			if first.Session != session.Name || first.ConversationID == "" || !first.TUI {
				t.Fatal("fixture did not produce an interactive registered launch")
			}
			if first.SessionUID != session.SessionUID || first.PodUID != settings.PodUID || first.Provider != protocol.AgentClaude {
				t.Fatal("initial registration lost identity")
			}
			settings.PodUID = "fixture-replacement-pod"
			resume, err := BuildResume(settings, session, workspace, first, "fixture-resume", time.Date(2026, 10, 27, 13, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal("fixture resume failed")
			}
			if resume.ConversationID != first.ConversationID || !resume.TUI || !resume.Resume {
				t.Fatal("fixture did not resume the registered conversation")
			}
			if resume.SessionUID != session.SessionUID || resume.PodUID != settings.PodUID || resume.Provider != protocol.AgentClaude {
				t.Fatal("resume failed to preserve owner and bind replacement pod")
			}
			legacy := first
			legacy.SessionUID = ""
			legacy.PodUID = ""
			legacy.Provider = ""
			resumedLegacy, err := BuildResume(settings, session, workspace, legacy, "legacy-resume", time.Date(2026, 10, 27, 13, 0, 0, 0, time.UTC))
			if err != nil || !resumedLegacy.Resume || resumedLegacy.SessionUID != "" || resumedLegacy.PodUID != "" {
				t.Fatal("legacy resume changed or was retrobound")
			}
			replacement := session
			replacement.SessionUID = "replacement-session-uid"
			resumedReplacement, err := BuildResume(settings, replacement, workspace, first, "replacement-resume", time.Date(2026, 10, 27, 13, 0, 0, 0, time.UTC))
			if err != nil || !resumedReplacement.Resume || resumedReplacement.SessionUID != "" || resumedReplacement.PodUID != "" {
				t.Fatal("same-name replacement retrobound retained history")
			}

		})
	}
}

func TestPortalConversationCodexResumeBinding(t *testing.T) {
	settings, session, workspace, now := managedCodexFixture(t)
	settings.PodUID = "fixture-pod-uid"
	first, err := BuildLaunch(settings, session, workspace, "fixture-boot", now)
	if err != nil {
		t.Fatal("fixture launch failed")
	}
	if first.SessionUID != session.SessionUID || first.Provider != protocol.AgentCodex || first.PodUID != settings.PodUID {
		t.Fatal("initial native launch not bound")
	}
	first.ConversationID = "12345678-1234-1234-1234-123456789abc"
	first.NativeThreadConfirmed = true
	settings.PodUID = "fixture-replacement-pod"
	resume, err := BuildResume(settings, session, workspace, first, "fixture-resume", now)
	if err != nil {
		t.Fatal("fixture resume failed")
	}
	if resume.SessionUID != session.SessionUID || resume.Provider != protocol.AgentCodex || resume.PodUID != settings.PodUID || !resume.NativeThreadConfirmed {
		t.Fatal("native resume lost owner or replacement pod")
	}
}

func TestPortalConversationPureProjectionSafety(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	id := "12345678-1234-1234-1234-123456789abc"
	claude := strings.Join([]string{
		`{"type":"user","sessionId":"` + id + `","uuid":"user-1","timestamp":"2026-10-27T11:00:00Z","message":{"role":"user","content":"hello password=SECRET-CANARY"}}`,
		`{"type":"assistant","sessionId":"` + id + `","uuid":"assistant-1","timestamp":"2026-10-27T11:01:00Z","message":{"role":"assistant","content":[{"type":"thinking","text":"SECRET-THINKING"},{"type":"tool_use","text":"SECRET-TOOL"},{"type":"text","text":"visible answer"}]}}`,
	}, "\n")
	got := projectPortalConversationBefore(protocol.AgentClaude, id, []byte(claude), now, 0)
	if !got.Valid() || got.State != "partial" || len(got.Turns) != 2 || got.Turns[0].Role != "user" || got.Turns[1].Role != "assistant" || !strings.Contains(got.Turns[0].Text, "[redacted]") || strings.Contains(fmt.Sprint(got), "SECRET-") {
		t.Fatal("Claude projection leaked private blocks or lost visible turns")
	}
	codex := `{"type":"response_item","timestamp":"2026-10-27T11:00:00Z","payload":{"type":"message","id":"item-1","role":"assistant","channel":"final","content":[{"type":"output_text","text":"visible answer"},{"type":"reasoning_text","text":"SECRET-THINKING"}]}}`
	got = projectPortalConversationBefore(protocol.AgentCodex, id, []byte(codex), now, 0)
	if !got.Valid() || got.State != "partial" || len(got.Turns) != 1 || got.Turns[0].Text != "visible answer" {
		t.Fatal("Codex projection failed allowlisted synthetic item")
	}
	repeated := claude + "\n" + strings.Split(claude, "\n")[0]
	got = projectPortalConversationBefore(protocol.AgentClaude, id, []byte(repeated), now, 0)
	if got.State == "unavailable" || len(got.Turns) != 2 {
		t.Fatal("exact repeated record duplicated or refused")
	}
	conflict := claude + "\n" + strings.Replace(strings.Split(claude, "\n")[0], "hello", "changed", 1)
	got = projectPortalConversationBefore(protocol.AgentClaude, id, []byte(conflict), now, 0)
	if got.State != "unavailable" || len(got.Turns) != 0 {
		t.Fatal("conflicting identity emitted a partial result")
	}
}

func TestPortalConversationProjectionBoundsAndUnknowns(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	id := "12345678-1234-1234-1234-123456789abc"
	var lines []string
	for i := 0; i < 61; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"user","sessionId":%q,"uuid":%q,"timestamp":"2026-10-27T11:00:00Z","message":{"role":"user","content":"visible"}}`, id, fmt.Sprint(i)))
	}
	got := projectPortalConversationBefore(protocol.AgentClaude, id, []byte(strings.Join(lines, "\n")), now, 0)
	if !got.Valid() || got.State != "truncated" || len(got.Turns) != 60 || got.OldestSeq != 2 || got.NewestSeq != 61 {
		t.Fatal("projection did not retain newest bounded turns")
	}
	got = projectPortalConversationBefore(protocol.AgentClaude, id, []byte(`{"type":"user"`), now, 0)
	if got.State != "partial" || len(got.Turns) != 0 {
		t.Fatal("malformed input claimed complete")
	}
	got = projectPortalConversationBefore("other", id, []byte("{}"), now, 0)
	if got.State != "unavailable" {
		t.Fatal("unsupported provider claimed availability")
	}
	duplicate := `{"type":"user","type":"assistant","sessionId":"` + id + `","uuid":"x","timestamp":"2026-10-27T11:00:00Z","message":{"role":"assistant","content":"SECRET-DUPLICATE"}}`
	got = projectPortalConversationBefore(protocol.AgentClaude, id, []byte(duplicate), now, 0)
	if got.State != "partial" || len(got.Turns) != 0 {
		t.Fatal("duplicate JSON field exposed a turn")
	}
	secret := `{"type":"user","sessionId":"` + id + `","uuid":"x","timestamp":"2026-10-27T11:00:00Z","message":{"role":"user","content":"password=\"PRIVATE SECRET VALUE\""}}`
	got = projectPortalConversationBefore(protocol.AgentClaude, id, []byte(secret), now, 0)
	if len(got.Turns) != 1 || strings.Contains(got.Turns[0].Text, "PRIVATE") || strings.Contains(got.Turns[0].Text, "VALUE") {
		t.Fatal("quoted secret was not redacted")
	}
}

func TestPortalConversationIdentityFenceBeforeAndAfterRead(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-session", "wrong-pod", "wrong-project", "missing-first-binding", "replacement-during-read"} {
		t.Run(mode, func(t *testing.T) {
			s, session, ws := launchFixture(t)
			s.PodUID = "fixture-pod-uid"
			session.SessionUID = "fixture-session-uid"
			session.Mode = protocol.ModeLocal
			session.Repo = "dev-env"
			if mode == "wrong-project" {
				session.Repo = "other"
			}
			id := "12345678-1234-1234-1234-123456789abc"
			launch := Launch{Session: session.Name, SessionUID: session.SessionUID, PodUID: s.PodUID, Provider: protocol.AgentClaude, Dir: ws.Worktree, ConversationID: id, BootID: "fixture-boot", TUI: true}
			if err := os.MkdirAll(s.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			if writeJSONFile(s.statePath(bootFile), bootRecord{BootID: launch.BootID}) != nil {
				t.Fatal("boot fixture")
			}
			if mode == "missing-first-binding" {
				launch.SessionUID = ""
			}
			if writeJSONFile(s.statePath(launchFile), launch) != nil {
				t.Fatal("launch fixture")
			}
			expectedSession, expectedPod := session.SessionUID, s.PodUID
			if mode == "wrong-session" {
				expectedSession = "other-session"
			}
			if mode == "wrong-pod" {
				expectedPod = "other-pod"
			}
			reads := 0
			now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
			got := projectRegisteredPortalConversationPage(s, session, expectedSession, expectedPod, now, 60, "", func(Launch, Launch) ([]byte, error) {
				reads++
				if mode == "replacement-during-read" {
					if writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "other-boot"}) != nil {
						t.Fatal("replacement fixture")
					}
				}
				return []byte(`{"type":"user","sessionId":"` + id + `","uuid":"u1","timestamp":"2026-10-27T11:00:00Z","message":{"role":"user","content":"hello"}}`), nil
			})
			if mode == "valid" {
				if reads != 1 || got.State != "partial" || len(got.Turns) != 1 {
					t.Fatal("bound synthetic source did not project")
				}
			} else if got.State != "unavailable" || len(got.Turns) != 0 || (mode != "replacement-during-read" && reads != 0) {
				t.Fatal("identity refusal leaked data or read an unauthorized source")
			}
		})
	}
}

func TestPortalConversationOwnClaudeLocatorAndUnsafeFiles(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-version", "wrong-session", "wrong-worktree", "symlink", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			s, sess, ws := launchFixture(t)
			s.PodUID = "fixture-pod-uid"
			sess.SessionUID = "fixture-session-uid"
			sess.Repo = "dev-env"
			sess.Mode = protocol.ModeLocal
			sess.Prompt = ""
			sess.Limits = nil
			now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
			l, err := BuildLaunch(s, sess, ws, "fixture-boot", now)
			if err != nil {
				t.Fatal("launch fixture")
			}
			if os.MkdirAll(s.StateDir, 0700) != nil || writeJSONFile(s.statePath(bootFile), bootRecord{BootID: l.BootID}) != nil || writeJSONFile(s.statePath(launchFile), l) != nil {
				t.Fatal("registration fixture")
			}
			dir := filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(l.Dir))
			if os.MkdirAll(dir, 0700) != nil {
				t.Fatal("project fixture")
			}
			id := l.ConversationID
			version := "2.1.296"
			if mode == "wrong-version" {
				version = "unsupported"
			}
			if mode == "wrong-session" {
				id = "12345678-1234-1234-1234-123456789abc"
			}
			cwd := l.Dir
			if mode == "wrong-worktree" {
				cwd = "/unrelated"
			}
			data := fmt.Sprintf(`{"type":"user","version":%q,"sessionId":%q,"cwd":%q,"uuid":"u1","timestamp":"2026-10-27T11:00:00Z","message":{"role":"user","content":"synthetic visible"}}`+"\n", version, id, cwd)
			path := filepath.Join(dir, l.ConversationID+".jsonl")
			if os.WriteFile(path, []byte(data), 0600) != nil {
				t.Fatal("synthetic transcript fixture")
			}
			if mode == "hardlink" {
				if os.Link(path, filepath.Join(dir, "extra")) != nil {
					t.Fatal("hardlink fixture")
				}
			}
			if mode == "symlink" {
				target := filepath.Join(dir, "other")
				if os.Rename(path, target) != nil || os.Symlink(target, path) != nil {
					t.Fatal("symlink fixture")
				}
			}
			got := PortalConversation(s, sess, sess.SessionUID, s.PodUID, 60, "", now)
			if mode == "valid" {
				if got.State != "partial" || len(got.Turns) != 1 || got.Turns[0].Text != "synthetic visible" {
					t.Fatal("registered synthetic conversation unavailable")
				}
			} else if got.State != "unavailable" || len(got.Turns) != 0 {
				t.Fatal("unsafe or unsupported file exposed content")
			}
		})
	}
}

func TestPortalConversationOwnCodexLocator(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-version", "wrong-thread", "wrong-worktree", "ambiguous", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			s, sess, ws := launchFixture(t)
			s.PodUID = "fixture-pod-uid"
			sess.SessionUID = "fixture-session-uid"
			sess.Repo = "dev-env"
			sess.Agent = protocol.AgentCodex
			now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
			id := "12345678-1234-1234-1234-123456789abc"
			launch := Launch{Session: sess.Name, SessionUID: sess.SessionUID, PodUID: s.PodUID, Provider: protocol.AgentCodex, Dir: ws.Worktree, ConversationID: id, BootID: "fixture-boot", TUI: true, NativeThreadConfirmed: true, CreatedAt: now, Env: []string{"CODEX_HOME=" + filepath.Join(s.Home, ".codex")}}
			if os.MkdirAll(s.StateDir, 0700) != nil || writeJSONFile(s.statePath(bootFile), bootRecord{BootID: launch.BootID}) != nil || writeJSONFile(s.statePath(launchFile), launch) != nil {
				t.Fatal("registration fixture")
			}
			dir := filepath.Join(s.Home, ".codex", "sessions", "2026", "10", "27")
			if os.MkdirAll(dir, 0700) != nil {
				t.Fatal("date fixture")
			}
			version := managedCodexVersion
			thread := id
			if mode == "wrong-version" {
				version = "unknown"
			}
			if mode == "wrong-thread" {
				thread = "aaaaaaaa-1234-1234-1234-123456789abc"
			}
			cwd := launch.Dir
			if mode == "wrong-worktree" {
				cwd = "/unrelated"
			}
			data := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cli_version":%q,"cwd":%q}}`+"\n", thread, version, cwd) + `{"type":"response_item","timestamp":"2026-10-27T11:00:00Z","payload":{"type":"message","id":"item-1","role":"assistant","channel":"final","content":[{"type":"output_text","text":"synthetic codex"}]}}` + "\n"
			data += `{"type":"response_item","timestamp":"2026-10-27T11:00:00Z","payload":{"type":"message","id":"item-2","role":"assistant","channel":"analysis","content":[{"type":"output_text","text":"PRIVATE-ANALYSIS"}]}}` + "\n"
			data += `{"type":"response_item","timestamp":"2026-10-27T11:00:00Z","payload":{"type":"message","id":"item-3","role":"assistant","channel":"unknown","content":[{"type":"output_text","text":"PRIVATE-UNKNOWN"}]}}` + "\n"
			data += `{"type":"response_item","timestamp":"2026-10-27T11:00:00Z","payload":{"type":"message","id":"item-4","role":"assistant","content":[{"type":"output_text","text":"PRIVATE-MISSING"}]}}` + "\n"
			path := filepath.Join(dir, "rollout-2026-10-27T12-00-00-"+id+".jsonl")
			if os.WriteFile(path, []byte(data), 0600) != nil {
				t.Fatal("synthetic rollout")
			}
			if mode == "ambiguous" {
				if os.WriteFile(filepath.Join(dir, "rollout-other-"+id+".jsonl"), []byte(data), 0600) != nil {
					t.Fatal("ambiguous fixture")
				}
			}
			if mode == "symlink" {
				target := filepath.Join(dir, "other")
				if os.Rename(path, target) != nil || os.Symlink(target, path) != nil {
					t.Fatal("symlink fixture")
				}
			}
			got := PortalConversation(s, sess, sess.SessionUID, s.PodUID, 60, "", now)
			if mode == "valid" {
				if got.State != "partial" || len(got.Turns) != 1 || got.Turns[0].Text != "synthetic codex" {
					t.Fatal("registered Codex synthetic conversation unavailable")
				}
			} else if got.State != "unavailable" || len(got.Turns) != 0 {
				t.Fatal("unsafe or unsupported Codex file exposed content")
			}
		})
	}
}

func TestPortalConversationCursorBindsSnapshotAndOwner(t *testing.T) {
	s, sess, ws := launchFixture(t)
	s.PodUID = "fixture-pod"
	sess.SessionUID = "fixture-session"
	sess.Repo = "dev-env"
	id := "12345678-1234-1234-1234-123456789abc"
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	l := Launch{Session: sess.Name, SessionUID: sess.SessionUID, PodUID: s.PodUID, Provider: protocol.AgentClaude, Dir: ws.Worktree, ConversationID: id, BootID: "boot", TUI: true}
	if os.MkdirAll(s.StateDir, 0700) != nil || writeJSONFile(s.statePath(bootFile), bootRecord{BootID: l.BootID}) != nil || writeJSONFile(s.statePath(launchFile), l) != nil {
		t.Fatal("fixture registration")
	}
	var lines []string
	for i := 1; i <= 3; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"user","sessionId":%q,"uuid":%q,"timestamp":"2026-10-27T11:00:00Z","message":{"role":"user","content":%q}}`, id, fmt.Sprint(i), fmt.Sprint(i)))
	}
	raw := []byte(strings.Join(lines, "\n"))
	read := func(Launch, Launch) ([]byte, error) { return raw, nil }
	first := projectRegisteredPortalConversationPage(s, sess, sess.SessionUID, s.PodUID, now, 1, "", read)
	if !first.Valid() || len(first.Turns) != 1 || first.Turns[0].Text != "3" || first.NextCursor == "" {
		t.Fatal("first page failed")
	}
	second := projectRegisteredPortalConversationPage(s, sess, sess.SessionUID, s.PodUID, now, 1, first.NextCursor, read)
	if !second.Valid() || len(second.Turns) != 1 || second.Turns[0].Text != "2" {
		t.Fatal("snapshot cursor did not page backward")
	}
	raw = append(raw, []byte("\n")...)
	changed := projectRegisteredPortalConversationPage(s, sess, sess.SessionUID, s.PodUID, now, 1, first.NextCursor, read)
	if changed.State != "unavailable" || len(changed.Turns) != 0 {
		t.Fatal("stale cursor exposed changed snapshot")
	}
}

func TestPortalConversationProviderSecurityAliases(t *testing.T) {
	id := "12345678-1234-1234-1234-123456789abc"
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	base := `{"type":"user","sessionId":"` + id + `","uuid":"u1","timestamp":"2026-10-27T11:00:00Z","message":{"role":"user","content":[{"type":"text","text":"PRIVATE-SENTINEL"}]}}`
	for _, fields := range []string{`"isSidechain":true,"IsSidechain":false,`, `"IsSidechain":false,"isSidechain":true,`, `"isApiErrorMessage":true,"IsApiErrorMessage":false,`, `"IsApiErrorMessage":false,"isApiErrorMessage":true,`} {
		raw := strings.Replace(base, `"type":"user",`, fields+`"type":"user",`, 1)
		got := projectPortalConversationBefore(protocol.AgentClaude, id, []byte(raw), now, 0)
		if len(got.Turns) != 0 {
			t.Fatal("ambiguous exclusion leaked")
		}
	}
	for _, replacement := range []struct{ old, value string }{
		{`"role":"user"`, `"role":"user","Role":"user"`},
		{`"content":`, `"Content":`}, {`"type":"text"`, `"type":"text","Type":"text"`},
		{`"sessionId":`, `"ſessionId":`}, {`"uuid":`, `"UUID":`},
	} {
		raw := strings.Replace(base, replacement.old, replacement.value, 1)
		got := projectPortalConversationBefore(protocol.AgentClaude, id, []byte(raw), now, 0)
		if len(got.Turns) != 0 {
			t.Fatal("ambiguous provider field leaked")
		}
	}
	for _, raw := range []string{`{"type":"session_meta","Type":"session_meta","payload":{"id":"` + id + `","cli_version":"` + managedCodexVersion + `","cwd":"/w"}}`, `{"type":"session_meta","payload":{"id":"` + id + `","Id":"` + id + `","cli_version":"` + managedCodexVersion + `","cwd":"/w"}}`} {
		if portalSourceVersion(protocol.AgentCodex, id, "/w", []byte(raw)) {
			t.Fatal("ambiguous metadata accepted")
		}
	}
}
