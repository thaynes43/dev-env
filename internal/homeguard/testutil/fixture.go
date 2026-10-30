// Package testutil shares only non-secret, offline OP286 fixture witnesses.
// Nothing in this package is imported by production code or contacts a service.
package testutil

import (
	"embed"
	"encoding/json"
	"testing"

	"github.com/thaynes43/dev-env/internal/homeguard"
)

//go:embed testdata/*
var files embed.FS

type Fixture struct {
	Session, PVC, PV                   json.RawMessage
	RemoteImage, UsageImage, TaskImage string
	UsageParent, UsageKey, UsageName   string
	TaskParent, TaskKey, TaskName      string
}

func Read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := files.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func Decode(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func Load(t *testing.T) Fixture {
	t.Helper()
	var f Fixture
	Decode(t, Read(t, "root.json"), &f)
	return f
}

// Binding derives all six fields from the shared objects; no second identity
// tuple can drift between the operator, agent and CLI test stages.
func (f Fixture) Binding(t *testing.T) homeguard.Binding {
	t.Helper()
	var s, c, v struct {
		Metadata struct{ Namespace, Name, UID string }
	}
	Decode(t, f.Session, &s)
	Decode(t, f.PVC, &c)
	Decode(t, f.PV, &v)
	b := homeguard.Binding{Namespace: s.Metadata.Namespace, Session: s.Metadata.Name, SessionUID: s.Metadata.UID, PVCUID: c.Metadata.UID, PV: v.Metadata.Name, PVUID: v.Metadata.UID}
	if _, err := homeguard.Parse(b.String()); err != nil {
		t.Fatal(err)
	}
	return b
}

// PodWitness contains precisely the rendered fields consumed by stage B, plus
// HOME and Session ownership linking the rendered Pod back to the fixture.
type PodWitness struct {
	Namespace, Name, SessionUID, HomeClaim string
	Main, Init                             Container
}

type Container struct {
	Image         string
	Command, Args []string
	Env           map[string]string
	HomeReadOnly  bool
}

type Golden struct {
	Selected, SelectorRemoved PodWitness
}

func LoadGolden(t *testing.T) Golden {
	t.Helper()
	var g Golden
	Decode(t, Read(t, "root-pod.golden.json"), &g)
	return g
}
