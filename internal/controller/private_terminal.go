package controller

// The durable terminal record of a private executor (DESIGN-025 section 3,
// unit H1). The operator records, from uncached reads, that the Pod named by
// status.privateHistory.lastExecutor.podUID reached a terminal phase on a Ready
// node with a fresh lease, and confirms the write before anything else in that
// reconcile. When the recorded Pod is found gone first, it writes the missed
// marker instead, once. Absence never yields a terminal record. Recording
// deletes, stops and exports nothing.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// MetricPrivateExecutorTerminal counts terminal records written, missed
// markers written (once per Session, whatever the restarts) and terminal
// observations refused because a node, lease, container or read check failed.
const MetricPrivateExecutorTerminal = "dev_env_private_executor_terminal_total"

// The result label's values.
const (
	terminalRecorded = "recorded"
	terminalMissed   = "missed"
	terminalRefused  = "refused"
)

// privateTerminalRetry is how soon a terminal executor whose checks failed is
// first observed again (DESIGN-025 3.3). Each further refusal of the same Pod
// revision doubles the wait, up to privateTerminalRetryMax (DESIGN-025 25,
// OP490). Pod status changes reconcile the Session anyway, and a Pod whose
// revision changed is checked at once; node and lease changes do not
// reconcile it, so they wait for the backoff.
const (
	privateTerminalRetry    = 30 * time.Second
	privateTerminalRetryMax = 30 * time.Minute
)

// terminalRetryAfter is the wait after the n-th refusal in a row of one
// executor: 30s, 1m, 2m, 4m, 8m, 16m, then 30m.
func terminalRetryAfter(n int) time.Duration {
	d := privateTerminalRetry
	for i := 1; i < n && d < privateTerminalRetryMax; i++ {
		d *= 2
	}
	return min(d, privateTerminalRetryMax)
}

// terminalRefusal is the last refused terminal check of one Session's
// recorded executor. It lives in memory only: a new operator checks once at
// once and then backs off from the start again. It changes nothing that is
// refused, only how often the same refusal is checked again.
type terminalRefusal struct {
	sessionUID          types.UID
	podUID, podRevision string
	refusals            int
	reason              string
	next                time.Time
}

// podGCDeletionReason is the DisruptionTarget reason the Pod garbage collector
// sets when it marks a Pod of a lost node Failed. Such a status never counts.
const podGCDeletionReason = "DeletionByPodGC"

// maxTerminalContainers is the schema's bound on the record's containers.
const maxTerminalContainers = 16

// NewPrivateExecutorTerminalCounter returns the counter of MetricPrivateExecutorTerminal,
// with each result's series present at 0. Register it once per process.
func NewPrivateExecutorTerminalCounter() *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricPrivateExecutorTerminal,
		Help: "Private executor terminal observations (DESIGN-025 3): recorded (a confirmed terminal record), missed (the recorded executor was gone before its record; once per Session) and refused (terminal, but a node, lease, container or read check failed).",
	}, []string{"result"})
	for _, result := range []string{terminalRecorded, terminalMissed, terminalRefused} {
		c.WithLabelValues(result)
	}
	return c
}

func (r *Reconciler) countTerminal(result string) {
	if r.PrivateExecutorTerminal != nil {
		r.PrivateExecutorTerminal.WithLabelValues(result).Inc()
	}
}

func (r *Reconciler) clock() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// terminalRecordApplies reports whether the Session may get a terminal record
// or a missed marker now: the flag is on, its retention record is intact, its
// HOME is bound, its executor is recorded, and neither mark exists yet. Canary
// originals are included; section 2 keeps them from expiring, not this.
func (r *Reconciler) terminalRecordApplies(s *v1alpha1.AgentSession) bool {
	h := s.Status.PrivateHistory
	return r.PrivateTerminalRecord && h != nil && privateHistoryIdentity(s) == nil && h.Admission == sharedStarted && h.HomeUID != "" &&
		h.LastExecutor != nil && h.LastExecutor.PodUID != "" && h.ExecutorTerminal == nil && h.ExecutorMissed == nil
}

