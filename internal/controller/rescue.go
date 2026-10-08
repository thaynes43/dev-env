package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/podexec"
)

// The operator's side of D-10's rescue (D-51). Before a suspend deletes a pod
// that ran, the operator runs `agentd ctl rescue --stop-agent` in it by exec
// (D-08: commands go operator to pod by exec), turns agentd's report into a
// verdict, and records it in status before anything is deleted.

// RescueCommand is what the operator runs in the session's container. It stops
// the agent first, so the rescue is the worktree's last state (D-48).
var RescueCommand = []string{"agentd", "ctl", "rescue", "--stop-agent"}

// RescueTimeout bounds one rescue: the agent's 30 s stop, a 60 s fetch per
// clone, and up to 5 minutes per bundle (D-43, D-48).
const RescueTimeout = 10 * time.Minute

// maxReport caps what the operator reads from the rescue's stdout.
const maxReport = 4 << 20

// maxRecordedRefs is how many unpushed refs status keeps (RescueStatus).
const maxRecordedRefs = 256

// Rescuer runs agentd's rescue in a session's pod and returns its report. An
// error means the rescue did not run, or its report could not be read; a
// report that is not OK is not an error.
type Rescuer interface {
	Rescue(ctx context.Context, pod *corev1.Pod) (protocol.RescueReport, error)
}

// ExecRescuer runs the rescue through the API server's pods/exec subresource,
// which the operator's RBAC allows in dev-agents (DESIGN-001 6.11).
type ExecRescuer struct {
	exec    *podexec.Executor
	timeout time.Duration
}

// NewExecRescuer returns a Rescuer that execs into pods with cfg.
func NewExecRescuer(cfg *rest.Config) (*ExecRescuer, error) {
	ex, err := podexec.New(cfg)
	if err != nil {
		return nil, err
	}
	return &ExecRescuer{exec: ex, timeout: RescueTimeout}, nil
}

// Rescue implements Rescuer.
func (e *ExecRescuer) Rescue(ctx context.Context, pod *corev1.Pod) (protocol.RescueReport, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	stdout, stderr := &cappedBuffer{max: maxReport}, &cappedBuffer{max: 64 << 10}
	err := e.exec.Run(ctx, pod.Namespace, pod.Name, ContainerName, RescueCommand, nil, stdout, stderr)
	return parseRescueOutput(stdout, stderr.String(), err)
}

// parseRescueOutput reads agentd's report. agentd exits 1 when the rescue is
// not OK and prints the report all the same, so a readable report wins over
// exit code 1; any other failure means the rescue did not run.
func parseRescueOutput(stdout *cappedBuffer, stderr string, runErr error) (protocol.RescueReport, error) {
	var rep protocol.RescueReport
	parseErr := stdout.err
	if parseErr == nil {
		dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
		parseErr = dec.Decode(&rep)
		if parseErr == nil && (rep.Session == "" || rep.Stamp == "") {
			parseErr = errors.New("the report names no session or stamp")
		}
	}
	var code utilexec.ExitError
	exitedOneOrZero := runErr == nil || (errors.As(runErr, &code) && code.ExitStatus() == 1)
	if parseErr == nil && exitedOneOrZero {
		return rep, nil
	}
	detail := strings.TrimSpace(stderr)
	if len(detail) > 400 {
		detail = detail[:400] + "..."
	}
	switch {
	case runErr != nil && detail != "":
		return protocol.RescueReport{}, fmt.Errorf("%w: %s", runErr, detail)
	case runErr != nil:
		return protocol.RescueReport{}, runErr
	default:
		return protocol.RescueReport{}, fmt.Errorf("unreadable rescue report: %w", parseErr)
	}
}

// cappedBuffer keeps at most max bytes and remembers that it overflowed. It
// holds its buffer in a field rather than embedding it: an embedded
// bytes.Buffer brings ReadFrom and WriteString, and io.Copy, which the exec
// stream uses, would call ReadFrom and pass the cap.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
	err error
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.err = fmt.Errorf("more than %d bytes", b.max)
		// Keep reading, so the stream ends normally.
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *cappedBuffer) String() string { return b.buf.String() }

