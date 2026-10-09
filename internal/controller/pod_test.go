package controller

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// The unit tests of the pod and volume builders. The envtest suite
// (reconciler_test.go) shows the same objects on a real API server.

func exampleTemplates(t *testing.T) *templates.Templates {
	t.Helper()
	b, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.Parse(map[string]string{templates.Key: string(b)})
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func taskSession() *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "haynes-ops-1006-120000", Namespace: "dev-agents", UID: "uid-1"},
		Spec: v1alpha1.AgentSessionSpec{
			Repo: "haynes-ops", Base: "origin/main", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask,
			Model: "claude-opus-5-5", Effort: "xhigh", Prompt: "fix the docs", Size: v1alpha1.SizeM,
			OperatingMode: v1alpha1.OperatingModeRunning,
			Limits:        &v1alpha1.SessionLimits{Timeout: &metav1.Duration{Duration: 40 * time.Minute}, MaxTurns: 50},
		},
	}
}

func envOf(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

func TestWorkspacePodRequiresBothOptInsAndRetainsPrivateHome(t *testing.T) {
	tmpl := exampleTemplates(t)
	s := taskSession()
	tmpl.Workspace = &templates.Workspace{Enabled: true, Claim: "retained-projects", ID: "projects-v2"}
	private, err := buildPod(s, tmpl, "")
	if err != nil {
		t.Fatal(err)
	}
	if private.Name != s.Name || slices.ContainsFunc(private.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == "workspace" }) {
		t.Fatal("template change silently migrated a private session")
	}
	s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
	shared, err := buildPod(s, tmpl, "")
	if err != nil {
		t.Fatal(err)
	}
	if shared.Name != private.Name || shared.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != HomeClaimName(s.Name) {
		t.Fatal("shared opt-in changed Pod identity or private home")
	}
	for _, sub := range []string{"repos", "codex", "work"} {
		if !slices.ContainsFunc(shared.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool {
			return m.Name == "workspace" && m.MountPath == "/home/dev/"+sub && m.SubPath == sub && !m.ReadOnly
		}) {
			t.Fatalf("missing literal mount %s", sub)
		}
	}
	if !slices.ContainsFunc(shared.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool {
		return m.Name == "workspace" && m.MountPath == templates.WorkspaceMetadataPath && m.SubPath == "metadata"
	}) {
		t.Fatal("missing marker/ownership metadata mount")
	}
	if got := envOf(shared.Spec.Containers[0], templates.WorkspaceIDEnv); got == nil || got.Value != "projects-v2" {
		t.Fatal("workspace binding not operator-owned")
	}
	doc, err := sessionDocument(s)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := protocol.ParseSession([]byte(doc))
	if err != nil || sess.Workspace == nil || sess.Workspace.SessionUID != string(s.UID) {
		t.Fatalf("session binding %+v, %v", sess.Workspace, err)
	}
	for _, change := range []func(){func() { tmpl.Workspace.Enabled = false }, func() { tmpl.Workspace.Enabled = true; tmpl.Workspace.ID = "foreign" }, func() { tmpl.Workspace.ID = "projects-v2"; tmpl.Workspace.Claim = "" }} {
		change()
		if _, err := buildPod(s, tmpl, ""); err == nil {
			t.Fatal("unmatched or disabled workspace built")
		}
	}
}