// executorTerminalRecorded reports whether the Session holds a terminal record
// of its recorded executor.
func executorTerminalRecorded(h *v1alpha1.PrivateHistoryStatus) bool {
	return h != nil && h.ExecutorTerminal != nil && h.LastExecutor != nil && h.ExecutorTerminal.PodUID == h.LastExecutor.PodUID
}

func podPhaseTerminal(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

// verifyExecutorTerminated is DESIGN-025 3.2: uncached reads of the Pod, its
// node and the node's lease. It returns the record to write, or why none may be
// written. A Pod with a deletion timestamp still counts: its terminal phase can
// be the last chance to record it.
func (r *Reconciler) verifyExecutorTerminated(ctx context.Context, s *v1alpha1.AgentSession, uid types.UID) (*v1alpha1.PrivateExecutorTerminalStatus, error) {
	if r.APIReader == nil || uid == "" {
		return nil, errors.New("a terminal record needs an uncached reader and the recorded executor UID")
	}
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &pod); err != nil {
		return nil, fmt.Errorf("recorded executor: %w", err)
	}
	if pod.UID != uid || !controlledBySession(&pod, s) || isHoldPod(&pod) {
		return nil, errors.New("the Pod is not the recorded executor, or the Session does not control it")
	}
	// A private executor restarts its containers (RestartPolicy Always), so a
	// terminated container proves nothing; the terminal phase does.
	if !podPhaseTerminal(&pod) {
		return nil, errors.New("the recorded executor's phase is not terminal")
	}
	if err := allContainersTerminated(&pod); err != nil {
		return nil, err
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.DisruptionTarget && c.Reason == podGCDeletionReason {
			return nil, errors.New("the Pod garbage collector marked the recorded executor Failed; a live kubelet did not")
		}
	}
	if pod.Spec.NodeName == "" {
		return nil, errors.New("the recorded executor has no node")
	}
	// now is the clock after the lease read (executorNodeLive), so observedAt
	// is never before the renewal it judged.
	node, lease, now, err := executorNodeLive(ctx, r.APIReader, pod.Spec.NodeName, r.clock)
	if err != nil {
		return nil, err
	}
	containers, err := executorContainerTerminations(&pod)
	if err != nil {
		return nil, err
	}
	if pod.ResourceVersion == "" || len(pod.ResourceVersion) > 64 || len(node.Name) > 253 || len(node.UID) > 128 || len(uid) > 128 {
		return nil, errors.New("the recorded executor's identity exceeds the record's bounds")
	}
	claude, codex := executorProviderDirs(&pod)
	return &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: string(uid), Phase: string(pod.Status.Phase), Reason: boundText(pod.Status.Reason, 128),
		PodResourceVersion: pod.ResourceVersion, NodeName: node.Name, NodeUID: string(node.UID),
		LeaseRenewedAt: recordTime(lease.Spec.RenewTime.Time), ObservedAt: recordTime(now),
		Containers: containers, ClaudeConfigDir: claude, CodexHome: codex}, nil
}

// recordTime is t as the API server stores a metav1.Time, to the second, so
// the confirmed write compares equal.
func recordTime(t time.Time) metav1.Time {
	return metav1.NewTime(t.UTC().Truncate(time.Second))
}

// boundText cuts s to at most n bytes on a rune boundary.
func boundText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// executorContainerTerminations lists every regular, init and ephemeral
// container's termination. allContainersTerminated has already checked that
// each one is terminated and that the lists match the spec.
func executorContainerTerminations(p *corev1.Pod) ([]v1alpha1.PrivateExecutorContainerTermination, error) {
	var out []v1alpha1.PrivateExecutorContainerTermination
	for _, statuses := range [][]corev1.ContainerStatus{p.Status.ContainerStatuses, p.Status.InitContainerStatuses, p.Status.EphemeralContainerStatuses} {
		for _, st := range statuses {
			t := st.State.Terminated
			if t == nil || st.Name == "" || len(st.Name) > 63 {
				return nil, errors.New("a container of the recorded executor has no recordable termination")
			}
			out = append(out, v1alpha1.PrivateExecutorContainerTermination{Name: st.Name, ExitCode: t.ExitCode, Reason: boundText(t.Reason, 128), FinishedAt: recordTime(t.FinishedAt.Time)})
		}
	}
	if len(out) == 0 || len(out) > maxTerminalContainers {
		return nil, fmt.Errorf("the recorded executor has %d containers; a record holds 1 to %d", len(out), maxTerminalContainers)
	}
	return out, nil
}