// verdict turns agentd's report into the rescue record (D-51). agentd checked
// its own bundles (D-48); the operator checks that the report adds up: it is
// this session's, the agent was stopped, every clone fetched, and every ref
// origin lacked is in a verified bundle at the same commit. Any doubt is
// Failed, which keeps the volume.
func verdict(s *v1alpha1.AgentSession, pod *corev1.Pod, rep protocol.RescueReport, now metav1.Time) (*v1alpha1.RescueStatus, string) {
	rec := &v1alpha1.RescueStatus{
		Stamp: rep.Stamp, At: &now, PodUID: string(pod.UID), Generation: s.Generation,
	}
	if s.Status.Rescue != nil {
		rec.LastBundle = s.Status.Rescue.LastBundle
	}
	if rep.Bundle != nil && rep.Bundle.Manifest != "" {
		rec.LastBundle = rep.Bundle.Manifest
	}
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	reason := "RescueFailed"
	setReason := func(r string) {
		if reason == "RescueFailed" {
			reason = r
		}
	}

	if rep.Session != s.Name {
		setReason("ReportMismatch")
		add("the report is for session %q", rep.Session)
	}
	switch {
	case rep.Agent == nil:
		setReason("AgentNotStopped")
		add("the report does not say the agent was stopped")
	case rep.Agent.Running:
		setReason("AgentStillRunning")
		add("the agent still ran after the stop, so it may have written more")
	}
	refs := 0
	switch {
	case rep.VolumeEmpty && (len(rep.Repos) > 0 || rep.Bundle != nil || !rep.OK || !rep.CleanAndPushed):
		setReason("ReportMismatch")
		add("the report says the volume is empty but also lists clones, a bundle or a failure")
	case rep.VolumeEmpty:
		// agentd proved the volume holds nothing: no pod ever wrote to it, so
		// there is no clone to look for (D-55).
	case !slices.ContainsFunc(rep.Repos, func(r protocol.RepoRescue) bool { return path.Base(r.Path) == s.Spec.Repo }):
		// The session's own clone must be on the report: agentd finds clones
		// by looking, so a missing ~/repos, a clone that failed, or a .git
		// that is not a directory would otherwise read as nothing to save.
		setReason("NotProven")
		add("the session's clone ~/repos/%s is not in the report, so nothing proves the volume holds no work", s.Spec.Repo)
	}
	for _, repo := range rep.Repos {
		if repo.Error != "" {
			setReason("RepoError")
			add("%s: %s", repo.Path, repo.Error)
		}
		if !repo.Fetched {
			// With stale remote refs, a local branch that only a since-deleted
			// origin branch held is on no list and in no bundle.
			setReason("FetchFailed")
			add("%s: origin could not be fetched (%s)", repo.Path, repo.FetchError)
		}
		for _, w := range repo.Worktrees {
			if w.Refused != "" {
				setReason("WorktreeRefused")
				add("%s: %s", w.Path, w.Refused)
			}
		}
		var bundled []protocol.BundleRef
		file := ""
		if b := repo.Bundle; b != nil {
			file = b.File
			if b.Error != "" || !b.Verified {
				setReason("BundleFailed")
				add("%s: bundle: %s", repo.Path, firstNonEmpty(b.Error, "not verified"))
			} else {
				bundled = b.Refs
			}
		}
		for _, ref := range repo.UnpushedRefs {
			refs++
			if len(rec.UnpushedRefs) < maxRecordedRefs {
				rec.UnpushedRefs = append(rec.UnpushedRefs, v1alpha1.RescuedRef{Repo: repo.Path, Name: ref.Name, Commit: ref.Commit, Bundle: file})
			} else {
				rec.OmittedRefs++
			}
			if !slices.ContainsFunc(bundled, func(b protocol.BundleRef) bool { return b.Source == ref.Name && b.Commit == ref.Commit }) {
				setReason("BundleFailed")
				add("%s: %s (%s) is in no verified bundle", repo.Path, ref.Name, short(ref.Commit))
			}
		}
	}
	if refs > 0 && (rep.Bundle == nil || rep.Bundle.Manifest == "") {
		setReason("BundleFailed")
		msg := "no manifest was written"
		if rep.Bundle != nil && rep.Bundle.Error != "" {
			msg = rep.Bundle.Error
		}
		add("bundle: %s", msg)
	}
	if !rep.OK && len(problems) == 0 {
		add("agentd reported the rescue not ok")
	}

	stopped := "the agent was not running"
	if rep.Agent != nil && rep.Agent.WasRunning {
		stopped = "the agent was stopped"
		if rep.Agent.Killed {
			stopped = "the agent was killed after the grace"
		}
	}
	switch {
	case len(problems) > 0:
		rec.Result = v1alpha1.RescueFailed
		rec.Message = truncate("the rescue " + rep.Stamp + " failed: " + strings.Join(problems, "; "))
		return rec, reason
	case rep.VolumeEmpty:
		rec.Result = v1alpha1.RescueCleanAndPushed
		rec.Message = fmt.Sprintf("the rescue %s found the volume empty: nothing but lost+found, so no pod ever wrote to it", rep.Stamp)
		return rec, "VolumeEmpty"
	case refs == 0 && rep.CleanAndPushed:
		rec.Result = v1alpha1.RescueCleanAndPushed
		rec.Message = fmt.Sprintf("the rescue %s found every clone clean and pushed; %s", rep.Stamp, stopped)
		return rec, string(v1alpha1.RescueCleanAndPushed)
	case refs == 0:
		rec.Result = v1alpha1.RescueFailed
		rec.Message = "the rescue " + rep.Stamp + " found nothing to bundle but did not prove every clone clean and pushed"
		return rec, "NotProven"
	default:
		rec.Result = v1alpha1.RescueVerified
		rec.Message = fmt.Sprintf("the rescue %s bundled %d refs origin lacks into %s; %s", rep.Stamp, refs, rep.Bundle.Dir, stopped)
		return rec, string(v1alpha1.RescueVerified)
	}
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// rescueRanIn reports whether the session's newest rescue ran in this pod, and
// the session has not changed since. Then the pod may go even if the rescue
// failed: D-10 suspends anyway and keeps the volume, and RescueFailed blocks
// archive.
func rescueRanIn(s *v1alpha1.AgentSession, pod *corev1.Pod) bool {
	r := s.Status.Rescue
	return r != nil && r.Result != "" && !r.Superseded && r.PodUID != "" &&
		r.PodUID == string(pod.UID) && r.Generation == s.Generation
}

// rescued reports whether the volume's work is safe off it (D-10): the newest
// rescue verified a bundle that covers every ref origin lacked, or proved there
// was none, and no pod has started on the volume since.
func rescued(s *v1alpha1.AgentSession) bool {
	r := s.Status.Rescue
	return r != nil && !r.Superseded && r.PodUID != "" &&
		(r.Result == v1alpha1.RescueVerified || r.Result == v1alpha1.RescueCleanAndPushed)
}
