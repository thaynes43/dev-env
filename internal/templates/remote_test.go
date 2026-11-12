package templates

import (
	"strings"
	"testing"
)

// Today's revision of testdata/templates.yaml, which has no remote block.
// Captured from the fixture before the block existed (same string as the
// tmp-size legacy pin: the example sets no tmpSizeLimit).
const fixtureRevisionWithoutRemote = "2.0.0-fffc40c6b4"

func withOpsRemote(t *testing.T, block string) string {
	t.Helper()
	doc := example(t)
	const old = "  ops:\n    env:\n"
	if !strings.Contains(doc, old) {
		t.Fatal("the example ops profile moved")
	}
	return strings.Replace(doc, old, "  ops:\n"+block+"    env:\n", 1)
}

const remoteProjection = "" +
	"    remote:\n" +
	"      env:\n" +
	"        - { name: AGENTD_CLAUDE_LIVE_FILE, value: /opt/dev-env/claude-access/access.json }\n" +
	"      mounts:\n" +
	"        - { name: claude-access, secret: dev-env-claude-live, path: /opt/dev-env/claude-access, defaultMode: 0440 }\n"

func TestRemoteBlockParses(t *testing.T) {
	tmpl, err := parse(t, withOpsRemote(t, remoteProjection))
	if err != nil {
		t.Fatal(err)
	}
	_, ops, err := tmpl.Profile("ops")
	if err != nil {
		t.Fatal(err)
	}
	if ops.Remote == nil || len(ops.Remote.Env) != 1 || len(ops.Remote.Mounts) != 1 {
		t.Fatalf("remote block %+v", ops.Remote)
	}
	env := ops.Remote.Env[0]
	if env.Name != "AGENTD_CLAUDE_LIVE_FILE" || env.Value != "/opt/dev-env/claude-access/access.json" || env.ValueFrom != nil {
		t.Fatalf("remote env %+v", env)
	}
	m := ops.Remote.Mounts[0]
	if m.Name != "claude-access" || m.Secret != "dev-env-claude-live" || m.ConfigMap != "" || m.Path != "/opt/dev-env/claude-access" || m.DefaultMode == nil || *m.DefaultMode != 0o440 {
		t.Fatalf("remote mount %+v", m)
	}
	if _, full, err := tmpl.Profile("full"); err != nil || full.Remote != nil {
		t.Fatalf("a profile without the block gained one: %+v %v", full.Remote, err)
	}
	mountsOnly := "    remote:\n      mounts:\n        - { name: claude-access, secret: dev-env-claude-live, path: /opt/dev-env/claude-access }\n"
	parsed, err := parse(t, withOpsRemote(t, mountsOnly))
	if err != nil {
		t.Fatal(err)
	}
	if _, p, err := parsed.Profile("ops"); err != nil || p.Remote == nil || len(p.Remote.Env) != 0 || len(p.Remote.Mounts) != 1 {
		t.Fatalf("mounts-only block %+v %v", p.Remote, err)
	}
}

func TestRemoteBlockRefused(t *testing.T) {
	cases := []struct {
		name, block, want string
	}{
		{"empty object", "    remote: {}\n", "profiles.ops.remote needs env or mounts"},
		{"empty lists", "    remote:\n      env: []\n      mounts: []\n", "profiles.ops.remote needs env or mounts"},
		{"envFrom, which the block does not hold", "    remote:\n      envFrom:\n        - secretRef: { name: dev-env-mcp-secret }\n", "unknown field"},
		{"labels, which the block does not hold", "    remote:\n      labels: { app: x }\n      env:\n        - { name: AGENTD_CLAUDE_LIVE_FILE, value: /x }\n", "unknown field"},
		{"serviceAccount, which the block does not hold", "    remote:\n      serviceAccount: dev-env-workbench\n      env:\n        - { name: AGENTD_CLAUDE_LIVE_FILE, value: /x }\n", "unknown field"},
		{"a name that is not a variable", "    remote:\n      env:\n        - { name: \"1NO\", value: x }\n", "is not a variable name"},
		{"a reserved name", "    remote:\n      env:\n        - { name: HOME, value: /root }\n", "HOME is set by the operator"},
		{"a forbidden name", "    remote:\n      env:\n        - { name: DISABLE_TELEMETRY, value: \"1\" }\n", "DISABLE_TELEMETRY may not be set"},
		{"the same name as the profile", "    remote:\n      env:\n        - { name: CIGAR_JOURNAL_TOKEN, value: x }\n", "profiles.ops.remote.env: CIGAR_JOURNAL_TOKEN is set twice"},
		{"the same name as the document", "    remote:\n      env:\n        - { name: DISABLE_AUTOUPDATER, value: \"0\" }\n", "profiles.ops.remote.env: DISABLE_AUTOUPDATER is set twice"},
		{"the same name twice in the block", "    remote:\n      env:\n        - { name: AGENTD_CLAUDE_LIVE_FILE, value: a }\n        - { name: AGENTD_CLAUDE_LIVE_FILE, value: b }\n", "AGENTD_CLAUDE_LIVE_FILE is set twice"},
		{"a mount name the profile uses", "    remote:\n      mounts:\n        - { name: gh-token, secret: dev-env-claude-live, path: /opt/dev-env/claude-access }\n", `volume name "gh-token" is already used`},
		{"a mount name the document uses", "    remote:\n      mounts:\n        - { name: scripts, secret: dev-env-claude-live, path: /opt/dev-env/claude-access }\n", `volume name "scripts" is already used`},
		{"a path the profile mounts", "    remote:\n      mounts:\n        - { name: claude-access, secret: dev-env-claude-live, path: /creds }\n", "path /creds is already mounted by this profile"},
		{"a path the document mounts", "    remote:\n      mounts:\n        - { name: claude-access, secret: dev-env-claude-live, path: /opt/dev-env/scripts }\n", "path /opt/dev-env/scripts is already mounted for every pod"},
		{"a path the block mounts twice", "    remote:\n      mounts:\n        - { name: claude-access, secret: dev-env-claude-live, path: /opt/dev-env/claude-access }\n        - { name: claude-live, secret: dev-env-claude-live, path: /opt/dev-env/claude-access }\n", "path /opt/dev-env/claude-access is mounted twice"},
		{"a path the operator mounts", "    remote:\n      mounts:\n        - { name: claude-access, secret: dev-env-claude-live, path: /tmp }\n", "overlaps /tmp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parse(t, withOpsRemote(t, c.block))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error containing %q, got %v", c.want, err)
			}
		})
	}
}