// executorProviderDirs returns the agent container's CLAUDE_CONFIG_DIR and
// CODEX_HOME relative to its HOME volume (DESIGN-025 6.3), each only when the
// container's env sets it literally: the last entry of that name has a value,
// not a valueFrom, with no $(VAR) reference, and it is an absolute path strictly
// inside the HOME volume's mount and outside every other mount. Kubernetes ranks
// env above envFrom, so a literal entry is the effective value. Anything else is
// empty, and the export later refuses (layout_unknown) rather than guess.
func executorProviderDirs(p *corev1.Pod) (claudeConfigDir, codexHome string) {
	var agent *corev1.Container
	for i := range p.Spec.Containers {
		if p.Spec.Containers[i].Name == ContainerName {
			agent = &p.Spec.Containers[i]
		}
	}
	if agent == nil {
		return "", ""
	}
	home := ""
	var others []string
	for _, m := range agent.VolumeMounts {
		if m.Name == "home" && m.SubPath == "" && m.SubPathExpr == "" && path.IsAbs(m.MountPath) && home == "" {
			home = path.Clean(m.MountPath)
			continue
		}
		others = append(others, path.Clean(m.MountPath))
	}
	if home == "" || home == "/" {
		return "", ""
	}
	dir := func(name string) string {
		var last *corev1.EnvVar
		for i := range agent.Env {
			if agent.Env[i].Name == name {
				last = &agent.Env[i]
			}
		}
		if last == nil || last.ValueFrom != nil || strings.Contains(last.Value, "$") || !path.IsAbs(last.Value) {
			return ""
		}
		v := path.Clean(last.Value)
		rel, ok := strings.CutPrefix(v, home+"/")
		if !ok || rel == "" || len(rel) > 256 {
			return ""
		}
		for _, o := range others {
			if v == o || strings.HasPrefix(v, o+"/") || strings.HasPrefix(o, v+"/") {
				return ""
			}
		}
		return rel
	}
	return dir("CLAUDE_CONFIG_DIR"), dir("CODEX_HOME")
}

// observeExecutorTerminal adds the terminal record to the Session's retention
// record when pod, the recorded executor just read uncached, is terminal and
// every check passes. applies is terminalRecordApplies of the record as stored,
// before this observation rebuilt lastExecutor; recorded is that stored
// lastExecutor. The caller's confirmed write stores the record, and the count
// follows that write. A failed check writes nothing and asks again after a
// wait that doubles from 30 seconds to 30 minutes while the same Pod revision
// is refused; until then a reconcile reuses the remembered refusal and checks
// nothing (DESIGN-025 25). The refusal goes into the Session's conditions.
func (r *Reconciler) observeExecutorTerminal(ctx context.Context, s *v1alpha1.AgentSession, applies bool, recorded *v1alpha1.PrivateExecutorStatus, pod *corev1.Pod, obs *observation) {
	if !applies || recorded == nil || recorded.PodUID != string(pod.UID) || !podPhaseTerminal(pod) {
		// No check is due for this Pod: a mark exists, the gate is shut or
		// the Pod is not the terminal recorded executor. A remembered
		// refusal has nothing left to back off.
		r.forgetTerminalRefusal(client.ObjectKeyFromObject(s))
		return
	}
	now := r.clock()
	if reason, wait := r.terminalBackoff(s, pod, now); wait > 0 {
		obs.privateTerminalRetry, obs.privateTerminalRefusal = wait, reason
		return
	}
	rec, err := r.verifyExecutorTerminated(ctx, s, pod.UID)
	if err != nil {
		r.countTerminal(terminalRefused)
		reason := boundText(err.Error(), 256)
		refusals, wait := r.noteTerminalRefusal(s, pod, reason, now)
		obs.privateTerminalRetry, obs.privateTerminalRefusal = wait, reason
		log.FromContext(ctx).Info("the recorded private executor is terminal, but its terminal record is refused for now", "podUID", string(pod.UID), "reason", reason,
			"refusals", refusals, "retryAfter", wait.String())
		return
	}
	r.forgetTerminalRefusal(client.ObjectKeyFromObject(s))
	s.Status.PrivateHistory.ExecutorTerminal = rec
	obs.privateTerminal = terminalRecorded
}

