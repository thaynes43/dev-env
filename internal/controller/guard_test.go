package controller

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

func TestPrivateHistoryPodDeletionBoundaryRetainsUncertainIdentity(t *testing.T) {
	for _, state := range []string{"recorded", "partial", "unknown-version", "canary-missing-history"} {
		t.Run(state, func(t *testing.T) {
			s, _ := privateHistoryFixture()
			switch state {
			case "partial":
				s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{StopState: "Unknown"}
			case "unknown-version":
				s.Status.PrivateHistory.Version = 99
			case "canary-missing-history":
				s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{}
				s.Status.PrivateHistory = nil
			}
			pod := guardPods["succeeded"]()
			pod.Name, pod.Namespace, pod.UID = s.Name, s.Namespace, "original-executor"
			pod.OwnerReferences = []metav1.OwnerReference{ownerRef(s)}
			if err := podRemovalAllowed(s, pod); !errors.Is(err, errPrivateHistoryRetained) {
				t.Fatalf("private removal boundary: %v", err)
			}
			c := privateHistoryClient(s, pod)
			if err := deletePod(context.Background(), c, s, pod); !errors.Is(err, errPrivateHistoryRetained) {
				t.Fatalf("direct deletion bypassed private retention: %v", err)
			}
			var retained corev1.Pod
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &retained); err != nil || retained.UID != pod.UID {
				t.Fatalf("original Pod was lost: %v", err)
			}
		})
	}
}

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
	{"detachPrivateHome", "retainSharedPrivateHome", "sharedPodsAbsent"},
	{"releaseRetainedSharedSession", "retainSharedPrivateHome", "confirmedRetention"},
	{"releaseNeverStartedSharedSession", "retainSharedPrivateHome", "sharedAdmissionIs"},
	// The HOME expiry's exceptions (DESIGN-025 11, ADR-005, ADR-006): the
	// ended executor of a private Session in Rescuing, and the expiry's own
	// hold pod, each deleted only after its own guard, by one caller.
	{"Delete", "deleteExpiryExecutor", "expiryPodRemovalAllowed"},
	{"Delete", "deleteExpiryHold", "expiryHoldRemovalAllowed"},
	{"deleteExpiryExecutor", "removeExpiredExecutor", "expiryPodRemovalAllowed"},
	{"deleteExpiryHold", "removeExpiryHold", "expiryHoldRemovalAllowed"},
}

// TestOnlyTheGuardDeletes reads this package's source. A Delete or DeleteAllOf
// call may appear only in deletePod, after podRemovalAllowed, in
// deleteVolume, after volumeRemovalAllowed, and in the HOME expiry's
// deleteExpiryExecutor and deleteExpiryHold, after expiryPodRemovalAllowed and
// expiryHoldRemovalAllowed (DESIGN-025 11), and each Delete names its object by
// UID (client.Preconditions), so a same-named replacement never goes in its
// place; RemoveFinalizer only in releaseSession, which only releaseIfEmpty
// calls, and in releaseVolume, which asks volumeRemovalAllowed and which only
// deleteVolume calls. Any other write of a finalizer or owner reference list (a
// JSON or merge patch naming finalizers or ownerReferences, SetFinalizers,
// SetOwnerReferences and their kin, an assignment to or an in-place slices
// edit of .Finalizers or .OwnerReferences, a use of a package-level constant or
// variable that names them, an assignment to .ObjectMeta, and a Patch of, or an
// assignment through *x of, an object built in the function from a composite
// literal) may appear only in the releaseWriters, whose one caller and guard
// guardedCalls pins; and no whole-object Update may appear at all, because it
// can drop both lists. A new delete or release path cannot slip in without
// changing this test, which is where review looks (5.1). The scan reads syntax,
// not types: it catches the ways TestGuardScanCatchesReleaseWrites lists, not
// every conceivable one, and that test names the known gaps.
func TestOnlyTheGuardDeletes(t *testing.T) {
	scan := scanGuards(t, ".")
	for _, v := range scan.violations {
		t.Error(v)
	}
	// The test must see every call it guards, once, and every release writer
	// write, or it proves nothing.
	for _, g := range guardedCalls {
		if n := scan.seen[g.call+" in "+g.in]; n != 1 {
			t.Errorf("%s in %s: seen %d times, want once", g.call, g.in, n)
		}
	}
	for fn := range releaseWriters {
		if scan.seen["release in "+fn] == 0 {
			t.Errorf("%s writes no finalizer or owner reference; drop it from releaseWriters", fn)
		}
	}
}

