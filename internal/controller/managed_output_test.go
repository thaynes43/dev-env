package controller

import (
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestManagedOutputTransportUsesOnlyTrustedCanaryBinding(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	doc, err := sessionDocument(f.Session)
	if err != nil {
		t.Fatal(err)
	}
	s, err := protocol.ParseSession([]byte(doc))
	if err != nil || s.CaptureOutput {
		t.Fatal("legacy capture enabled", err)
	}
	f.Record.CaptureOutput = true
	f.Session.Spec.ProjectCanary.CaptureOutput = true
	f.Session.Spec.ProjectCanary.PolicyDigest = f.Record.Digest()
	doc, err = sessionDocument(f.Session)
	if err != nil {
		t.Fatal(err)
	}
	s, err = protocol.ParseSession([]byte(doc))
	if err != nil || !s.CaptureOutput || s.SessionUID != string(f.Session.UID) {
		t.Fatal("original trusted transport", err)
	}
	s.Mode = protocol.ModeLocal
	if s.Validate() == nil {
		t.Fatal("local capture admitted")
	}
	s.Mode = protocol.ModeTask
	s.SessionUID = ""
	if s.Validate() == nil {
		t.Fatal("unbound capture admitted")
	}
	if f.Record.MatchSession(f.Session, testutil.Owner) != nil {
		t.Fatal("capture binding refused")
	}
	f.Session.Spec.ProjectCanary.CaptureOutput = false
	if f.Record.MatchSession(f.Session, testutil.Owner) == nil {
		t.Fatal("capture binding drift accepted")
	}
}

func TestManagedOutputCommonCanaryOwnedFixedFile(t *testing.T) {
	for _, provider := range []string{protocol.AgentClaude, protocol.AgentCodex} {
		t.Run(provider, func(t *testing.T) {
			f := testutil.New(t, provider, "dev/dev-env")
			f.Record.CaptureOutput, f.Session.Spec.ProjectCanary.CaptureOutput = true, true
			accepted := commonCanaryController(t, &f, "ACCEPTED_COMMON")
			pod, err := buildManagedPod(f.Session, f.Templates, "https://operator.example", true, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			container := pod.Spec.Containers[0]
			if envOf(container, protocol.SessionEnv).Value != "" || envOf(container, protocol.SessionFileEnv).Value != protocol.SessionInputPath {
				t.Fatal("common capture lost fixed file transport")
			}
			parsed, err := protocol.ParseSessionFile([]byte(pod.Annotations[protocol.SessionInputAnnotation]))
			if err != nil || !parsed.CaptureOutput || parsed.SessionUID != string(f.Session.UID) || parsed.Workspace != nil {
				t.Fatal("common capture lost original private identity", err)
			}
			common, err := commonrules.Parse(parsed.CommonRules)
			if err != nil || common.CommonRevision != accepted.CommonRevision || common.ProjectDigest != commonrules.Digest(parsed.ProjectSnapshot) {
				t.Fatal("common capture lost source digest", err)
			}
			for _, mount := range container.VolumeMounts {
				if mount.Name == "shared" || mount.MountPath == templates.SharedPath {
					t.Fatal("common capture mounted shared artifacts")
				}
			}
			bound, err := projectcanary.BoundRecord(f.Session)
			if err != nil || !bound.CaptureOutput || bound.CommonRevision != accepted.CommonRevision || bound.Digest() != f.Record.Digest() {
				t.Fatal("common capture policy identity drift", err)
			}
			for _, field := range []string{"capture", "common", "project"} {
				changed := f.Session.DeepCopy()
				switch field {
				case "capture":
					changed.Spec.ProjectCanary.CaptureOutput = false
				case "common":
					changed.Spec.CommonRules.SnapshotDigest = "0000000000000000000000000000000000000000000000000000000000000000"
				case "project":
					delete(changed.Annotations, v1alpha1.AnnotationProjectSnapshot)
				}
				if f.Record.MatchSession(changed, testutil.Owner) == nil {
					t.Fatal("changed common capture identity admitted", field)
				}
			}
		})
	}
}