// terminalBackoff returns the remembered refusal of this exact executor Pod
// revision and how long to wait before checking it again; a zero wait means
// check now. A changed Pod revision is checked at once.
func (r *Reconciler) terminalBackoff(s *v1alpha1.AgentSession, pod *corev1.Pod, now time.Time) (string, time.Duration) {
	r.terminalMu.Lock()
	defer r.terminalMu.Unlock()
	b := r.terminalRefusals[client.ObjectKeyFromObject(s)]
	if b == nil || b.sessionUID != s.UID || b.podUID != string(pod.UID) || b.podRevision != pod.ResourceVersion || !now.Before(b.next) {
		return "", 0
	}
	// A clock that stepped back never stretches the wait past the cap.
	return b.reason, min(b.next.Sub(now), privateTerminalRetryMax)
}

// noteTerminalRefusal remembers a refused check of the executor Pod and
// returns how many refusals in a row it has had and the wait before the next
// check. A Session that is gone has its entry dropped by Reconcile's NotFound
// read; as a backstop, entries no reconcile came back for within the cap
// after their due time are dropped here too.
func (r *Reconciler) noteTerminalRefusal(s *v1alpha1.AgentSession, pod *corev1.Pod, reason string, now time.Time) (int, time.Duration) {
	r.terminalMu.Lock()
	defer r.terminalMu.Unlock()
	if r.terminalRefusals == nil {
		r.terminalRefusals = map[types.NamespacedName]*terminalRefusal{}
	}
	for key, b := range r.terminalRefusals {
		if now.Sub(b.next) > privateTerminalRetryMax {
			delete(r.terminalRefusals, key)
		}
	}
	key := client.ObjectKeyFromObject(s)
	b := r.terminalRefusals[key]
	if b == nil || b.sessionUID != s.UID || b.podUID != string(pod.UID) {
		b = &terminalRefusal{sessionUID: s.UID, podUID: string(pod.UID)}
		r.terminalRefusals[key] = b
	}
	b.refusals++
	b.podRevision, b.reason = pod.ResourceVersion, reason
	wait := terminalRetryAfter(b.refusals)
	b.next = now.Add(wait)
	return b.refusals, wait
}

// forgetTerminalRefusal drops the remembered refusal of the Session of this
// name: once its terminal record is taken, once no check is due for it any
// more (its executor is gone, or a mark exists), and once the Session itself
// is gone (released or deleted), whatever UID the entry holds.
func (r *Reconciler) forgetTerminalRefusal(key types.NamespacedName) {
	r.terminalMu.Lock()
	defer r.terminalMu.Unlock()
	delete(r.terminalRefusals, key)
}

// observeExecutorAbsent handles an uncached NotFound for the recorded
// executor. With a terminal record the executor ended, and its absence is
// expected (ExecutorEnded). Without one the absence is Unknown, and when
// applies the missed marker is added, once: absence never yields a record.
func (r *Reconciler) observeExecutorAbsent(s *v1alpha1.AgentSession, applies bool, obs *observation) {
	r.forgetTerminalRefusal(client.ObjectKeyFromObject(s))
	h := s.Status.PrivateHistory
	if executorTerminalRecorded(h) {
		obs.privateExecutorEnded = true
		return
	}
	if !h.ExecutorAttempted || h.ExecutorNotCreated {
		return
	}
	obs.privateHistoryUnknown = errors.New("the recorded private executor is absent; absence cannot authorize a retry or prove stop")
	if applies {
		h.ExecutorMissed = &v1alpha1.PrivateExecutorMissedStatus{Version: 1, PodUID: h.LastExecutor.PodUID, ObservedAt: recordTime(r.clock())}
		obs.privateTerminal = terminalMissed
	}
}
