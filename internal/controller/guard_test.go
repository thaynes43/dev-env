package controller

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The pods the guard judges, by what runs in them.
var guardPods = map[string]func() *corev1.Pod{
	"unscheduled": func() *corev1.Pod { return &corev1.Pod{} },
	"never started": func() *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{NodeName: "talosw02", Containers: []corev1.Container{{Name: ContainerName}}},
			Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
				Name: ContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}}}
	},
	"scheduled, no status yet": func() *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{NodeName: "talosw02", Containers: []corev1.Container{{Name: ContainerName}}},
			Status: corev1.PodStatus{Phase: corev1.PodPending}}
	},
	"running": func() *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{NodeName: "talosw02", Containers: []corev1.Container{{Name: ContainerName}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
				Name: ContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	},
	"crash looping": func() *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{NodeName: "talosw02", Containers: []corev1.Container{{Name: ContainerName}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
				Name: ContainerName, RestartCount: 3, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}}}}
	},
	// Pending again after a run: the kubelet restarted it.
	"pending after a run": func() *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{NodeName: "talosw02", Containers: []corev1.Container{{Name: ContainerName}}},
			Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
				Name: ContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}}}}
	},
	"evicted": func() *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{NodeName: "talosw02"}, Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}}
	},
	"succeeded": func() *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{NodeName: "talosw02"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	},
}

// An agent may run in these, so only a rescue lets them go.
var livePods = map[string]bool{"scheduled, no status yet": true, "running": true, "crash looping": true, "pending after a run": true}

// The rescue records the guard reads, relative to the pod and the session.
var guardRecords = map[string]func(pod types.UID, gen int64) *v1alpha1.RescueStatus{
	"none": func(types.UID, int64) *v1alpha1.RescueStatus { return nil },
	"verified here": func(pod types.UID, gen int64) *v1alpha1.RescueStatus {
		return &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, PodUID: string(pod), Generation: gen}
	},
	"clean here": func(pod types.UID, gen int64) *v1alpha1.RescueStatus {
		return &v1alpha1.RescueStatus{Result: v1alpha1.RescueCleanAndPushed, PodUID: string(pod), Generation: gen}
	},
	"failed here": func(pod types.UID, gen int64) *v1alpha1.RescueStatus {
		return &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, PodUID: string(pod), Generation: gen}
	},
	"verified here, older generation": func(pod types.UID, gen int64) *v1alpha1.RescueStatus {
		return &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, PodUID: string(pod), Generation: gen - 1}
	},
	"verified in another pod": func(_ types.UID, gen int64) *v1alpha1.RescueStatus {
		return &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, PodUID: "another-pod", Generation: gen}
	},
	"verified here, superseded": func(pod types.UID, gen int64) *v1alpha1.RescueStatus {
		return &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, PodUID: string(pod), Generation: gen, Superseded: true}
	},
	"no result": func(pod types.UID, gen int64) *v1alpha1.RescueStatus {
		return &v1alpha1.RescueStatus{PodUID: string(pod), Generation: gen}
	},
}

// rescuedHere are the records of a rescue that ran in the pod since the session
// last changed, whatever its result: D-10 suspends after a failed rescue too.
var rescuedHere = map[string]bool{"verified here": true, "clean here": true, "failed here": true}

var allPhases = []v1alpha1.SessionPhase{"", v1alpha1.PhasePending, v1alpha1.PhaseRunning, v1alpha1.PhaseIdle,
	v1alpha1.PhaseDraining, v1alpha1.PhaseSuspended, v1alpha1.PhaseArchived, v1alpha1.PhaseFailed}

