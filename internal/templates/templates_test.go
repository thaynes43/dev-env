package templates

import (
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func example(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parse(t *testing.T, doc string) (*Templates, error) {
	t.Helper()
	return Parse(map[string]string{Key: doc})
}

func TestParseExample(t *testing.T) {
	tmpl, err := parse(t, example(t))
	if err != nil {
		t.Fatalf("the example does not parse: %v", err)
	}
	if got := tmpl.StorageClassFor(v1alpha1.SizeL); got != "gasha01-rbd" {
		t.Errorf("L's storage class = %q, want the home default gasha01-rbd", got)
	}
	m, err := tmpl.Size("")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Limits.Cpu().String(); got != "4" {
		t.Errorf("an empty size class is M, whose CPU limit is 4; got %s", got)
	}
	name, p, err := tmpl.Profile("")
	if err != nil || name != "full" || len(p.Mounts) != 1 {
		t.Errorf("an empty profile is the default full; got %q, %+v, %v", name, p, err)
	}
	if _, _, err := tmpl.Profile("nope"); err == nil {
		t.Error("an unknown profile resolved")
	}
	if got := *tmpl.Mounts[2].DefaultMode; got != 0o555 {
		t.Errorf("YAML 0555 is octal, as in Kubernetes manifests; got %o", got)
	}
	rev := tmpl.Revision()
	if !strings.HasPrefix(rev, "2.0.0-") || len(rev) != len("2.0.0-")+10 {
		t.Errorf("revision %q is not <tag>-<10 hex>", rev)
	}
	if msgs := validation.IsValidLabelValue(rev); len(msgs) > 0 {
		t.Errorf("revision %q is not a label value: %v", rev, msgs)
	}
}

func TestParseNeedsTheKey(t *testing.T) {
	if _, err := Parse(map[string]string{"other.yaml": example(t)}); err == nil || !strings.Contains(err.Error(), Key) {
		t.Fatalf("want an error naming %s, got %v", Key, err)
	}
}

// The revision hashes content, not text: a comment or a reordered key is not a
// new revision (it would mark every session outdated for nothing), and any
// change of a value is.
func TestRevisionFollowsContentOnly(t *testing.T) {
	base, err := parse(t, example(t))
	if err != nil {
		t.Fatal(err)
	}
	reformatted := "# a new comment\n" + strings.Replace(example(t),
		"home:\n  storageClassName: gasha01-rbd\n  size: 20Gi\n",
		"home: { size: 20Gi, storageClassName: gasha01-rbd }  # reordered\n", 1)
	same, err := parse(t, reformatted)
	if err != nil {
		t.Fatal(err)
	}
	if same.Revision() != base.Revision() {
		t.Errorf("a comment and a reordered key changed the revision: %s to %s", base.Revision(), same.Revision())
	}

	for name, edit := range map[string][2]string{
		"image":  {"0000000000000000000000000000000000000000000000000000000000000001", "0000000000000000000000000000000000000000000000000000000000000002"},
		"limit":  {`limits: { cpu: "4", memory: 8Gi }`, `limits: { cpu: "4", memory: 9Gi }`},
		"mount":  {"path: /creds }\n  dev:", "path: /creds2 }\n  dev:"},
		"envvar": {`value: claude-opus-5-5`, `value: claude-sonnet-5-5`},
	} {
		changed, err := parse(t, strings.Replace(example(t), edit[0], edit[1], 1))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if changed.Revision() == base.Revision() {
			t.Errorf("changing the %s kept revision %s", name, base.Revision())
		}
	}
}

func TestImageTag(t *testing.T) {
	for image, want := range map[string]string{
		"ghcr.io/thaynes43/dev-env:2.0.3@sha256:ab":    "2.0.3",
		"ghcr.io/thaynes43/dev-env@sha256:ab":          "",
		"registry:5000/thaynes43/dev-env@sha256:ab":    "",
		"registry:5000/thaynes43/dev-env:v2@sha256:ab": "v2",
	} {
		if got := imageTag(image); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", image, got, want)
		}
	}
}

// A tag that cannot start a label value leaves the revision as the bare hash.
func TestRevisionWithoutAUsableTag(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/thaynes43/dev-env@sha256:0000000000000000000000000000000000000000000000000000000000000001",
		"ghcr.io/thaynes43/dev-env:_odd@sha256:0000000000000000000000000000000000000000000000000000000000000001",
	} {
		doc := strings.Replace(example(t), "ghcr.io/thaynes43/dev-env:2.0.0@sha256:0000000000000000000000000000000000000000000000000000000000000001", image, 1)
		tmpl, err := parse(t, doc)
		if err != nil {
			t.Fatal(err)
		}
		if rev := tmpl.Revision(); len(rev) != 10 {
			t.Errorf("image %s: revision %q, want 10 hex characters", image, rev)
		}
	}
}

