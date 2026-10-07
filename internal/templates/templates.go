// Package templates reads dev-env-templates, the GitOps data every session pod is
// built from (D-04): the agent image, the size classes, the session volume, the
// profiles and the mounts every pod gets. haynes-ops ships the ConfigMap; the
// operator only reads it. The format is D-44.
//
// Parse is strict: an unknown field, a missing size class or a reserved name is an
// error, so a typo in haynes-ops leaves new sessions Pending with the reason
// instead of building a pod that silently lacks something. A running pod never
// changes when the templates do (DESIGN-001 5.1, 5.2).
package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	kjson "sigs.k8s.io/json"
	"sigs.k8s.io/yaml"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	// DefaultName is the ConfigMap's name. It lives in the operator's own
	// namespace, dev-env-system.
	DefaultName = "dev-env-templates"
	// Key is the ConfigMap data key that holds the templates document.
	Key = "templates.yaml"
)

// Paths the operator mounts itself. Template mounts may not sit at, above or below
// any of them.
const (
	HomePath       = "/home/dev"
	SharedPath     = "/home/dev/.shared"
	TmpPath        = "/tmp"
	APITokenDir    = "/var/run/secrets/dev-env"
	ServiceAccount = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// Volume names the operator uses itself.
var reservedVolumeNames = []string{"home", "shared", "tmp", "api-token"}

// ReservedEnv are the environment variables the operator owns. The templates' env
// may not name them. A Secret or ConfigMap in a profile's envFrom is not checked,
// because the operator reads no Secrets (DESIGN-001 6.11); D-44 says what such a
// source may hold. The operator sets every one of these in the container's env,
// which Kubernetes ranks above envFrom, empty when unused, so a source cannot
// supply them. The exception is CLAUDE_CODE_OAUTH_TOKEN outside task and local
// mode: an empty value is not the same as an unset one to every program, so the
// operator leaves it out there, and agentd's remote mode unsets it (plan 03).
var ReservedEnv = []string{
	"HOME",
	"XDG_RUNTIME_DIR",
	"DEV_ENV_CPU_LIMIT",
	"AGENTD_SESSION",
	"AGENTD_SESSION_FILE",
	// The operator API's address and the token agentd calls it with (D-41).
	"AGENTD_API_URL",
	"AGENTD_API_TOKEN_FILE",
	// The static token goes to task and local pods only, never remote ones
	// (DESIGN-001 6.1, 6.2), so only the operator places it: see Claude.
	"CLAUDE_CODE_OAUTH_TOKEN",
}

// Environment variables no session pod may set (DESIGN-001 6.2, "Environment
// rules"): the first two turn Remote Control off, the last two switch the CLI to a
// check that wants a refresh token. The templates' env may not name them; agentd
// also removes them before it starts the CLI (D-42).
var forbiddenEnv = []string{
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	"DISABLE_GROWTHBOOK",
	"DISABLE_TELEMETRY",
	"DO_NOT_TRACK",
}

// Templates is the parsed templates document.
type Templates struct {
	// Image is the agent image, pinned by digest
	// (ghcr.io/thaynes43/dev-env:2.0.3@sha256:...). Renovate bumps it.
	Image string `json:"image"`

	// Sizes holds the requests and limits of each size class (DESIGN-001 7.2).
	// Every class the CRD allows (S, M, L) must be present.
	Sizes map[v1alpha1.SizeClass]Size `json:"sizes"`

	// Home is the session volume (D-22): one RWO claim per session.
	Home Home `json:"home"`

	// SharedClaim is the RWX claim every pod mounts at /home/dev/.shared
	// (dev-env-shared, D-22).
	SharedClaim string `json:"sharedClaim"`

	// DefaultProfile is the profile of a session whose spec names none.
	DefaultProfile string `json:"defaultProfile"`

	// Profiles name what a session pod gets beyond the common part (D-18):
	// Secrets as environment or files, and labels the egress tiers and standing
	// grants select on.
	Profiles map[string]Profile `json:"profiles"`

	// Env is set in every session pod: the paths and switches of the agent image
	// (CLAUDE_CONFIG_DIR, DEV_ENV_CLAUDE_MODEL and so on).
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Mounts are mounted in every session pod: the GitOps config agentd renders
	// (/opt/dev-env/config/claude and the rest, in v1's layout).
	Mounts []Mount `json:"mounts,omitempty"`

	// Claude holds what the operator gives Claude sessions by mode.
	Claude Claude `json:"claude"`

	// Lifecycle holds D-09's timers. Each one is optional, and a missing one
	// takes D-09's default (D-60). A session's spec.lifecycle overrides them.
	// They decide nothing about a pod, so the revision leaves them out: a timer
	// change marks no session Outdated.
	Lifecycle *Lifecycle `json:"lifecycle,omitempty"`

	revision string
}

// Lifecycle is D-09's timers (DESIGN-001 4.3, D-60).
type Lifecycle struct {
	// TaskIdleSuspendAfter suspends a task session that is not busy, once
	// nothing has happened in it for this long: its task finished (D-09: 1h).
	TaskIdleSuspendAfter *metav1.Duration `json:"taskIdleSuspendAfter,omitempty"`
	// IdleSuspendAfter suspends an idle local or remote session (D-09: 72h).
	IdleSuspendAfter *metav1.Duration `json:"idleSuspendAfter,omitempty"`
	// ArchiveAfter archives a suspended session's volume, after a valid rescue
	// (D-09: 168h; plan 02 step 7).
	ArchiveAfter *metav1.Duration `json:"archiveAfter,omitempty"`
	// BundleRetention keeps rescue bundles this long (D-09: 720h; plan 02
	// step 11).
	BundleRetention *metav1.Duration `json:"bundleRetention,omitempty"`
}

// D-09's defaults, for a timer the templates do not set.
const (
	DefaultTaskIdleSuspendAfter = time.Hour
	DefaultIdleSuspendAfter     = 72 * time.Hour
	DefaultArchiveAfter         = 168 * time.Hour
	DefaultBundleRetention      = 720 * time.Hour
)

func durationOr(d *metav1.Duration, def time.Duration) time.Duration {
	if d == nil {
		return def
	}
	return d.Duration
}

// IdleSuspendAfter is how long a session of mode may be idle before the
// operator suspends it: TaskIdleSuspendAfter for a task, IdleSuspendAfter for
// the others.
func (t *Templates) IdleSuspendAfter(mode v1alpha1.SessionMode) time.Duration {
	var l Lifecycle
	if t.Lifecycle != nil {
		l = *t.Lifecycle
	}
	if mode == v1alpha1.ModeTask {
		return durationOr(l.TaskIdleSuspendAfter, DefaultTaskIdleSuspendAfter)
	}
	return durationOr(l.IdleSuspendAfter, DefaultIdleSuspendAfter)
}

// ArchiveAfter is how long a session stays suspended before archive.
func (t *Templates) ArchiveAfter() time.Duration {
	if t.Lifecycle == nil {
		return DefaultArchiveAfter
	}
	return durationOr(t.Lifecycle.ArchiveAfter, DefaultArchiveAfter)
}

// BundleRetention is how long rescue bundles are kept.
func (t *Templates) BundleRetention() time.Duration {
	if t.Lifecycle == nil {
		return DefaultBundleRetention
	}
	return durationOr(t.Lifecycle.BundleRetention, DefaultBundleRetention)
}

// Size is one size class.
type Size struct {
	// Requests and Limits each need cpu and memory. CPU limits are mandatory
	// (DESIGN-001 7.2).
	Requests corev1.ResourceList `json:"requests"`
	Limits   corev1.ResourceList `json:"limits"`

	// StorageClassName overrides Home.StorageClassName for this class. It is
	// how size L moves to ceph-block if spike S-8 says so (DESIGN-001 6.6).
	StorageClassName string `json:"storageClassName,omitempty"`
}

// Home is the session volume.
type Home struct {
	StorageClassName string            `json:"storageClassName"`
	Size             resource.Quantity `json:"size"`
}

// Profile is one profile.
type Profile struct {
	Env []corev1.EnvVar `json:"env,omitempty"`
	// EnvFrom brings in every key of a Secret or ConfigMap, such as the MCP
	// tokens. Parse cannot see the keys: a source must not hold a variable that
	// ReservedEnv or forbiddenEnv names (D-44).
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`
	Mounts  []Mount                `json:"mounts,omitempty"`
	// Labels go on the pod, for the egress tiers and the broker's standing
	// policies to select on.
	Labels map[string]string `json:"labels,omitempty"`
}

// Mount is a ConfigMap or a Secret mounted read-only as a directory.
type Mount struct {
	// Name is the pod volume's name.
	Name string `json:"name"`
	// Path is where it is mounted.
	Path string `json:"path"`
	// ConfigMap or Secret names the source; exactly one is set.
	ConfigMap string `json:"configMap,omitempty"`
	Secret    string `json:"secret,omitempty"`
	// DefaultMode is the files' mode, for example 0555 for scripts.
	DefaultMode *int32 `json:"defaultMode,omitempty"`
}

// Claude holds what the operator gives Claude sessions.
type Claude struct {
	// StaticToken is the Secret key holding the static token (DESIGN-001 6.1).
	// The operator sets it as CLAUDE_CODE_OAUTH_TOKEN in task and local pods
	// only: remote pods run on the keeper's access token and must not have it
	// (6.2).
	StaticToken SecretKey `json:"staticToken"`
}

// SecretKey is one key of one Secret in the sessions' namespace.
type SecretKey struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// Parse reads the templates from a ConfigMap's data and validates them.
func Parse(data map[string]string) (*Templates, error) {
	doc, ok := data[Key]
	if !ok {
		return nil, fmt.Errorf("the ConfigMap has no %q key", Key)
	}
	// YAML to JSON, then the API server's own strict decoder: field names are
	// case-sensitive, and an unknown or repeated field is an error.
	j, err := yaml.YAMLToJSONStrict([]byte(doc))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Key, err)
	}
	var t Templates
	strict, err := kjson.UnmarshalStrict(j, &t)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Key, err)
	}
	if len(strict) > 0 {
		return nil, fmt.Errorf("%s: %w", Key, errors.Join(strict...))
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", Key, err)
	}
	rev, err := t.computeRevision()
	if err != nil {
		return nil, err
	}
	t.revision = rev
	return &t, nil
}

// Revision is the hash of the templates' content (D-04, DESIGN-001 5.2), prefixed
// with the image tag when there is one: 2.0.3-1a2b3c4d5e. It hashes the parsed
// document, so a comment or a reordered key is not a new revision, and it is a
// valid label value.
func (t *Templates) Revision() string { return t.revision }

func (t *Templates) computeRevision() (string, error) {
	// The timers reach no pod, so they are not part of the revision.
	c := *t
	c.Lifecycle = nil
	// Marshal sorts map keys, so equal content gives equal bytes.
	b, err := json.Marshal(&c)
	if err != nil {
		return "", fmt.Errorf("hash the templates: %w", err)
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])[:10]
	if tag := imageTag(t.Image); tag != "" && len(validation.IsValidLabelValue(tag+"-"+hash)) == 0 {
		return tag + "-" + hash, nil
	}
	return hash, nil
}

// Profile returns the profile a session runs with: its own, or the default.
func (t *Templates) Profile(name string) (string, Profile, error) {
	if name == "" {
		name = t.DefaultProfile
	}
	p, ok := t.Profiles[name]
	if !ok {
		return name, Profile{}, fmt.Errorf("profile %q is not in the templates", name)
	}
	return name, p, nil
}

// Size returns a size class. An empty class is M, as the CRD defaults it.
func (t *Templates) Size(class v1alpha1.SizeClass) (Size, error) {
	if class == "" {
		class = v1alpha1.SizeM
	}
	s, ok := t.Sizes[class]
	if !ok {
		return Size{}, fmt.Errorf("size class %q is not in the templates", class)
	}
	return s, nil
}

// StorageClassFor returns the session volume's storage class for a size class.
func (t *Templates) StorageClassFor(class v1alpha1.SizeClass) string {
	if s, err := t.Size(class); err == nil && s.StorageClassName != "" {
		return s.StorageClassName
	}
	return t.Home.StorageClassName
}

// Validate checks the templates.
func (t *Templates) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if t.Image == "" {
		add("image is empty")
	} else if !strings.Contains(t.Image, "@sha256:") {
		add("image %q is not pinned by digest (name:tag@sha256:...)", t.Image)
	}

	for _, class := range []v1alpha1.SizeClass{v1alpha1.SizeS, v1alpha1.SizeM, v1alpha1.SizeL} {
		s, ok := t.Sizes[class]
		if !ok {
			add("sizes.%s is missing", class)
			continue
		}
		errs = append(errs, validateSize(string(class), s)...)
	}
	for class := range t.Sizes {
		if class != v1alpha1.SizeS && class != v1alpha1.SizeM && class != v1alpha1.SizeL {
			add("sizes.%s is not a size class (S, M or L)", class)
		}
	}

	if l := t.Lifecycle; l != nil {
		for name, d := range map[string]*metav1.Duration{
			"taskIdleSuspendAfter": l.TaskIdleSuspendAfter, "idleSuspendAfter": l.IdleSuspendAfter,
			"archiveAfter": l.ArchiveAfter, "bundleRetention": l.BundleRetention,
		} {
			if d != nil && d.Duration <= 0 {
				add("lifecycle.%s must be a positive duration such as 1h", name)
			}
		}
	}
	if t.Home.StorageClassName == "" {
		add("home.storageClassName is empty")
	}
	if t.Home.Size.Sign() <= 0 {
		add("home.size must be positive")
	}
	if msgs := validation.IsDNS1123Subdomain(t.SharedClaim); len(msgs) > 0 {
		add("sharedClaim %q: %s", t.SharedClaim, strings.Join(msgs, "; "))
	}
	if t.Claude.StaticToken.Name == "" || t.Claude.StaticToken.Key == "" {
		add("claude.staticToken needs a name and a key")
	}

	names := map[string]string{}
	for _, n := range reservedVolumeNames {
		names[n] = "the operator"
	}
	envNames := map[string]bool{}
	errs = append(errs, validateEnv("env", t.Env, envNames)...)
	errs = append(errs, validateMounts("mounts", t.Mounts, names)...)

	if len(t.Profiles) == 0 {
		add("profiles is empty")
	}
	if _, ok := t.Profiles[t.DefaultProfile]; !ok {
		add("defaultProfile %q is not in profiles", t.DefaultProfile)
	}
	for name, p := range t.Profiles {
		where := "profiles." + name
		if msgs := validation.IsDNS1123Label(name); len(msgs) > 0 {
			add("%s: the name: %s", where, strings.Join(msgs, "; "))
		}
		// A profile may not repeat what every pod gets, but two profiles
		// may use the same names: a pod has one profile.
		pEnv := maps.Clone(envNames)
		errs = append(errs, validateEnv(where+".env", p.Env, pEnv)...)
		for i, ef := range p.EnvFrom {
			if (ef.SecretRef == nil) == (ef.ConfigMapRef == nil) {
				add("%s.envFrom[%d] needs exactly one of secretRef and configMapRef", where, i)
			}
		}
		pNames := maps.Clone(names)
		errs = append(errs, validateMounts(where+".mounts", p.Mounts, pNames)...)
		if msgs := validateGlobalMountPaths(t.Mounts, p.Mounts); len(msgs) > 0 {
			for _, m := range msgs {
				add("%s.mounts: %s", where, m)
			}
		}
		for k, v := range p.Labels {
			if msgs := validation.IsQualifiedName(k); len(msgs) > 0 {
				add("%s.labels: key %q: %s", where, k, strings.Join(msgs, "; "))
			}
			if msgs := validation.IsValidLabelValue(v); len(msgs) > 0 {
				add("%s.labels: value %q: %s", where, v, strings.Join(msgs, "; "))
			}
			if IsOperatorLabel(k) {
				add("%s.labels: %q is set by the operator", where, k)
			}
		}
	}
	return errors.Join(errs...)
}

func validateSize(class string, s Size) []error {
	var errs []error
	for _, part := range []struct {
		name string
		list corev1.ResourceList
	}{{"requests", s.Requests}, {"limits", s.Limits}} {
		for _, r := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			q, ok := part.list[r]
			if !ok || q.Sign() <= 0 {
				errs = append(errs, fmt.Errorf("sizes.%s.%s.%s must be set and positive", class, part.name, r))
			}
		}
		for r := range part.list {
			if r != corev1.ResourceCPU && r != corev1.ResourceMemory && r != corev1.ResourceEphemeralStorage {
				errs = append(errs, fmt.Errorf("sizes.%s.%s.%s: only cpu, memory and ephemeral-storage are allowed", class, part.name, r))
			}
		}
	}
	for r, req := range s.Requests {
		if lim, ok := s.Limits[r]; ok && req.Cmp(lim) > 0 {
			errs = append(errs, fmt.Errorf("sizes.%s: the %s request %s is above its limit %s", class, r, req.String(), lim.String()))
		}
	}
	return errs
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateEnv(where string, env []corev1.EnvVar, seen map[string]bool) []error {
	var errs []error
	for _, e := range env {
		switch {
		case !envName.MatchString(e.Name):
			errs = append(errs, fmt.Errorf("%s: %q is not a variable name", where, e.Name))
		case slices.Contains(ReservedEnv, e.Name):
			errs = append(errs, fmt.Errorf("%s: %s is set by the operator", where, e.Name))
		case slices.Contains(forbiddenEnv, e.Name):
			errs = append(errs, fmt.Errorf("%s: %s may not be set in a session pod (DESIGN-001 6.2)", where, e.Name))
		case seen[e.Name]:
			errs = append(errs, fmt.Errorf("%s: %s is set twice", where, e.Name))
		}
		seen[e.Name] = true
	}
	return errs
}

func validateMounts(where string, mounts []Mount, names map[string]string) []error {
	var errs []error
	for i, m := range mounts {
		at := fmt.Sprintf("%s[%d]", where, i)
		if msgs := validation.IsDNS1123Label(m.Name); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("%s: name %q: %s", at, m.Name, strings.Join(msgs, "; ")))
		}
		if by, ok := names[m.Name]; ok {
			errs = append(errs, fmt.Errorf("%s: volume name %q is already used by %s", at, m.Name, by))
		}
		names[m.Name] = at
		if (m.ConfigMap == "") == (m.Secret == "") {
			errs = append(errs, fmt.Errorf("%s: set exactly one of configMap and secret", at))
		}
		if m.DefaultMode != nil && (*m.DefaultMode < 0 || *m.DefaultMode > 0o777) {
			errs = append(errs, fmt.Errorf("%s: defaultMode %o is not a file mode", at, *m.DefaultMode))
		}
		if !path.IsAbs(m.Path) || path.Clean(m.Path) != m.Path {
			errs = append(errs, fmt.Errorf("%s: path %q is not a clean absolute path", at, m.Path))
			continue
		}
		for _, r := range []string{HomePath, SharedPath, TmpPath, APITokenDir, ServiceAccount} {
			if overlaps(m.Path, r) {
				errs = append(errs, fmt.Errorf("%s: path %s overlaps %s, which the operator mounts", at, m.Path, r))
			}
		}
	}
	for i := range mounts {
		for j := i + 1; j < len(mounts); j++ {
			if mounts[i].Path == mounts[j].Path {
				errs = append(errs, fmt.Errorf("%s: path %s is mounted twice", where, mounts[i].Path))
			}
		}
	}
	return errs
}

// validateGlobalMountPaths refuses a profile mount at the path of a mount every pod
// gets.
func validateGlobalMountPaths(global, profile []Mount) []string {
	var msgs []string
	for _, p := range profile {
		for _, g := range global {
			if p.Path == g.Path {
				msgs = append(msgs, fmt.Sprintf("path %s is already mounted for every pod", p.Path))
			}
		}
	}
	return msgs
}

// overlaps reports whether a is b, under b, or above b.
func overlaps(a, b string) bool {
	under := func(x, dir string) bool { return x == dir || dir == "/" || strings.HasPrefix(x, dir+"/") }
	return under(a, b) || under(b, a)
}

// IsOperatorLabel reports whether the operator sets this label key itself, so no
// profile may: the well-known app.kubernetes.io keys and every key under
// dev-env.haynesops.com/ that names the session.
func IsOperatorLabel(key string) bool {
	return strings.HasPrefix(key, "app.kubernetes.io/") || slices.Contains(operatorLabels, key)
}

var operatorLabels = []string{
	v1alpha1.LabelSession,
	v1alpha1.LabelAgent,
	v1alpha1.LabelMode,
	v1alpha1.LabelSize,
	v1alpha1.LabelProfile,
	v1alpha1.LabelRevision,
	v1alpha1.LabelLane,
}

// imageTag returns the tag of name:tag@sha256:..., or "".
func imageTag(image string) string {
	ref, _, _ := strings.Cut(image, "@")
	// The tag follows the last ':' after the last '/', so a registry port
	// (host:5000/name) is not a tag.
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon <= slash {
		return ""
	}
	return ref[colon+1:]
}