func TestPodShape(t *testing.T) {
	tmpl := exampleTemplates(t)
	s := taskSession()
	pod, err := buildPod(s, tmpl, "")
	if err != nil {
		t.Fatal(err)
	}
	spec := pod.Spec
	c := spec.Containers[0]

	if pod.Name != s.Name || pod.Namespace != s.Namespace {
		t.Errorf("pod %s/%s, want the session's name and namespace", pod.Namespace, pod.Name)
	}
	if spec.RestartPolicy != corev1.RestartPolicyAlways {
		t.Errorf("restartPolicy %s: a crashed agentd must restart in place, in the same pod", spec.RestartPolicy)
	}
	if *spec.TerminationGracePeriodSeconds != TerminationGracePeriod {
		t.Errorf("grace %d, want %d", *spec.TerminationGracePeriodSeconds, TerminationGracePeriod)
	}
	if spec.PriorityClassName != "dev-env-agent" || spec.ServiceAccountName != "dev-env-agent" {
		t.Errorf("priority class %q, service account %q", spec.PriorityClassName, spec.ServiceAccountName)
	}
	if c.LivenessProbe != nil || c.ReadinessProbe != nil || c.StartupProbe != nil {
		t.Error("a session pod has no probes: a liveness kill would restart the agent (5.1)")
	}
	if c.Image != tmpl.Image || c.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("image %s (%s)", c.Image, c.ImagePullPolicy)
	}

	// 3.6: non-root, read-only root, no capabilities, RuntimeDefault seccomp, ndots 1.
	psc, csc := spec.SecurityContext, c.SecurityContext
	if !*psc.RunAsNonRoot || *psc.RunAsUser != 1000 || *psc.FSGroup != 1000 || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod security context %+v", psc)
	}
	if !*csc.ReadOnlyRootFilesystem || *csc.AllowPrivilegeEscalation || !slices.Equal(csc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Errorf("container security context %+v", csc)
	}
	if o := spec.DNSConfig.Options; len(o) != 1 || o[0].Name != "ndots" || *o[0].Value != "1" {
		t.Errorf("dns options %+v", o)
	}
	for _, key := range []string{corev1.TaintNodeNotReady, corev1.TaintNodeUnreachable} {
		if !slices.ContainsFunc(spec.Tolerations, func(tl corev1.Toleration) bool {
			return tl.Key == key && tl.Effect == corev1.TaintEffectNoExecute && *tl.TolerationSeconds == 3600
		}) {
			t.Errorf("no 3600 s NoExecute toleration for %s", key)
		}
	}

	// 7.1 (D-20): workers only, GPU nodes last, a soft spread.
	na := spec.Affinity.NodeAffinity
	req := na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(req) != 1 || len(req[0].MatchExpressions) != 1 {
		t.Fatalf("required node affinity %+v", req)
	}
	if e := req[0].MatchExpressions[0]; e.Key != "topology.kubernetes.io/zone" || e.Operator != corev1.NodeSelectorOpIn || !slices.Equal(e.Values, []string{"w"}) {
		t.Errorf("required term %+v, want zone In [w]", e)
	}
	pref := na.PreferredDuringSchedulingIgnoredDuringExecution
	if len(pref) != 1 || pref[0].Weight != 30 || pref[0].Preference.MatchExpressions[0].Key != GPULabel ||
		pref[0].Preference.MatchExpressions[0].Operator != corev1.NodeSelectorOpNotIn {
		t.Errorf("preferred node affinity %+v, want weight 30 away from GPU nodes", pref)
	}
	if ts := spec.TopologySpreadConstraints; len(ts) != 1 || ts[0].MaxSkew != 2 || ts[0].TopologyKey != corev1.LabelHostname || ts[0].WhenUnsatisfiable != corev1.ScheduleAnyway {
		t.Errorf("topology spread %+v", ts)
	}

	// 7.2: M's requests and limits.
	if got := c.Resources.Limits.Cpu().String(); got != "4" {
		t.Errorf("M's CPU limit %s, want 4", got)
	}
	if got := c.Resources.Requests.Memory().String(); got != "2Gi" {
		t.Errorf("M's memory request %s, want 2Gi", got)
	}
	cpu := envOf(c, "DEV_ENV_CPU_LIMIT")
	if cpu == nil || cpu.ValueFrom.ResourceFieldRef.Resource != "limits.cpu" || cpu.ValueFrom.ResourceFieldRef.ContainerName != ContainerName {
		t.Errorf("DEV_ENV_CPU_LIMIT %+v", cpu)
	}
	if e := envOf(c, "XDG_RUNTIME_DIR"); e == nil || e.Value != "/dev/shm/run-1000" {
		t.Errorf("XDG_RUNTIME_DIR %+v", e)
	}
	if e := envOf(c, "HOME"); e == nil || e.Value != "/home/dev" {
		t.Errorf("HOME %+v", e)
	}
	if e := envOf(c, protocol.GrantsDirEnv); e == nil || e.Value != protocol.GrantsDir {
		t.Error("the grants store does not point at the operator's reserved volume")
	}
	if e := envOf(c, protocol.KubeconfigEnv); e == nil || e.Value != protocol.GrantsDir+"/"+protocol.KubeconfigName {
		t.Error("KUBECONFIG does not name the grant store")
	}
	for env, field := range map[string]string{protocol.PodUIDEnv: "metadata.uid", protocol.PodNamespaceEnv: "metadata.namespace"} {
		if e := envOf(c, env); e == nil || e.ValueFrom == nil || e.ValueFrom.FieldRef == nil || e.ValueFrom.FieldRef.FieldPath != field {
			t.Errorf("%s must come from the downward API", env)
		}
	}
	if e := envOf(c, "DEV_ENV_CLAUDE_MODEL"); e == nil || e.Value != "claude-opus-5-5" {
		t.Errorf("the templates' env is missing: %+v", e)
	}

	// Volumes: home, shared, /tmp, the API token, the templates' mounts and
	// the profile's.
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range c.VolumeMounts {
		mounts[m.MountPath] = m
	}
	for path, vol := range map[string]string{
		"/home/dev": "home", "/home/dev/.shared": "shared", "/tmp": "tmp",
		"/var/run/secrets/dev-env": "api-token", "/opt/dev-env/config/claude": "config-claude", "/creds": "gh-token",
		"/etc/codex":       "codex-requirements",
		protocol.GrantsDir: "grants",
	} {
		if m, ok := mounts[path]; !ok || m.Name != vol {
			t.Errorf("mount at %s: %+v, want volume %s", path, m, vol)
		}
	}
	vols := map[string]corev1.Volume{}
	for _, v := range spec.Volumes {
		vols[v.Name] = v
	}
	if v := vols["home"]; v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != "home-"+s.Name {
		t.Errorf("home volume %+v", v)
	}
	if v := vols["shared"]; v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != "dev-env-shared" {
		t.Errorf("shared volume %+v", v)
	}
	if v := vols["tmp"]; v.EmptyDir == nil || v.EmptyDir.SizeLimit.String() != "8Gi" {
		t.Errorf("tmp volume %+v", v)
	}
	if v := vols["grants"]; v.EmptyDir == nil || v.EmptyDir.Medium != corev1.StorageMediumMemory || v.EmptyDir.SizeLimit.String() != "16Mi" {
		t.Error("the grant token volume must be bounded and memory-backed")
	}
	if v := vols["api-token"]; v.Projected == nil || v.Projected.Sources[0].ServiceAccountToken.Audience != "dev-env-operator" {
		t.Errorf("api-token volume %+v", v)
	}
	if v := vols["gh-token"]; v.Secret == nil || v.Secret.SecretName != "dev-env-gh-token" {
		t.Errorf("gh-token volume %+v", v)
	}
	if v := vols["scripts"]; v.ConfigMap == nil || *v.ConfigMap.DefaultMode != 0o555 {
		t.Errorf("scripts volume %+v", v)
	}
	for _, m := range c.VolumeMounts {
		if m.Name != "home" && m.Name != "shared" && m.Name != "tmp" && m.Name != "grants" && !m.ReadOnly {
			t.Errorf("template mount %s is writable", m.Name)
		}
	}
	if len(c.EnvFrom) != 1 || c.EnvFrom[0].SecretRef.Name != "dev-env-mcp-secret" {
		t.Errorf("envFrom %+v", c.EnvFrom)
	}

	// Labels and the one owner reference.
	for k, v := range map[string]string{
		v1alpha1.LabelSession: s.Name, v1alpha1.LabelAgent: "claude", v1alpha1.LabelMode: "task", v1alpha1.LabelSize: "M",
		v1alpha1.LabelProfile: "full", v1alpha1.LabelRevision: tmpl.Revision(), v1alpha1.LabelAppName: "dev-env-session",
	} {
		if pod.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, pod.Labels[k], v)
		}
	}
	if pod.Annotations[v1alpha1.AnnotationRepo] != "haynes-ops" {
		t.Errorf("annotations %v", pod.Annotations)
	}
	assertOnlyOwnerIsSession(t, pod.OwnerReferences, s)
}