func TestValidateRefuses(t *testing.T) {
	cases := []struct {
		name, old, new, want string
	}{
		{"strict: an unknown field", "sharedClaim: dev-env-shared", "sharedClaim: dev-env-shared\nsharedClaims: x", "unknown field"},
		{"strict: a field in the wrong case", "  storageClassName: gasha01-rbd\n", "  storageclassName: gasha01-rbd\n", "unknown field"},
		{"strict: a field set twice", "sharedClaim: dev-env-shared", "sharedClaim: dev-env-shared\nsharedClaim: other", "already set"},
		{"an image without a digest", "dev-env:2.0.0@sha256:0000000000000000000000000000000000000000000000000000000000000001", "dev-env:2.0.0", "not pinned by digest"},
		{"a missing size class", "  L:\n    requests: { cpu: \"1\", memory: 6Gi }\n    limits: { cpu: \"8\", memory: 24Gi }\n", "", "sizes.L is missing"},
		{"a size class the CRD does not have", "  L:\n", "  XL:\n    requests: { cpu: \"1\", memory: 6Gi }\n    limits: { cpu: \"8\", memory: 24Gi }\n  L:\n", "sizes.XL is not a size class"},
		{"no CPU limit (7.2: mandatory)", `limits: { cpu: "4", memory: 8Gi }`, `limits: { memory: 8Gi }`, "sizes.M.limits.cpu must be set"},
		{"a request above its limit", `requests: { cpu: 250m, memory: 2Gi }`, `requests: { cpu: "5", memory: 2Gi }`, "above its limit"},
		{"a GPU in a size class", `limits: { cpu: "4", memory: 8Gi }`, `limits: { cpu: "4", memory: 8Gi, nvidia.com/gpu: "1" }`, "only cpu, memory and ephemeral-storage"},
		{"no home storage class", "  storageClassName: gasha01-rbd\n", "", "home.storageClassName is empty"},
		{"a zero home size", "size: 20Gi", "size: 0", "home.size must be positive"},
		{"no static token key", "key: CLAUDE_CODE_OAUTH_TOKEN }", "key: \"\" }", "claude.staticToken"},
		{"the static token as plain env", "  - { name: CODEX_HOME, value: /home/dev/.codex }", "  - { name: CLAUDE_CODE_OAUTH_TOKEN, value: x }", "CLAUDE_CODE_OAUTH_TOKEN is set by the operator"},
		{"AGENTD_API_URL, which the operator sets", "  - { name: CODEX_HOME, value: /home/dev/.codex }", "  - { name: AGENTD_API_URL, value: http://x }", "AGENTD_API_URL is set by the operator"},
		{"a mount over the API token", "path: /opt/dev-env/config/codex }", "path: /var/run/secrets/dev-env }", "overlaps /var/run/secrets/dev-env"},
		{"HOME, which the operator sets", "  - { name: CODEX_HOME, value: /home/dev/.codex }", "  - { name: HOME, value: /root }", "HOME is set by the operator"},
		{"a variable 6.2 forbids", "  - { name: CODEX_HOME, value: /home/dev/.codex }", "  - { name: DISABLE_TELEMETRY, value: \"1\" }", "DISABLE_TELEMETRY may not be set"},
		{"a variable set twice", "  - { name: CODEX_HOME, value: /home/dev/.codex }", "  - { name: CLAUDE_CONFIG_DIR, value: /x }", "CLAUDE_CONFIG_DIR is set twice"},
		{"a profile repeating a common variable", "    env:\n      - name: CIGAR_JOURNAL_TOKEN", "    env:\n      - name: DISABLE_AUTOUPDATER", "DISABLE_AUTOUPDATER is set twice"},
		{"a reserved volume name", "name: config-codex,", "name: home,", `volume name "home" is already used by the operator`},
		{"a mount under the home volume", "path: /opt/dev-env/config/codex }", "path: /home/dev/.codex }", "overlaps /home/dev"},
		{"a mount above the home volume", "path: /opt/dev-env/config/codex }", "path: /home }", "overlaps /home/dev"},
		{"a mount at /tmp", "path: /opt/dev-env/config/codex }", "path: /tmp }", "overlaps /tmp"},
		{"a mount at the root", "path: /opt/dev-env/config/codex }", "path: / }", "overlaps"},
		{"a relative mount path", "path: /opt/dev-env/config/codex }", "path: opt/codex }", "not a clean absolute path"},
		{"a mount with two sources", "configMap: dev-env-config-codex,", "configMap: dev-env-config-codex, secret: x,", "exactly one of configMap and secret"},
		{"a profile mount at a common path", "path: /creds }\n  dev:", "path: /opt/dev-env/scripts }\n  dev:", "already mounted for every pod"},
		{"a profile reusing a common volume name", "name: gh-token, secret: dev-env-gh-token, path: /creds }\n  dev:", "name: scripts, secret: dev-env-gh-token, path: /creds }\n  dev:", `volume name "scripts" is already used`},
		{"envFrom with no source", "      - secretRef: { name: dev-env-mcp-secret }\n    mounts:\n      - { name: gh-token, secret: dev-env-gh-token, path: /creds }\n  dev:", "      - prefix: X_\n    mounts:\n      - { name: gh-token, secret: dev-env-gh-token, path: /creds }\n  dev:", "exactly one of secretRef and configMapRef"},
		{"a default profile that is not there", "defaultProfile: full", "defaultProfile: nope", `defaultProfile "nope" is not in profiles`},
		{"a profile label the operator owns", "dev-env.haynesops.com/egress: ops", "dev-env.haynesops.com/profile: full", "is set by the operator"},
		{"an app.kubernetes.io label", "dev-env.haynesops.com/egress: ops", "app.kubernetes.io/name: x", "is set by the operator"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := example(t)
			if !strings.Contains(doc, c.old) {
				t.Fatalf("the example lacks %q", c.old)
			}
			_, err := parse(t, strings.Replace(doc, c.old, c.new, 1))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestIsOperatorLabel(t *testing.T) {
	for key, want := range map[string]bool{
		v1alpha1.LabelSession:            true,
		v1alpha1.LabelRevision:           true,
		"app.kubernetes.io/managed-by":   true,
		"dev-env.haynesops.com/egress":   false,
		"dev-env.haynesops.com/standing": false,
	} {
		if got := IsOperatorLabel(key); got != want {
			t.Errorf("IsOperatorLabel(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestOverlaps(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"/home/dev", "/home/dev", true},
		{"/home/dev/x", "/home/dev", true},
		{"/home", "/home/dev", true},
		{"/", "/tmp", true},
		{"/home/devx", "/home/dev", false},
		{"/opt/dev-env", "/tmp", false},
	} {
		if got := overlaps(c.a, c.b); got != c.want {
			t.Errorf("overlaps(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// The quantities come back as Kubernetes parses them, so the pod gets exactly
// the class's numbers.
func TestSizeQuantities(t *testing.T) {
	tmpl, err := parse(t, example(t))
	if err != nil {
		t.Fatal(err)
	}
	l, _ := tmpl.Size(v1alpha1.SizeL)
	if l.Requests.Memory().Cmp(*l.Limits.Memory()) >= 0 {
		t.Error("L's memory request is not below its limit")
	}
	if got := l.Limits[corev1.ResourceMemory]; got.String() != "24Gi" {
		t.Errorf("L's memory limit = %s, want 24Gi", got.String())
	}
}

// D-60: the timers are optional, take D-09's defaults when missing, are
// checked, and are not part of the revision.
func TestLifecycle(t *testing.T) {
	plain, err := parse(t, example(t))
	if err != nil {
		t.Fatal(err)
	}
	if plain.IdleSuspendAfter(v1alpha1.ModeTask) != time.Hour || plain.IdleSuspendAfter(v1alpha1.ModeLocal) != 72*time.Hour ||
		plain.ArchiveAfter() != 168*time.Hour || plain.BundleRetention() != 720*time.Hour {
		t.Errorf("defaults: %v %v %v %v", plain.IdleSuspendAfter(v1alpha1.ModeTask), plain.IdleSuspendAfter(v1alpha1.ModeLocal), plain.ArchiveAfter(), plain.BundleRetention())
	}
	set, err := parse(t, example(t)+"\nlifecycle:\n  taskIdleSuspendAfter: 30m\n  idleSuspendAfter: 24h\n  archiveAfter: 72h\n")
	if err != nil {
		t.Fatal(err)
	}
	if set.IdleSuspendAfter(v1alpha1.ModeTask) != 30*time.Minute || set.IdleSuspendAfter(v1alpha1.ModeRemote) != 24*time.Hour ||
		set.ArchiveAfter() != 72*time.Hour || set.BundleRetention() != 720*time.Hour {
		t.Errorf("set: %+v", set.Lifecycle)
	}
	if set.Revision() != plain.Revision() {
		t.Errorf("a timer changed the revision: %s, was %s", set.Revision(), plain.Revision())
	}
	for _, bad := range []string{"\nlifecycle:\n  archiveAfter: -1h\n", "\nlifecycle:\n  idleSuspendAfter: 0s\n", "\nlifecycle:\n  sleepAfter: 1h\n"} {
		if _, err := parse(t, example(t)+bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
