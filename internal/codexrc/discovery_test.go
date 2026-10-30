package codexrc

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func fixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/responses/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func targetFixture(t *testing.T) Target {
	t.Helper()
	catalog, err := projectcatalog.Parse([]byte(`{"version":1,"repositories":{"example":{"github":"example/example"}},"projects":{"example":{"repositories":[{"name":"example"}],"rules":"fixture rules"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("example", "example")
	if err != nil {
		t.Fatal(err)
	}
	return Target{Snapshot: snapshot, CWD: "/workspace/example"}
}

func hostFixture(t *testing.T, target Target) HostInventory {
	t.Helper()
	id := "provider-host-fixture"
	return HostInventory{
		ProjectKind: "codex", HostID: &id, InstallationID: "installation-fixture",
		SessionName: "managed-session-fixture", CLIVersion: SupportedCLIVersion,
		CatalogRevision: target.Snapshot.CatalogRevision(), Repository: target.Snapshot.Selected(),
		RepositoryRoot: target.CWD,
		Status:         fixture(t, "connected"), ProjectPages: []json.RawMessage{fixture(t, "projects-one")},
		Pairing: PairingEvidence{State: Paired, HostID: id, InstallationID: "installation-fixture", EvidenceSource: "supported-client-fixture"},
	}
}

func secondHost(host HostInventory) HostInventory {
	id := "provider-host-other"
	host.HostID = &id
	host.InstallationID = "installation-other"
	host.Status = bytes.ReplaceAll(host.Status, []byte("installation-fixture"), []byte(host.InstallationID))
	host.Pairing.HostID, host.Pairing.InstallationID = id, host.InstallationID
	return host
}

func TestDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*Target, *[]HostInventory)
		outcome Outcome
		code    string
		action  bool
	}{
		{name: "one paired native project", outcome: Matched},
		{name: "two projects on one host", change: func(_ *Target, h *[]HostInventory) {
			(*h)[0].ProjectPages = []json.RawMessage{fixture(t, "projects-many")}
		}, outcome: Ambiguous, code: "codex_inventory_unavailable"},
		{name: "two suitable hosts", change: func(_ *Target, h *[]HostInventory) { *h = append(*h, secondHost((*h)[0])) }, outcome: Ambiguous, code: "codex_inventory_unavailable"},
		{name: "unknown second candidate cannot hide ambiguity", change: func(_ *Target, h *[]HostInventory) {
			other := secondHost((*h)[0])
			other.Pairing = PairingEvidence{}
			*h = append(*h, other)
		}, outcome: Ambiguous, code: "codex_inventory_unavailable"},
		{name: "no hosts", change: func(_ *Target, h *[]HostInventory) { *h = nil }, outcome: None, code: "codex_inventory_unavailable"},
		{name: "no projects", change: func(_ *Target, h *[]HostInventory) {
			(*h)[0].ProjectPages = []json.RawMessage{fixture(t, "projects-none")}
		}, outcome: None, code: "codex_inventory_unavailable"},
		{name: "root differs", change: func(target *Target, _ *[]HostInventory) { target.CWD = "/workspace/other" }, outcome: None, code: "codex_inventory_unavailable"},
		{name: "prefix is not root", change: func(target *Target, _ *[]HostInventory) { target.CWD += "/child" }, outcome: None, code: "codex_inventory_unavailable"},
		{name: "path on wrong repository", change: func(_ *Target, h *[]HostInventory) { (*h)[0].Repository.GitHub = "example/other" }, outcome: None, code: "codex_inventory_unavailable"},
		{name: "wrong catalog revision", change: func(_ *Target, h *[]HostInventory) { (*h)[0].CatalogRevision = "other" }, outcome: None, code: "codex_inventory_unavailable"},
		{name: "ChatGPT with nonnull host still refused", change: func(_ *Target, h *[]HostInventory) { (*h)[0].ProjectKind = "chatgpt" }, outcome: None, code: "codex_inventory_unavailable"},
		{name: "null host refused", change: func(_ *Target, h *[]HostInventory) { (*h)[0].HostID = nil }, outcome: None, code: "codex_inventory_unavailable"},
		{name: "empty host refused", change: func(_ *Target, h *[]HostInventory) { empty := ""; (*h)[0].HostID = &empty }, outcome: None, code: "codex_inventory_unavailable"},
		{name: "unknown project kind", change: func(_ *Target, h *[]HostInventory) { (*h)[0].ProjectKind = "future" }, outcome: None, code: "codex_inventory_unavailable"},
		{name: "pairing required", change: func(_ *Target, h *[]HostInventory) { (*h)[0].Pairing.State = RequiresPairing }, outcome: PairingRequired, code: "pairing_required", action: true},
		{name: "pairing action has no session mapping", change: func(_ *Target, h *[]HostInventory) {
			(*h)[0].Pairing.State, (*h)[0].SessionName = RequiresPairing, ""
		}, outcome: PairingRequired, code: "pairing_action_unavailable"},
		{name: "pairing action cannot contain shell text", change: func(_ *Target, h *[]HostInventory) {
			(*h)[0].Pairing.State, (*h)[0].SessionName = RequiresPairing, "session; other-command"
		}, outcome: PairingRequired, code: "pairing_action_unavailable"},
		{name: "pairing unknown", change: func(_ *Target, h *[]HostInventory) { (*h)[0].Pairing = PairingEvidence{} }, outcome: PairingUnknown, code: "pairing_unknown"},
		{name: "code issued is not paired", change: func(_ *Target, h *[]HostInventory) { (*h)[0].Pairing.State = "pair-code-issued" }, outcome: PairingUnknown, code: "pairing_unknown"},
		{name: "claimed code is not paired", change: func(_ *Target, h *[]HostInventory) { (*h)[0].Pairing.State = "claimed" }, outcome: PairingUnknown, code: "pairing_unknown"},
		{name: "pairing source missing", change: func(_ *Target, h *[]HostInventory) { (*h)[0].Pairing.EvidenceSource = "" }, outcome: PairingUnknown, code: "pairing_unknown"},
		{name: "pairing belongs to other host", change: func(_ *Target, h *[]HostInventory) { (*h)[0].Pairing.HostID = "other" }, outcome: PairingUnknown, code: "pairing_unknown"},
		{name: "pairing belongs to other installation", change: func(_ *Target, h *[]HostInventory) { (*h)[0].Pairing.InstallationID = "other" }, outcome: PairingUnknown, code: "pairing_unknown"},
		{name: "unsupported CLI version", change: func(_ *Target, h *[]HostInventory) { (*h)[0].CLIVersion = "0.160.2" }, outcome: UnsupportedVersion, code: "protocol_unsupported"},
		{name: "absent CLI version", change: func(_ *Target, h *[]HostInventory) { (*h)[0].CLIVersion = "" }, outcome: UnsupportedVersion, code: "protocol_unsupported"},
		{name: "other installation status", change: func(_ *Target, h *[]HostInventory) { (*h)[0].InstallationID = "other" }, outcome: InvalidInventory, code: "protocol_unsupported"},
		{name: "daemon not connected", change: func(_ *Target, h *[]HostInventory) {
			(*h)[0].Status = bytes.ReplaceAll((*h)[0].Status, []byte(`"connected"`), []byte(`"connecting"`))
		}, outcome: None, code: "codex_inventory_unavailable"},
		{name: "null environment ID", change: func(_ *Target, h *[]HostInventory) {
			(*h)[0].Status = bytes.ReplaceAll((*h)[0].Status, []byte(`"environment-fixture"`), []byte(`null`))
		}, outcome: None, code: "codex_inventory_unavailable"},
		{name: "complete paginated inventory", change: func(_ *Target, h *[]HostInventory) {
			(*h)[0].ProjectPages = []json.RawMessage{fixture(t, "projects-page1"), fixture(t, "projects-page2")}
		}, outcome: Matched},
		{name: "incomplete page cannot prove uniqueness", change: func(_ *Target, h *[]HostInventory) {
			(*h)[0].ProjectPages = []json.RawMessage{fixture(t, "projects-page1")}
		}, outcome: InvalidInventory, code: "protocol_unsupported"},
		{name: "missing project response", change: func(_ *Target, h *[]HostInventory) { (*h)[0].ProjectPages = nil }, outcome: InvalidInventory, code: "protocol_unsupported"},
		{name: "duplicate host observations", change: func(_ *Target, h *[]HostInventory) { *h = append(*h, (*h)[0]) }, outcome: InvalidInventory, code: "protocol_unsupported"},
		{name: "no accepted snapshot", change: func(target *Target, _ *[]HostInventory) { target.Snapshot = projectcatalog.Snapshot{} }, outcome: InvalidTarget, code: "project_unavailable"},
		{name: "non-normalized root", change: func(target *Target, _ *[]HostInventory) { target.CWD += "/../example" }, outcome: InvalidTarget, code: "project_unavailable"},
		{name: "relative root", change: func(target *Target, _ *[]HostInventory) { target.CWD = "workspace/example" }, outcome: InvalidTarget, code: "project_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := targetFixture(t)
			hosts := []HostInventory{hostFixture(t, target)}
			if tc.change != nil {
				tc.change(&target, &hosts)
			}
			before, err := json.Marshal(hosts)
			if err != nil {
				t.Fatal(err)
			}
			result := Discover(target, hosts)
			if result.Outcome != tc.outcome {
				t.Fatalf("outcome = %s, want %s", result.Outcome, tc.outcome)
			}
			wireError := result.RCLaunchError()
			if tc.code == "" {
				want := &Project{Kind: "codex", ProjectID: "native-project-fixture", HostID: "provider-host-fixture", InstallationID: "installation-fixture", CWD: "/workspace/example"}
				if wireError != nil || !reflect.DeepEqual(result.Project, want) {
					t.Fatalf("mapping = %+v, error = %+v", result.Project, wireError)
				}
			} else if wireError == nil || wireError.Code != tc.code || result.Project != nil {
				t.Fatalf("refusal selected a project or wrong error: %+v / %+v", result, wireError)
			}
			if tc.action != (len(result.OwnerAction) > 0) || tc.action && !reflect.DeepEqual(result.OwnerAction, []string{"agent-run", "pair", "managed-session-fixture"}) {
				t.Fatalf("pairing action = %+v", result)
			}
			after, err := json.Marshal(hosts)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("discovery modified caller inventory")
			}
		})
	}
}

func TestUnreadyCandidatesCannotResolveAmbiguity(t *testing.T) {
	for _, state := range []string{"connecting", "errored", "disabled", "null environment"} {
		t.Run(state, func(t *testing.T) {
			target := targetFixture(t)
			ready := hostFixture(t, target)
			unready := secondHost(ready)
			if state == "null environment" {
				unready.Status = bytes.ReplaceAll(unready.Status, []byte(`"environment-fixture"`), []byte(`null`))
			} else {
				unready.Status = bytes.ReplaceAll(unready.Status, []byte(`"connected"`), []byte(`"`+state+`"`))
			}
			// An unready unique candidate must refuse without prompting pairing.
			unready.Pairing.State = RequiresPairing
			nonmatching := unready
			nonmatching.ProjectPages = []json.RawMessage{bytes.ReplaceAll(unready.ProjectPages[0], []byte(target.CWD), []byte("/workspace/other"))}
			for _, tc := range []struct {
				name    string
				hosts   []HostInventory
				outcome Outcome
			}{
				{"unique unready", []HostInventory{unready}, None},
				{"ready then unready", []HostInventory{ready, unready}, Ambiguous},
				{"unready then ready", []HostInventory{unready, ready}, Ambiguous},
				{"ready then nonmatching unready", []HostInventory{ready, nonmatching}, Matched},
				{"nonmatching unready then ready", []HostInventory{nonmatching, ready}, Matched},
			} {
				t.Run(tc.name, func(t *testing.T) {
					result := Discover(target, tc.hosts)
					if result.Outcome != tc.outcome || len(result.OwnerAction) != 0 {
						t.Fatalf("unexpected discovery or pairing action: %+v, want %s", result, tc.outcome)
					}
					if tc.outcome == Matched {
						if result.Project == nil || result.Project.HostID != *ready.HostID {
							t.Fatalf("connected matching host not selected: %+v", result)
						}
					} else if result.Project != nil {
						t.Fatalf("refusal selected a project: %+v", result)
					}
				})
			}
		})
	}
}

func TestMacChatGPTOnlyEvidence(t *testing.T) {
	// This is the retained evidence's count/kind/host shape, not invented native
	// ProjectListResponse fields or a claim to possess the original Mac payload.
	var entries []struct {
		ProjectKind string  `json:"projectKind"`
		HostID      *string `json:"hostId"`
	}
	if err := json.Unmarshal(fixture(t, "mac-chatgpt-only"), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 11 {
		t.Fatal("fixture must retain the reported eleven entries")
	}
	var hosts []HostInventory
	for _, entry := range entries {
		if entry.ProjectKind != "chatgpt" || entry.HostID != nil {
			t.Fatal("fixture does not match retained Mac evidence")
		}
		hosts = append(hosts, HostInventory{ProjectKind: entry.ProjectKind, HostID: entry.HostID})
	}
	result := Discover(targetFixture(t), hosts)
	if result.Outcome != None || result.Project != nil || result.RCLaunchError().Code != "codex_inventory_unavailable" {
		t.Fatalf("ChatGPT inventory produced %+v", result)
	}
}

func TestStrictDiscoveryResponses(t *testing.T) {
	project := fixture(t, "projects-one")
	status := fixture(t, "connected")
	for _, tc := range []struct {
		name   string
		raw    []byte
		status bool
	}{
		{"unknown project field", bytes.Replace(project, []byte(`"id":`), []byte(`"hostId": null, "id":`), 1), false},
		{"case alias", bytes.Replace(project, []byte(`"id":`), []byte(`"ID":`), 1), false},
		{"duplicate field", bytes.Replace(project, []byte(`"id":`), []byte(`"id": "other", "id":`), 1), false},
		{"escaped duplicate", bytes.Replace(project, []byte(`"id":`), []byte(`"\u0069d": "other", "id":`), 1), false},
		{"null project ID", bytes.ReplaceAll(project, []byte(`"native-project-fixture"`), []byte(`null`)), false},
		{"missing required metadata", bytes.ReplaceAll(project, []byte(`"metadata": {},`), nil), false},
		{"null required metadata", bytes.ReplaceAll(project, []byte(`"metadata": {}`), []byte(`"metadata": null`)), false},
		{"null required timestamp", bytes.ReplaceAll(project, []byte(`"createdAt": 1700000000`), []byte(`"createdAt": null`)), false},
		{"timestamp overflow", bytes.ReplaceAll(project, []byte(`1700000000`), []byte(`9223372036854775808`)), false},
		{"fractional timestamp", bytes.ReplaceAll(project, []byte(`1700000000`), []byte(`1.5`)), false},
		{"null data", []byte(`{"data":null}`), false},
		{"missing data", []byte(`{}`), false},
		{"relative native root", bytes.ReplaceAll(project, []byte(`/workspace/example`), []byte(`workspace/example`)), false},
		{"non-normalized native root", bytes.ReplaceAll(project, []byte(`/workspace/example`), []byte(`/workspace/../example`)), false},
		{"trailing document", append(append([]byte(nil), project...), []byte(` {}`)...), false},
		{"oversized response", []byte(strings.Repeat(" ", maxResponseBytes) + string(project)), false},
		{"invalid UTF8", bytes.ReplaceAll(project, []byte(`Synthetic native project`), []byte{0xff}), false},
		{"unpaired high surrogate in project ID", bytes.ReplaceAll(project, []byte(`native-project-fixture`), []byte(`\ud800`)), false},
		{"unpaired low surrogate in project ID", bytes.ReplaceAll(project, []byte(`native-project-fixture`), []byte(`\udc00`)), false},
		{"unpaired surrogate in root", bytes.ReplaceAll(project, []byte(`/workspace/example`), []byte(`/workspace/\ud800`)), false},
		{"unpaired surrogate in status identity", bytes.ReplaceAll(status, []byte(`installation-fixture`), []byte(`\ud800`)), true},
		{"unknown connection enum", bytes.ReplaceAll(status, []byte(`connected`), []byte(`future`)), true},
		{"unknown status field", bytes.Replace(status, []byte(`"status":`), []byte(`"hostId": "other", "status":`), 1), true},
		{"missing installation ID", bytes.ReplaceAll(status, []byte(`"installationId": "installation-fixture",`), nil), true},
		{"null installation ID", bytes.ReplaceAll(status, []byte(`"installation-fixture"`), []byte(`null`)), true},
		{"truncated status", status[:len(status)/2], true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := targetFixture(t)
			host := hostFixture(t, target)
			if tc.status {
				host.Status = tc.raw
			} else {
				host.ProjectPages = []json.RawMessage{tc.raw}
			}
			result := Discover(target, []HostInventory{host})
			if result.Outcome != InvalidInventory || result.Project != nil || result.RCLaunchError().Code != "protocol_unsupported" {
				t.Fatalf("malformed response produced %+v", result)
			}
		})
	}
}

func TestUnicodeIdentityPreservation(t *testing.T) {
	for _, tc := range []struct {
		name, wireID, wantID string
	}{
		{"valid surrogate pair", `\ud83d\ude00`, "😀"},
		{"literal replacement character", "�", "�"},
		{"escaped replacement character", `\ufffd`, "�"},
		{"escaped backslash is literal", `\\ud800`, `\ud800`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := targetFixture(t)
			host := hostFixture(t, target)
			host.ProjectPages[0] = bytes.ReplaceAll(host.ProjectPages[0], []byte(`native-project-fixture`), []byte(tc.wireID))
			result := Discover(target, []HostInventory{host})
			if result.Outcome != Matched || result.Project.ProjectID != tc.wantID {
				t.Fatalf("valid Unicode identity changed: %+v", result)
			}
		})
	}
}

func TestDiscoveryBoundsAndPages(t *testing.T) {
	target := targetFixture(t)
	host := hostFixture(t, target)
	for _, tc := range []struct {
		name  string
		hosts []HostInventory
	}{
		{"host cap", make([]HostInventory, 65)},
		{"page cap", []HostInventory{withPages(host, make([]json.RawMessage, 17))}},
		{"pages after terminal", []HostInventory{withPages(host, []json.RawMessage{fixture(t, "projects-none"), fixture(t, "projects-one")})}},
		{"duplicate cursor", []HostInventory{withPages(host, []json.RawMessage{fixture(t, "projects-page1"), fixture(t, "projects-page1"), fixture(t, "projects-page2")})}},
		{"duplicate project ID", []HostInventory{withPages(host, []json.RawMessage{bytes.ReplaceAll(fixture(t, "projects-one"), []byte(`"nextCursor": null`), []byte(`"nextCursor": "cursor"`)), fixture(t, "projects-one")})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if result := Discover(target, tc.hosts); result.Outcome != InvalidInventory || result.Project != nil {
				t.Fatalf("incomplete inventory produced %+v", result)
			}
		})
	}
}

func withPages(host HostInventory, pages []json.RawMessage) HostInventory {
	host.ProjectPages = pages
	return host
}