// The five nestings from the OP765 review, plus a sibling that must still parse.
// Equality stays covered by TestRemoteBlockRefused; these are inside or above.
func TestRemoteBlockRefusesNestedMounts(t *testing.T) {
	cases := []struct {
		name, block, path string
	}{
		{
			"under the profile mount /creds",
			"    remote:\n      mounts:\n        - { name: gh-token-file, secret: dev-env-claude-live, path: /creds/gh_token }\n",
			"/creds/gh_token",
		},
		{
			"under the document mount /etc/codex",
			"    remote:\n      mounts:\n        - { name: codex-req-file, secret: dev-env-claude-live, path: /etc/codex/requirements.toml }\n",
			"/etc/codex/requirements.toml",
		},
		{
			"under the document mount /opt/dev-env/scripts",
			"    remote:\n      mounts:\n        - { name: agent-run-file, secret: dev-env-claude-live, path: /opt/dev-env/scripts/agent-run }\n",
			"/opt/dev-env/scripts/agent-run",
		},
		{
			"above the document config mounts",
			"    remote:\n      mounts:\n        - { name: config-root, secret: dev-env-claude-live, path: /opt/dev-env/config }\n",
			"/opt/dev-env/config",
		},
		{
			"a parent and a child inside the block",
			"    remote:\n      mounts:\n        - { name: review-root, secret: dev-env-claude-live, path: /opt/review-root }\n        - { name: review-child, secret: dev-env-claude-live, path: /opt/review-root/child }\n",
			"/opt/review-root/child",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parse(t, withOpsRemote(t, c.block))
			if err == nil || !strings.Contains(err.Error(), "overlaps") || !strings.Contains(err.Error(), c.path) {
				t.Fatalf("want an overlap refusal for %s, got %v", c.path, err)
			}
		})
	}
}

func TestRemoteBlockAcceptsSiblingMount(t *testing.T) {
	const block = "    remote:\n      mounts:\n        - { name: creds-other, secret: dev-env-claude-live, path: /creds-other }\n"
	tmpl, err := parse(t, withOpsRemote(t, block))
	if err != nil {
		t.Fatal(err)
	}
	_, ops, err := tmpl.Profile("ops")
	if err != nil || ops.Remote == nil || len(ops.Remote.Mounts) != 1 || ops.Remote.Mounts[0].Path != "/creds-other" {
		t.Fatalf("sibling mount %+v %v", ops.Remote, err)
	}
}

func TestDocumentWithoutRemoteBlockKeepsRevision(t *testing.T) {
	tmpl, err := parse(t, example(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := tmpl.Revision(); got != fixtureRevisionWithoutRemote {
		t.Fatalf("fixture revision %s, want today's %s", got, fixtureRevisionWithoutRemote)
	}
	for name, p := range tmpl.Profiles {
		if p.Remote != nil {
			t.Fatalf("profile %s has a remote block", name)
		}
	}
	with, err := parse(t, withOpsRemote(t, remoteProjection))
	if err != nil {
		t.Fatal(err)
	}
	if with.Revision() == fixtureRevisionWithoutRemote {
		t.Fatal("a remote block is pod content and must change the revision")
	}
}