// The pod and agentd agree on where things are: the API token is where agentd
// looks by default, and AGENTD_API_URL is set only when the operator has an API to
// report to (D-41).
func TestPodMeetsAgentdsSettings(t *testing.T) {
	settings, err := agentd.LoadSettings(func(k string) string {
		if k == "HOME" {
			return "/home/dev"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.APITokenFile != APITokenFile {
		t.Errorf("agentd reads the API token from %s, the pod mounts it at %s", settings.APITokenFile, APITokenFile)
	}
	if settings.SharedDir != templates.SharedPath {
		t.Errorf("agentd's shared dir %s, the pod mounts dev-env-shared at %s", settings.SharedDir, templates.SharedPath)
	}

	tmpl := exampleTemplates(t)
	pod, err := buildPod(taskSession(), tmpl, "")
	if err != nil {
		t.Fatal(err)
	}
	if e := envOf(pod.Spec.Containers[0], "AGENTD_API_URL"); e == nil || e.Value != "" {
		t.Errorf("AGENTD_API_URL %+v with no API, want set and empty (the heartbeat is off)", e)
	}
	url := "https://dev-env-operator.dev-env-system.svc:8443"
	pod, err = buildPod(taskSession(), tmpl, url)
	if err != nil {
		t.Fatal(err)
	}
	if e := envOf(pod.Spec.Containers[0], "AGENTD_API_URL"); e == nil || e.Value != url {
		t.Errorf("AGENTD_API_URL %+v, want %s", e, url)
	}
	if *pod.Spec.TerminationGracePeriodSeconds <= 30 {
		t.Errorf("agentd gives the CLI 30 s after SIGTERM (D-42); the grace must be longer, got %d", *pod.Spec.TerminationGracePeriodSeconds)
	}
}

// Every variable the operator owns is in the container's env, which Kubernetes
// ranks above envFrom, so no Secret in a profile's envFrom can supply it (D-44).
// The one exception is the static token outside task and local mode: it is left
// out there, not set empty.
func TestTheOperatorSetsEveryReservedVariable(t *testing.T) {
	tmpl := exampleTemplates(t)
	for _, mode := range []v1alpha1.SessionMode{v1alpha1.ModeTask, v1alpha1.ModeRemote} {
		s := taskSession()
		s.Spec.Mode = mode
		if mode != v1alpha1.ModeTask {
			s.Spec.Prompt, s.Spec.Limits = "", nil
		}
		pod, err := buildPod(s, tmpl, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range templates.ReservedEnv {
			e := envOf(pod.Spec.Containers[0], name)
			if name == "CLAUDE_CODE_OAUTH_TOKEN" && mode == v1alpha1.ModeRemote {
				if e != nil {
					t.Errorf("remote: %s is set", name)
				}
				continue
			}
			if e == nil {
				t.Errorf("%s: %s is not in the container's env, so an envFrom source could supply it", mode, name)
			}
		}
	}
}

func assertOnlyOwnerIsSession(t *testing.T, refs []metav1.OwnerReference, s *v1alpha1.AgentSession) {
	t.Helper()
	if len(refs) != 1 {
		t.Fatalf("%d owner references, want exactly the session's: %+v", len(refs), refs)
	}
	r := refs[0]
	if r.APIVersion != "dev-env.haynesops.com/v1alpha1" || r.Kind != "AgentSession" || r.Name != s.Name || r.UID != s.UID ||
		r.Controller == nil || !*r.Controller {
		t.Errorf("owner reference %+v, want the session as controller", r)
	}
}

func TestSessionDocument(t *testing.T) {
	s := taskSession()
	pod, err := buildPod(s, exampleTemplates(t), "")
	if err != nil {
		t.Fatal(err)
	}
	e := envOf(pod.Spec.Containers[0], protocol.SessionEnv)
	if e == nil {
		t.Fatal("no AGENTD_SESSION")
	}
	got, err := protocol.ParseSession([]byte(e.Value))
	if err != nil {
		t.Fatalf("agentd refuses the document: %v", err)
	}
	want := protocol.Session{
		Name: s.Name, Repo: "haynes-ops", Base: "origin/main", Agent: "claude", Mode: "task", Model: "claude-opus-5-5",
		Effort: "xhigh", Prompt: "fix the docs", Limits: &protocol.Limits{Timeout: "40m0s", MaxTurns: 50},
	}
	if got.Name != want.Name || got.Repo != want.Repo || got.Base != want.Base || got.Agent != want.Agent || got.Mode != want.Mode ||
		got.Model != want.Model || got.Effort != want.Effort || got.Prompt != want.Prompt || *got.Limits != *want.Limits {
		t.Errorf("document %+v, want %+v", got, want)
	}
	if got.TimeoutDuration() != 40*time.Minute {
		t.Errorf("timeout %s", got.TimeoutDuration())
	}
}

// A session agentd would refuse never gets a pod: the build fails with a
// sessionInvalidError, which the reconciler reports as Failed.
func TestSessionAgentdWouldRefuse(t *testing.T) {
	s := taskSession()
	s.Spec.Prompt = strings.Repeat("x", protocol.MaxPromptBytes+1)
	_, err := buildPod(s, exampleTemplates(t), "")
	if !errors.As(err, new(sessionInvalidError)) || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("want a sessionInvalidError about the prompt, got %v", err)
	}
}

// The static token goes to Claude task and local pods only (6.1); a remote pod
// runs on the keeper's access token and must not have it (6.2).
func TestStaticTokenByMode(t *testing.T) {
	tmpl := exampleTemplates(t)
	for _, c := range []struct {
		agent v1alpha1.AgentKind
		mode  v1alpha1.SessionMode
		want  bool
	}{
		{v1alpha1.AgentClaude, v1alpha1.ModeTask, true},
		{v1alpha1.AgentClaude, v1alpha1.ModeLocal, true},
		{v1alpha1.AgentClaude, v1alpha1.ModeRemote, false},
		{v1alpha1.AgentCodex, v1alpha1.ModeTask, false},
	} {
		s := taskSession()
		s.Spec.Agent, s.Spec.Mode = c.agent, c.mode
		if c.agent == v1alpha1.AgentCodex {
			s.Spec.Model = "gpt-6-astra"
		}
		if c.mode != v1alpha1.ModeTask {
			s.Spec.Prompt, s.Spec.Limits = "", nil
		}
		pod, err := buildPod(s, tmpl, "")
		if err != nil {
			t.Fatalf("%s/%s: %v", c.agent, c.mode, err)
		}
		e := envOf(pod.Spec.Containers[0], "CLAUDE_CODE_OAUTH_TOKEN")
		if (e != nil) != c.want {
			t.Errorf("%s/%s: static token present = %v, want %v", c.agent, c.mode, e != nil, c.want)
		}
		if e != nil && (e.ValueFrom.SecretKeyRef.Name != "dev-env-claude-secret" || e.Value != "") {
			t.Errorf("%s/%s: the token must come from its Secret: %+v", c.agent, c.mode, e)
		}
	}
}

func TestProfileAndSize(t *testing.T) {
	tmpl := exampleTemplates(t)
	s := taskSession()
	s.Spec.Profile, s.Spec.Size = "ops", v1alpha1.SizeL
	s.Spec.Caller, s.Spec.Lane = "alert-responder", v1alpha1.LaneRemediation
	pod, err := buildPod(s, tmpl, "")
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	if got := c.Resources.Limits.Memory().String(); got != "24Gi" {
		t.Errorf("L's memory limit %s", got)
	}
	if pod.Labels[v1alpha1.LabelProfile] != "ops" || pod.Labels["dev-env.haynesops.com/egress"] != "ops" || pod.Labels[v1alpha1.LabelLane] != "remediation" {
		t.Errorf("labels %v", pod.Labels)
	}
	if pod.Annotations[v1alpha1.AnnotationCaller] != "alert-responder" {
		t.Errorf("annotations %v", pod.Annotations)
	}
	if e := envOf(c, "CIGAR_JOURNAL_TOKEN"); e == nil || e.ValueFrom.SecretKeyRef.Name != "dev-env-cigar-secret" {
		t.Errorf("the ops profile's env is missing: %+v", e)
	}
	if len(c.EnvFrom) != 0 {
		t.Errorf("ops has no envFrom, got %+v", c.EnvFrom)
	}

	s.Spec.Profile = "nope"
	if _, err := buildPod(s, tmpl, ""); err == nil || errors.As(err, new(sessionInvalidError)) {
		t.Errorf("an unknown profile is a templates problem (Pending), not an invalid session: %v", err)
	}
}

func TestHomeClaim(t *testing.T) {
	tmpl := exampleTemplates(t)
	s := taskSession()
	claim, err := buildHomeClaim(s, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Name != "home-"+s.Name || *claim.Spec.StorageClassName != "gasha01-rbd" ||
		!slices.Equal(claim.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}) ||
		claim.Spec.Resources.Requests.Storage().String() != "20Gi" {
		t.Errorf("claim %+v", claim)
	}
	if _, ok := claim.Labels[v1alpha1.LabelRevision]; ok {
		t.Error("the volume outlives revisions; it carries no revision label")
	}
	assertOnlyOwnerIsSession(t, claim.OwnerReferences, s)
}

// The hold pod (D-55) is the session's pod with `agentd hold`, the S class, and
// no agent credentials: it mounts what a rescue needs and nothing an agent does.
func TestHoldPodShape(t *testing.T) {
	tmpl := exampleTemplates(t)
	s := taskSession()
	s.Spec.Size = v1alpha1.SizeL
	pod, err := buildHoldPod(s, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	full, err := buildPod(s, tmpl, "https://api")
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	if !isHoldPod(pod) || isHoldPod(full) || pod.Labels[v1alpha1.LabelSize] != "S" || pod.Name != s.Name {
		t.Errorf("name %s, labels %v", pod.Name, pod.Labels)
	}
	if !slices.Equal(c.Args, []string{"hold"}) || len(c.Command) != 0 || len(full.Spec.Containers[0].Args) != 0 {
		t.Errorf("args %q, command %q; the session pod's args %q", c.Args, c.Command, full.Spec.Containers[0].Args)
	}
	small, _ := tmpl.Size(v1alpha1.SizeS)
	if !apiequality.Semantic.DeepEqual(c.Resources.Limits, small.Limits) || !apiequality.Semantic.DeepEqual(c.Resources.Requests, small.Requests) {
		t.Errorf("resources %+v, want S's", c.Resources)
	}
	for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CIGAR_JOURNAL_TOKEN"} {
		if envOf(c, name) != nil {
			t.Errorf("the hold pod has %s", name)
		}
	}
	if len(c.EnvFrom) != 0 {
		t.Errorf("envFrom %+v", c.EnvFrom)
	}
	if e := envOf(c, "AGENTD_API_URL"); e == nil || e.Value != "" {
		t.Errorf("AGENTD_API_URL %+v, want set and empty", e)
	}
	if e := envOf(c, protocol.SessionEnv); e == nil || !strings.Contains(e.Value, `"name":"`+s.Name+`"`) {
		t.Errorf("the session document %+v", e)
	}
	// Rescue keeps the session data, shared bundle destination and GitHub token.
	for _, name := range []string{"home", "shared", "gh-token"} {
		if !slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == name }) ||
			!slices.ContainsFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == name }) {
			t.Errorf("rescue is missing volume or mount %s", name)
		}
	}
	if !apiequality.Semantic.DeepEqual(pod.Spec.Affinity, full.Spec.Affinity) || !apiequality.Semantic.DeepEqual(c.SecurityContext, full.Spec.Containers[0].SecurityContext) || *pod.Spec.SecurityContext.RunAsUser != 1000 {
		t.Error("placement or security differ from the session pod's")
	}
	assertOnlyOwnerIsSession(t, pod.OwnerReferences, s)
}