// TestPodGuardTable is 5.1's pod rule over every phase, operating mode, delete,
// pod state and rescue record: a pod goes only in the Suspended transition, and
// a pod an agent may run in only after a rescue in it at the session's current
// generation. Drain (plan 04) is refused until it is built.
func TestPodGuardTable(t *testing.T) {
	now := metav1.Now()
	allowed := 0
	for _, phase := range allPhases {
		for _, mode := range []v1alpha1.OperatingMode{v1alpha1.OperatingModeRunning, v1alpha1.OperatingModeSuspended} {
			for _, deleted := range []bool{false, true} {
				for podName, mkPod := range guardPods {
					for recName, mkRec := range guardRecords {
						pod := mkPod()
						pod.UID = "this-pod"
						s := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{OperatingMode: mode}, Status: v1alpha1.AgentSessionStatus{Phase: phase}}
						s.Generation = 7
						if deleted {
							s.DeletionTimestamp = &now
						}
						s.Status.Rescue = mkRec(pod.UID, s.Generation)
						want := phase != v1alpha1.PhaseDraining &&
							(mode == v1alpha1.OperatingModeSuspended || deleted) &&
							(!livePods[podName] || rescuedHere[recName])
						err := podRemovalAllowed(s, pod)
						name := fmt.Sprintf("phase %q, %s, deleted %v, pod %s, rescue %s", phase, mode, deleted, podName, recName)
						switch {
						case want && err != nil:
							t.Errorf("%s: refused: %v", name, err)
						case !want && err == nil:
							t.Errorf("%s: allowed a pod delete", name)
						case !want && phase == v1alpha1.PhaseDraining && !errors.Is(err, errDrainNotBuilt):
							t.Errorf("%s: %v, want the drain reason", name, err)
						case !want && phase != v1alpha1.PhaseDraining && (mode == v1alpha1.OperatingModeSuspended || deleted) && !errors.Is(err, errNeedsRescue):
							t.Errorf("%s: %v, want the rescue reason", name, err)
						case !want && phase != v1alpha1.PhaseDraining && mode == v1alpha1.OperatingModeRunning && !deleted && !strings.Contains(err.Error(), "neither draining nor suspending"):
							t.Errorf("%s: %v", name, err)
						}
						if want {
							allowed++
						}
					}
				}
			}
		}
	}
	if allowed == 0 {
		t.Fatal("the table allows nothing, so it proves nothing")
	}
}

// The heart of 5.1, said once more without the table: a pod an agent may run in
// is never deleted while the session wants it, and never without a rescue in
// it since the session last changed.
func TestARunningPodNeedsARescueOfItsOwn(t *testing.T) {
	now := metav1.Now()
	for podName := range livePods {
		for recName, mkRec := range guardRecords {
			pod := guardPods[podName]()
			pod.UID = "this-pod"
			running := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{OperatingMode: v1alpha1.OperatingModeRunning}}
			running.Generation = 3
			running.Status.Rescue = mkRec(pod.UID, running.Generation)
			if err := podRemovalAllowed(running, pod); err == nil {
				t.Errorf("pod %s, rescue %s: deleted the pod of a session that wants it", podName, recName)
			}
			reaped := running.DeepCopy()
			reaped.DeletionTimestamp = &now
			if err := podRemovalAllowed(reaped, pod); (err == nil) != rescuedHere[recName] {
				t.Errorf("pod %s, rescue %s, reaped: %v", podName, recName, err)
			}
		}
	}
}