// releaseWriters are the functions that may write a finalizer or owner
// reference list by other means than RemoveFinalizer. Each releases one
// retained object by a JSON patch that tests its UID and resourceVersion, and
// guardedCalls pins the one caller of each and the guard it asks first.
var releaseWriters = map[string]bool{
	"detachPrivateHome":                true,
	"releaseRetainedSharedSession":     true,
	"releaseNeverStartedSharedSession": true,
}

// releaseFields are the metadata lists whose loss lets an object go: the
// finalizers that hold it, and the owner references the garbage collector
// follows.
var releaseFields = map[string]bool{"Finalizers": true, "OwnerReferences": true}

// releaseSetters write a finalizer or owner reference list wholesale.
var releaseSetters = map[string]bool{
	"SetFinalizers": true, "SetOwnerReferences": true, "RemoveOwnerReference": true,
	"SetOwnerReference": true, "SetControllerReference": true,
}

// sliceEditors are the slices functions that change a slice in place.
var sliceEditors = map[string]bool{
	"Delete": true, "DeleteFunc": true, "Replace": true, "Insert": true, "Compact": true, "CompactFunc": true,
	"Reverse": true, "Sort": true, "SortFunc": true, "SortStableFunc": true,
}

// TestGuardScanCatchesReleaseWrites runs the scan over testdata/guardscan, a
// file that is never compiled, where each function deletes or releases outside
// the guards in a different way. The scan must name every one of them, or
// TestOnlyTheGuardDeletes passing proves less than it claims.
//
// Known gaps, because the scan reads syntax and does not follow values: an
// object built in another function and then patched or assigned through *x
// here (the refresh pattern `var fresh T; Get(..., &fresh); *s = fresh` is
// allowed on purpose, since fresh then holds the stored finalizers and owner
// references); a patch text assembled so that no one string names finalizers
// or ownerReferences; and a release write in another package. Review still
// covers those.
func TestGuardScanCatchesReleaseWrites(t *testing.T) {
	scan := scanGuards(t, filepath.Join("testdata", "guardscan"))
	for _, fn := range []string{
		"rawFinalizerReplace", "rawFinalizerRemove", "rawOwnerReplace", "mergeFinalizerNull", "mapPatchFinalizers",
		"setFinalizersThenUpdate", "setOwnersThenPatch", "assignFinalizersThenPatch", "assignNestedOwnersThenPatch",
		"deleteFuncInPlace", "removeOwnerThenPatch", "wholeObjectUpdate", "unguardedDelete", "unguardedRemoveFinalizer",
		"deletePod", "pkgConstPatch", "freshObjectMergePatch", "wholeMetaAssignThenPatch", "derefAssignThenPatch",
		"pkgVarFuncLiteral",
	} {
		if !slices.ContainsFunc(scan.violations, func(v string) bool { return strings.Contains(v, " in "+fn+";") }) {
			t.Errorf("the guard scan does not catch %s", fn)
		}
	}
}

// guardScan is what scanGuards found: one message per violation, and how often
// it saw each allowed guarded call.
type guardScan struct {
	violations []string
	seen       map[string]int
}

