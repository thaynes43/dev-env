package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestRemoteTitleArgvAndStatus(t *testing.T) {
	s, _, ws := launchFixture(t)
	s.Getenv = envOf(nil)
	sess := remoteSession()
	sess.Repo = "haynes-ops"

	// These argv are what origin/main at b9e23135 builds for an untitled Claude
	// remote session. The conversation id is minted; everything else is fixed.
	guard := fmt.Sprintf("You are working in an isolated git worktree (%s) on branch %s. "+
		"Stay inside it. NEVER push to main: commit to %s, push that branch, and "+
		"open a PR with 'gh pr create' when the work is ready. If the work needs extra "+
		"git worktrees, create them under %s and 'git worktree remove' each once its PR "+
		"merges; never leave stranded worktrees behind.", ws.Worktree, ws.Branch, ws.Branch, s.WorkDir())

	first, err := BuildLaunch(s, sess, ws, "boot1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	wantLaunch := []string{
		"claude", "--model", "claude-haiku-4-5", "--dangerously-skip-permissions",
		"--session-id", first.ConversationID,
		"--remote-control", "dev-env-1019-120000",
		"--append-system-prompt", guard,
	}
	if !slices.Equal(first.Argv, wantLaunch) {
		t.Fatalf("untitled launch argv\n got %q\nwant %q", first.Argv, wantLaunch)
	}
	resumeA, err := BuildResume(s, sess, ws, first, "boot2", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	wantResume := []string{
		"claude", "--model", "claude-haiku-4-5", "--dangerously-skip-permissions",
		"--resume", first.ConversationID,
		"--remote-control", "dev-env-1019-120000",
		"--append-system-prompt", guard,
	}
	if !slices.Equal(resumeA.Argv, wantResume) {
		t.Fatalf("untitled resume argv\n got %q\nwant %q", resumeA.Argv, wantResume)
	}

	sess.Title = "Main coordinator"
	titled := remoteControlArgs(sess)
	if len(titled) != 2 || titled[1] != "Haiku | Main coordinator" {
		t.Fatalf("titled args %q", titled)
	}
	launch, err := BuildLaunch(s, sess, ws, "boot1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	gotName, gotSig := argvSignature(launch.Argv)
	_, wantSig := argvSignature(first.Argv)
	if gotName != "Haiku | Main coordinator" || gotSig != wantSig {
		t.Fatalf("launch name %q changed more than the remote-control argument", gotName)
	}
	resumed, err := BuildResume(s, sess, ws, first, "boot2", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	gotName, gotSig = argvSignature(resumed.Argv)
	_, resumeSig := argvSignature(resumeA.Argv)
	if gotName != "Haiku | Main coordinator" || gotSig != resumeSig {
		t.Fatalf("resume name %q changed more than the remote-control argument", gotName)
	}

	sess.Title = ""
	if !slices.Equal(remoteControlArgs(sess), []string{"--remote-control", "dev-env-1019-120000"}) {
		t.Fatal("clearing the title did not restore the arguments")
	}

	// A title that somehow starts with a hyphen, on a model outside the family
	// table, must not be passed: the CLI would read it as an option.
	unsafe := remoteSession()
	unsafe.Model = "claude-3-5-haiku-20241022"
	unsafe.Title = "-settings"
	if got := remoteControlArgs(unsafe); !slices.Equal(got, []string{"--remote-control", unsafe.Name}) {
		t.Fatalf("hyphen title args %q", got)
	}

	codex := remoteSession()
	codex.Agent = protocol.AgentCodex
	codex.Model = "gpt-6-astra"
	codex.Title = "Main coordinator"
	if got := remoteControlArgs(codex); len(got) != 2 || got[1] != codex.Name {
		t.Fatalf("codex args %q", got)
	}
	rc := collectRemoteControl(s, sess, false, agentPid{}, false)
	if rc.Title != "" || rc.Name != sess.Name {
		t.Fatalf("untitled collectRemoteControl: %+v", rc)
	}
	sess.Title = "Main coordinator"
	rc = collectRemoteControl(s, sess, false, agentPid{}, false)
	if rc.Title != "" || rc.Name != sess.Name {
		t.Fatalf("collectRemoteControl picked up the title: %+v", rc)
	}

	booting := CollectStatus(context.Background(), &fakeRunner{}, s, remoteSession(), rescueNow)
	bare, err := json.Marshal(booting.RemoteControl)
	if err != nil || strings.Contains(string(bare), `"title"`) {
		t.Fatalf("untitled boot status %s %v", bare, err)
	}
	titledSess := remoteSession()
	titledSess.Title = "Main coordinator"
	st := CollectStatus(context.Background(), &fakeRunner{}, s, titledSess, rescueNow)
	if st.RemoteControl == nil || st.RemoteControl.Title != "" || st.RemoteControl.Name != titledSess.Name {
		t.Fatalf("boot status reported a title before launch: %+v", st.RemoteControl)
	}
	writeJSON(t, s.statePath(bootFile), bootRecord{BootID: "boot-ready", Boot: protocol.BootReady})
	waiting := CollectStatus(context.Background(), &fakeRunner{}, s, titledSess, rescueNow)
	if waiting.RemoteControl == nil || waiting.RemoteControl.Title != "" {
		t.Fatalf("login wait reported a title: %+v", waiting.RemoteControl)
	}
	writeJSON(t, s.statePath(launchFile), Launch{
		Session: titledSess.Name, BootID: "boot-ready", TUI: true, CreatedAt: rescueNow, ConversationID: "conv-1",
	})
	ready := CollectStatus(context.Background(), &fakeRunner{}, s, titledSess, rescueNow)
	if ready.RemoteControl == nil || ready.RemoteControl.Title != "Haiku | Main coordinator" || ready.Agent.State != protocol.AgentBusy {
		t.Fatalf("live status %+v agent %s", ready.RemoteControl, ready.Agent.State)
	}
	codexBoot := CollectStatus(context.Background(), &fakeRunner{}, s, codex, rescueNow)
	if codexBoot.RemoteControl != nil && codexBoot.RemoteControl.Title != "" {
		t.Fatalf("codex status title %q", codexBoot.RemoteControl.Title)
	}
}

func TestResumeAfterTitleChangeKeepsResumeID(t *testing.T) {
	s, _, ws := launchFixture(t)
	s.Getenv = envOf(nil)
	sess := remoteSession()
	sess.Repo = "haynes-ops"
	first, err := BuildLaunch(s, sess, ws, "boot1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	sess.Title = "Café"
	resumed, err := BuildResume(s, sess, ws, first, "boot2", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	var resumeID, title string
	for i := 0; i+1 < len(resumed.Argv); i++ {
		switch resumed.Argv[i] {
		case "--resume":
			resumeID = resumed.Argv[i+1]
		case "--remote-control":
			title = resumed.Argv[i+1]
		}
	}
	if resumeID != first.ConversationID || title != "Haiku | Café" {
		t.Fatalf("resume %q title %q argv %q", resumeID, title, resumed.Argv)
	}
}

func argvSignature(argv []string) (name, rest string) {
	cp := append([]string(nil), argv...)
	for i := 0; i+1 < len(cp); i++ {
		switch cp[i] {
		case "--remote-control":
			name = cp[i+1]
			cp[i+1] = "NAME"
		case "--session-id":
			cp[i+1] = "SID"
		}
	}
	return name, strings.Join(cp, "\x00")
}