// TestVolumeGuardTable is the archive rule (D-10, D-51, D-62): a volume goes
// only when its session is deleted, or suspended with its archive timer due; no
// pod of it exists; and the newest rescue, of the volume's last pod, was
// verified or found nothing to save.
func TestVolumeGuardTable(t *testing.T) {
	now := metav1.Now()
	safe := map[string]bool{"verified here": true, "clean here": true, "verified here, older generation": true, "verified in another pod": true}
	allowed := 0
	for _, phase := range allPhases {
		for _, mode := range []v1alpha1.OperatingMode{v1alpha1.OperatingModeRunning, v1alpha1.OperatingModeSuspended} {
			for _, deleted := range []bool{false, true} {
				for _, podExists := range []bool{false, true} {
					for _, due := range []bool{false, true} {
						for _, recorded := range []bool{false, true} {
							for recName, mkRec := range guardRecords {
								s := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{OperatingMode: mode}, Status: v1alpha1.AgentSessionStatus{Phase: phase}}
								s.Generation = 7
								if deleted {
									s.DeletionTimestamp = &now
								}
								if recorded {
									s.Status.ArchivedAt = &now
								}
								s.Status.Rescue = mkRec("last-pod", s.Generation)
								want := (deleted || (mode == v1alpha1.OperatingModeSuspended && due && recorded)) && !podExists && safe[recName]
								err := volumeRemovalAllowed(s, podExists, due)
								if want != (err == nil) {
									t.Errorf("phase %q, %s, deleted %v, pod exists %v, archive due %v, recorded %v, rescue %s: %v (want allowed %v)", phase, mode, deleted, podExists, due, recorded, recName, err, want)
								}
								if want {
									allowed++
								}
							}
						}
					}
				}
			}
		}
	}
	if allowed == 0 {
		t.Fatal("the table allows nothing, so it proves nothing")
	}
}

// guardedCalls are the calls TestOnlyTheGuardDeletes allows, each with the
// one function it may appear in and the guard that function must ask first.
var guardedCalls = []struct {
	call, in, guard string
}{
	{"Delete", "deletePod", "podRemovalAllowed"},
	{"Delete", "deleteVolume", "volumeRemovalAllowed"},
	{"RemoveFinalizer", "releaseSession", ""},
	{"RemoveFinalizer", "releaseVolume", "volumeRemovalAllowed"},
	{"releaseSession", "releaseIfEmpty", ""},
	{"releaseVolume", "deleteVolume", "volumeRemovalAllowed"},
	{"deletePod", "removePod", "podRemovalAllowed"},
	{"deletePod", "removeSharedPod", "podRemovalAllowed"},
	{"deleteVolume", "archive", "volumeRemovalAllowed"},
}

// TestOnlyTheGuardDeletes reads this package's source. A Delete or DeleteAllOf
// call may appear only in deletePod, after podRemovalAllowed, and in
// deleteVolume, after volumeRemovalAllowed; RemoveFinalizer only in
// releaseSession, which only releaseIfEmpty calls, and in releaseVolume, which
// asks volumeRemovalAllowed and which only deleteVolume calls. A new delete path
// cannot slip in without changing this test, which is where review looks (5.1).
func TestOnlyTheGuardDeletes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			first := map[string]token.Pos{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					name = f.Sel.Name
				case *ast.Ident:
					name = f.Name
				}
				if _, ok := first[name]; !ok {
					first[name] = call.Pos()
				}
				at := fset.Position(call.Pos())
				guarded := false
				for _, g := range guardedCalls {
					if g.call != name {
						continue
					}
					guarded = true
					if g.in != fn.Name.Name {
						continue
					}
					seen[g.call+" in "+g.in]++
					if g.guard != "" {
						if pos, ok := first[g.guard]; !ok || pos > call.Pos() {
							t.Errorf("%s: %s calls %s before it asks %s", at, fn.Name.Name, name, g.guard)
						}
					}
					return true
				}
				if guarded || name == "DeleteAllOf" {
					t.Errorf("%s: %s in %s; only the guarded functions may (5.1, D-51)", at, name, fn.Name.Name)
				}
				return true
			})
		}
	}
	// The test must see every call it guards, once, or it proves nothing.
	for _, g := range guardedCalls {
		if n := seen[g.call+" in "+g.in]; n != 1 {
			t.Errorf("%s in %s: seen %d times, want once", g.call, g.in, n)
		}
	}
}