// scanGuards parses the non-test Go files in dir and checks every call against
// guardedCalls. It scans function bodies and the values of package-level
// declarations. A package-level constant or variable whose value names
// finalizers or ownerReferences (directly, or through another such name) is a
// release name, and a function that uses one writes those lists as surely as one
// that spells the patch out.
func scanGuards(t *testing.T, dir string) guardScan {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	fset := token.NewFileSet()
	scan := guardScan{seen: map[string]int{}}
	report := func(pos token.Pos, fn, format string, args ...any) {
		scan.violations = append(scan.violations, fmt.Sprintf("%s: in %s; ", fset.Position(pos), fn)+fmt.Sprintf(format, args...))
	}
	var parsed []*ast.File
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
		parsed = append(parsed, file)
	}
	releaseNames := packageReleaseNames(parsed)
	for _, file := range parsed {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body != nil {
					scanBody(&scan, report, releaseNames, d.Name.Name, d.Body)
				}
			case *ast.GenDecl:
				// A package-level value runs no code by itself, but a function
				// literal in one does: scan it under the declared name.
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
						for _, v := range vs.Values {
							ast.Inspect(v, func(n ast.Node) bool {
								if lit, ok := n.(*ast.FuncLit); ok {
									scanBody(&scan, report, releaseNames, vs.Names[0].Name, lit.Body)
									return false
								}
								return true
							})
						}
					}
				}
			}
		}
	}
	return scan
}

// packageReleaseNames are the package-level constants and variables whose
// value names finalizers or ownerReferences, directly or through another of
// them.
func packageReleaseNames(files []*ast.File) map[string]bool {
	var specs []*ast.ValueSpec
	for _, file := range files {
		for _, decl := range file.Decls {
			if d, ok := decl.(*ast.GenDecl); ok {
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						specs = append(specs, vs)
					}
				}
			}
		}
	}
	names := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, vs := range specs {
			if len(vs.Names) == 0 || names[vs.Names[0].Name] {
				continue
			}
			found := false
			for _, v := range vs.Values {
				ast.Inspect(v, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.BasicLit:
						found = found || namesReleaseField(n)
					case *ast.Ident:
						found = found || names[n.Name]
					}
					return !found
				})
			}
			if found {
				for _, name := range vs.Names {
					names[name.Name] = true
				}
				changed = true
			}
		}
	}
	return names
}

// namesReleaseField reports whether lit is a string that names a finalizer or
// owner reference list, as a JSON or merge patch does.
func namesReleaseField(lit *ast.BasicLit) bool {
	return lit.Kind == token.STRING && (strings.Contains(lit.Value, "finalizers") || strings.Contains(lit.Value, "ownerReferences"))
}