func TestHoldPodExcludesAgentProfileMounts(t *testing.T) {
	for _, tc := range []struct {
		name, profile, resolvedProfile, githubSecret, githubMount string
	}{
		{"default", "", "full", "dev-env-gh-token", "gh-token"},
		{"full", "full", "full", "dev-env-gh-token", "gh-token"},
		{"dev", "dev", "dev", "dev-env-gh-token", "gh-token"},
		{"ops", "ops", "ops", "dev-env-ops-gh-token", "gh-token"},
		{"renamed-github", "full", "full", "dev-env-gh-token", "github-credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := exampleTemplates(t)
			profile := tmpl.Profiles[tc.resolvedProfile]
			profile.Mounts[0].Name = tc.githubMount
			profile.Env = append(profile.Env, corev1.EnvVar{Name: "GOOGLE_APPLICATION_CREDENTIALS", Value: "/etc/gcp/sa_key.json"})
			profile.EnvFrom = append(profile.EnvFrom, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "agent-auth"},
			}})
			profile.Mounts = append(profile.Mounts,
				templates.Mount{Name: "gcp-sa", Secret: "dev-env-gcp-secret", Path: "/etc/gcp"},
				templates.Mount{Name: "agent-settings", ConfigMap: "agent-settings", Path: "/etc/agent-settings"},
			)
			tmpl.Profiles[tc.resolvedProfile] = profile
			s := taskSession()
			s.Spec.Profile = tc.profile
			agent, err := buildPod(s, tmpl, "https://api")
			if err != nil {
				t.Fatal(err)
			}
			hold, err := buildHoldPod(s, tmpl)
			if err != nil {
				t.Fatal(err)
			}
			agentContainer, holdContainer := agent.Spec.Containers[0], hold.Spec.Containers[0]
			for _, name := range []string{"gcp-sa", "agent-settings"} {
				if !slices.ContainsFunc(agent.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == name }) ||
					!slices.ContainsFunc(agentContainer.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == name && m.ReadOnly }) {
					t.Errorf("agent lost its %s volume or read-only mount", name)
				}
				if slices.ContainsFunc(hold.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == name }) ||
					slices.ContainsFunc(holdContainer.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == name }) {
					t.Errorf("rescue retains the agent-only %s volume or mount", name)
				}
			}
			for name, claim := range map[string]string{"home": "home-" + s.Name, "shared": tmpl.SharedClaim} {
				if !slices.ContainsFunc(hold.Spec.Volumes, func(v corev1.Volume) bool {
					return v.Name == name && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == claim
				}) {
					t.Errorf("rescue lost its %s data volume", name)
				}
			}
			if !slices.ContainsFunc(hold.Spec.Volumes, func(v corev1.Volume) bool {
				return v.Name == tc.githubMount && v.Secret != nil && v.Secret.SecretName == tc.githubSecret
			}) {
				t.Errorf("rescue lost its profile's GitHub token")
			}
			for name, path := range map[string]string{"home": templates.HomePath, "shared": templates.SharedPath, tc.githubMount: "/creds"} {
				if !slices.ContainsFunc(holdContainer.VolumeMounts, func(m corev1.VolumeMount) bool {
					return m.Name == name && m.MountPath == path && (name != tc.githubMount || m.ReadOnly)
				}) {
					t.Errorf("rescue lost its %s mount at %s", name, path)
				}
			}
			for _, common := range tmpl.Mounts {
				if !slices.ContainsFunc(hold.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == common.Name }) ||
					!slices.ContainsFunc(holdContainer.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == common.Name && m.MountPath == common.Path }) {
					t.Errorf("rescue lost its common %s volume or mount", common.Name)
				}
			}
			for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CIGAR_JOURNAL_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS"} {
				if envOf(holdContainer, name) != nil {
					t.Errorf("rescue retains agent auth environment %s", name)
				}
			}
			if envOf(agentContainer, "CLAUDE_CODE_OAUTH_TOKEN") == nil || envOf(agentContainer, "GOOGLE_APPLICATION_CREDENTIALS") == nil || len(agentContainer.EnvFrom) == 0 {
				t.Error("ordinary session lost agent authentication")
			}
			apiURL := envOf(holdContainer, "AGENTD_API_URL")
			if len(holdContainer.EnvFrom) != 0 || !slices.Equal(holdContainer.Args, HoldArgs) || apiURL == nil || apiURL.Value != "" {
				t.Error("rescue enables agent credentials, execution or heartbeat")
			}
		})
	}
}