// A hold pod runs no agent (D-55), so the guard judges it by what it is for: it
// goes when the session wants its own pod, when it ended, or once a rescue in it
// made the volume safe. It stays while it starts and while its rescue fails.
func TestHoldPodGuardTable(t *testing.T) {
	now := metav1.Now()
	safeHere := map[string]bool{"verified here": true, "clean here": true}
	ended := map[string]bool{"evicted": true, "succeeded": true}
	notStarted := map[string]bool{"unscheduled": true, "never started": true}
	allowed, kept := 0, 0
	for _, mode := range []v1alpha1.OperatingMode{v1alpha1.OperatingModeRunning, v1alpha1.OperatingModeSuspended} {
		for _, deleted := range []bool{false, true} {
			for podName, mkPod := range guardPods {
				for recName, mkRec := range guardRecords {
					pod := mkPod()
					pod.UID = "this-pod"
					pod.Labels = map[string]string{v1alpha1.LabelHold: "true"}
					s := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{OperatingMode: mode}}
					s.Generation = 7
					if deleted {
						s.DeletionTimestamp = &now
					}
					s.Status.Rescue = mkRec(pod.UID, s.Generation)
					wantsGone := mode == v1alpha1.OperatingModeSuspended || deleted
					want := !wantsGone || ended[podName] || safeHere[recName]
					err := podRemovalAllowed(s, pod)
					name := fmt.Sprintf("%s, deleted %v, pod %s, rescue %s", mode, deleted, podName, recName)
					switch {
					case want && err != nil:
						t.Errorf("%s: refused: %v", name, err)
					case !want && err == nil:
						t.Errorf("%s: let the hold pod go", name)
					case !want && notStarted[podName] && !errors.Is(err, errHoldStarting):
						t.Errorf("%s: %v, want the starting reason", name, err)
					case !want && !notStarted[podName] && !errors.Is(err, errHoldRescuing):
						t.Errorf("%s: %v, want the rescuing reason", name, err)
					}
					if want {
						allowed++
					} else {
						kept++
					}
				}
			}
		}
	}
	if allowed == 0 || kept == 0 {
		t.Fatalf("allowed %d, kept %d: the table proves nothing", allowed, kept)
	}
}

// The hold pod's rescue runs once it has started and the session still wants
// its pod gone, and again every retry while the last one failed (D-55). A
// session pod's rule is unchanged: one rescue since the session last changed.
func TestHoldPodRescueRetry(t *testing.T) {
	now := time.Now()
	deleted := metav1.NewTime(now)
	retry := 15 * time.Minute
	for _, c := range []struct {
		name   string
		hold   bool
		pod    string
		rec    func(types.UID, int64) *v1alpha1.RescueStatus
		ago    time.Duration
		reaped bool
		want   bool
	}{
		{"no rescue yet", true, "running", guardRecords["none"], 0, true, true},
		{"not started", true, "never started", guardRecords["none"], 0, true, false},
		{"ended", true, "evicted", guardRecords["none"], 0, true, false},
		{"failed just now", true, "running", guardRecords["failed here"], time.Minute, true, false},
		{"failed a retry ago", true, "running", guardRecords["failed here"], retry, true, true},
		{"verified", true, "running", guardRecords["verified here"], retry, true, false},
		{"failed in another pod", true, "running", func(_ types.UID, gen int64) *v1alpha1.RescueStatus {
			return &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, PodUID: "an-earlier-pod", Generation: gen}
		}, time.Minute, true, true},
		{"the session wants its pod", true, "running", guardRecords["none"], 0, false, false},
		{"a session pod's failed rescue is not retried", false, "running", guardRecords["failed here"], retry, true, false},
		{"a session pod with no rescue", false, "running", guardRecords["none"], 0, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			pod := guardPods[c.pod]()
			pod.UID = "this-pod"
			if c.hold {
				pod.Labels = map[string]string{v1alpha1.LabelHold: "true"}
			}
			s := &v1alpha1.AgentSession{}
			s.Generation = 4
			if c.reaped {
				s.DeletionTimestamp = &deleted
			}
			s.Status.Rescue = c.rec(pod.UID, s.Generation)
			if s.Status.Rescue != nil {
				at := metav1.NewTime(now.Add(-c.ago))
				s.Status.Rescue.At = &at
			}
			if got := needsRescue(s, pod, now, retry); got != c.want {
				t.Errorf("needsRescue = %v, want %v", got, c.want)
			}
		})
	}
}
