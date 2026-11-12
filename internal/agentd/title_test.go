package agentd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestRemoteTitleArgvAndStatus(t *testing.T) {
	s, _, ws := launchFixture(t)
	s.Getenv = envOf(nil)
	sess := remoteSession()
	sess.Repo = "haynes-ops"

	bareArgs := remoteControlArgs(sess)
	if len(bareArgs) != 2 || bareArgs[0] != "--remote-control" || bareArgs[1] != sess.Name {
		t.Fatalf("untitled args %q", bareArgs)
	}
	if again := remoteControlArgs(sess); strings.Join(again, "\x00") != strings.Join(bareArgs, "\x00") {
		t.Fatalf("untitled args changed %q", again)
	}

	first, err := BuildLaunch(s, sess, ws, "boot1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildLaunch(s, sess, ws, "boot1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	nameA, sigA := argvSignature(first.Argv)
	nameB, sigB := argvSignature(second.Argv)
	if nameA != sess.Name || nameB != sess.Name || sigA != sigB {
		t.Fatal("two untitled launches differ aside from the minted session id")
	}
	resumeA, err := BuildResume(s, sess, ws, first, "boot2", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	resumeB, err := BuildResume(s, sess, ws, first, "boot2", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(resumeA.Argv, "\x00") != strings.Join(resumeB.Argv, "\x00") {
		t.Fatalf("untitled resume argv differs\n%q\n%q", resumeA.Argv, resumeB.Argv)
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
	if strings.Join(remoteControlArgs(sess), "\x00") != strings.Join(bareArgs, "\x00") {
		t.Fatal("clearing the title did not restore the arguments")
	}

	codex := remoteSession()
	codex.Agent = protocol.AgentCodex
	codex.Model = "gpt-6-astra"
	codex.Title = "Main coordinator"
	if got := remoteControlArgs(codex); len(got) != 2 || got[1] != codex.Name {
		t.Fatalf("codex args %q", got)
	}
	rc := collectRemoteControl(s, sess, false, agentPid{}, false)
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
	if st.RemoteControl == nil || st.RemoteControl.Title != "Haiku | Main coordinator" || st.RemoteControl.Name != titledSess.Name {
		t.Fatalf("boot status %+v", st.RemoteControl)
	}
	writeJSON(t, s.statePath(bootFile), bootRecord{BootID: "boot-ready", Boot: protocol.BootReady})
	ready := CollectStatus(context.Background(), &fakeRunner{}, s, titledSess, rescueNow)
	if ready.RemoteControl == nil || ready.RemoteControl.Title != "Haiku | Main coordinator" {
		t.Fatalf("ready status %+v", ready.RemoteControl)
	}
	codexBoot := CollectStatus(context.Background(), &fakeRunner{}, s, codex, rescueNow)
	if codexBoot.RemoteControl != nil && codexBoot.RemoteControl.Title != "" {
		t.Fatalf("codex status title %q", codexBoot.RemoteControl.Title)
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