func TestDecisionPodFeatureCannotBeEnabledByTemplateOrPrivateTask(t *testing.T) {
	for _, kind := range []string{"default", "enabled", "managed-off", "private", "claude", "local", "hold"} {
		t.Run(kind, func(t *testing.T) {
			tmpl := exampleTemplates(t)
			tmpl.Workspace = &templates.Workspace{Enabled: true, Claim: "retained-projects", ID: "projects-v2"}
			tmpl.Env = append(tmpl.Env, corev1.EnvVar{Name: "AGENTD_ENABLE_CHILD_DECISIONS", Value: "true"})
			s := taskSession()
			s.Spec.Agent, s.Spec.Model, s.Spec.Effort, s.Spec.Limits = v1alpha1.AgentCodex, "gpt-6.1-sol", "xhigh", nil
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
			managed, enabled := true, true
			switch kind {
			case "default":
				enabled = false
			case "managed-off":
				managed = false
			case "private":
				s.Spec.Workspace = nil
			case "claude":
				s.Spec.Agent, s.Spec.Model = v1alpha1.AgentClaude, "claude-opus-5-5"
			case "local":
				s.Spec.Mode = v1alpha1.ModeLocal
			}
			p, err := buildPod(s, tmpl, "", managed, enabled)
			if kind == "hold" {
				p, err = buildHoldPod(s, tmpl)
			}
			if err != nil {
				t.Fatal(err)
			}
			var final string
			for _, e := range p.Spec.Containers[0].Env {
				if e.Name == "AGENTD_ENABLE_CHILD_DECISIONS" {
					final = e.Value
				}
			}
			want := "false"
			if kind == "enabled" {
				want = "true"
			}
			if final != want {
				t.Fatal("template widened child-decision admission", final, want)
			}
		})
	}
}