// scanBody checks one function body, named fn, against guardedCalls and the
// release rules.
func scanBody(scan *guardScan, report func(token.Pos, string, string, ...any), releaseNames map[string]bool, fn string, body *ast.BlockStmt) {
	first := map[string]token.Pos{}
	// fresh are the local variables last set from a composite literal: an
	// object built here, not read from the API server, so it has none of the
	// stored object's finalizers and owner references.
	fresh := map[string]bool{}
	selectors := map[*ast.Ident]bool{}
	release := func(pos token.Pos, what string) {
		if releaseWriters[fn] {
			scan.seen["release in "+fn]++
			return
		}
		report(pos, fn, "%s writes a finalizer or owner reference list; only the releaseWriters may (5.1, D-45)", what)
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			selectors[n.Sel] = true
		case *ast.Ident:
			if releaseNames[n.Name] && !selectors[n] {
				release(n.Pos(), "package-level "+n.Name+", which names finalizers or ownerReferences,")
			}
		case *ast.BasicLit:
			if namesReleaseField(n) {
				release(n.Pos(), "a patch naming "+n.Value)
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				fresh[name.Name] = i < len(n.Values) && compositeLiteral(n.Values[i])
			}
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if field := releaseField(lhs); field != "" {
					release(lhs.Pos(), "an assignment to ."+field)
				}
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "ObjectMeta" {
					release(lhs.Pos(), "an assignment to .ObjectMeta")
				}
				var rhs ast.Expr
				if len(n.Rhs) == len(n.Lhs) {
					rhs = n.Rhs[i]
				}
				if star, ok := lhs.(*ast.StarExpr); ok && rhs != nil && freshObject(rhs, fresh) {
					release(star.Pos(), "an assignment through *x of an object built here")
				}
				if id, ok := lhs.(*ast.Ident); ok {
					fresh[id.Name] = rhs != nil && compositeLiteral(rhs)
				}
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call)
		if releaseSetters[name] {
			release(call.Pos(), name)
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sliceEditors[name] && isIdent(sel.X, "slices") && len(call.Args) > 0 {
			if field := releaseField(call.Args[0]); field != "" {
				release(call.Pos(), "slices."+name+" on ."+field)
			}
		}
		if name == "Update" && !onSubresource(call) {
			report(call.Pos(), fn, "a whole-object Update can drop finalizers and owner references; patch through a guarded function instead")
		}
		if name == "Patch" && !onSubresource(call) && len(call.Args) > 1 && freshObject(call.Args[1], fresh) {
			release(call.Pos(), "a Patch of an object built here, which a merge patch empties of what it lacks,")
		}
		if _, ok := first[name]; !ok {
			first[name] = call.Pos()
		}
		guarded := false
		for _, g := range guardedCalls {
			if g.call != name {
				continue
			}
			guarded = true
			if g.in != fn {
				continue
			}
			scan.seen[g.call+" in "+g.in]++
			if g.guard != "" {
				if pos, ok := first[g.guard]; !ok || pos > call.Pos() {
					report(call.Pos(), fn, "%s is called before %s is asked", name, g.guard)
				}
			}
			if name == "Delete" && !hasUIDPrecondition(call) {
				report(call.Pos(), fn, "Delete names its object by name only; pass client.Preconditions{UID: ...}")
			}
			return true
		}
		if guarded || name == "DeleteAllOf" {
			report(call.Pos(), fn, "%s outside the guarded functions (5.1, D-51)", name)
		}
		return true
	})
}

// compositeLiteral reports whether e is T{...} or &T{...}.
func compositeLiteral(e ast.Expr) bool {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.UnaryExpr:
			if x.Op != token.AND {
				return false
			}
			e = x.X
		case *ast.CompositeLit:
			return true
		default:
			return false
		}
	}
}

// freshObject reports whether e is an object built in this function: a
// composite literal, or a local variable last set from one, through any & or *.
func freshObject(e ast.Expr, fresh map[string]bool) bool {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.UnaryExpr:
			if x.Op != token.AND {
				return false
			}
			e = x.X
		case *ast.CompositeLit:
			return true
		case *ast.Ident:
			return fresh[x.Name]
		default:
			return false
		}
	}
}

// releaseField is "Finalizers" or "OwnerReferences" when e is that field of
// some object, or an element of it, and "" otherwise.
func releaseField(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			if releaseFields[x.Sel.Name] {
				return x.Sel.Name
			}
			return ""
		case *ast.IndexExpr:
			e = x.X
		case *ast.SliceExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return ""
		}
	}
}

// onSubresource reports whether call is X.Status().Update(...) or
// X.SubResource(...).Update(...), which cannot touch metadata.
func onSubresource(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	inner, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	name := calleeName(inner)
	return name == "Status" || name == "SubResource"
}

// hasUIDPrecondition reports whether a Delete call passes a
// client.Preconditions literal that sets UID.
func hasUIDPrecondition(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.CompositeLit)
		if !ok {
			continue
		}
		var typ string
		switch ty := lit.Type.(type) {
		case *ast.SelectorExpr:
			typ = ty.Sel.Name
		case *ast.Ident:
			typ = ty.Name
		}
		if typ != "Preconditions" {
			continue
		}
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok && isIdent(kv.Key, "UID") {
				return true
			}
		}
	}
	return false
}

// isIdent reports whether e is the identifier name.
func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// calleeName is the name a call is made by: the method or the function.
func calleeName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
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
