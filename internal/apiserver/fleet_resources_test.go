package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestFleetResourcesLegacyDoesNotListPods(t *testing.T) {
	f := newFixture(t)
	addFleetSession(t, f, "run", v1alpha1.PhaseRunning, "status-node", "")
	spy := &podListSpy{Reader: f.c}
	f.srv.Live = spy
	w := f.do(http.MethodGet, apiv1.FleetPath, tokHuman, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if spy.podLists != 0 {
		t.Fatalf("legacy fleet listed pods %d times", spy.podLists)
	}
	if strings.Contains(w.Body.String(), `"resources"`) {
		t.Fatalf("legacy body carries resources: %s", w.Body.String())
	}
	fl := decode[apiv1.Fleet](t, w)
	if fl.Phases["Running"] != 1 || len(fl.Sessions) != 1 || fl.Sessions[0].Node != "status-node" || fl.Sessions[0].Resources != nil {
		t.Fatalf("legacy fleet changed: %+v", fl)
	}
}

func TestFleetResourcesObservedSpec(t *testing.T) {
	f := newFixture(t)
	running := addFleetSession(t, f, "run", v1alpha1.PhaseRunning, "status-node", "")
	pending := addFleetSession(t, f, "pend", v1alpha1.PhasePending, "", "0/9 nodes are available: 3 Insufficient cpu.")
	failed := addFleetSession(t, f, "ended", v1alpha1.PhaseFailed, "status-node", "")
	failed.Status.Agent = &v1alpha1.AgentStatus{Status: "exited", Task: &v1alpha1.TaskStatus{ExitCode: 0, Subtype: "success", NumTurns: 2, FinishedAt: metav1.NewTime(f.now)}}
	if err := f.c.Status().Update(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	suspended := addFleetSession(t, f, "paused", v1alpha1.PhaseSuspended, "", "")
	cpu := resource.MustParse("100m")
	zero := resource.MustParse("0")
	mem := resource.MustParse("1Gi")
	side := resource.MustParse("256Mi")
	initMem := resource.MustParse("64Mi")
	hidden := resource.MustParse("999m")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: running.Name, Namespace: running.Namespace, UID: "pod-run",
			Labels:          map[string]string{"dev-env.haynesops.com/session": running.Name},
			OwnerReferences: []metav1.OwnerReference{fleetOwner(running)},
		},
		Spec: corev1.PodSpec{
			NodeName: "spec-node",
			Containers: []corev1.Container{
				{Name: "agent", Command: []string{"print-secret-command"}, Env: []corev1.EnvVar{{Name: "TOKEN", Value: "fleet-probe-secret-name"}},
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: mem}, Limits: corev1.ResourceList{corev1.ResourceCPU: zero}}},
				{Name: "sidecar", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: side}}},
			},
			InitContainers:      []corev1.Container{{Name: "bootstrap", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: initMem}}}},
			EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: hidden}}}}},
			Volumes:             []corev1.Volume{{Name: "secret", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "fleet-probe-secret-name"}}}},
		},
	}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	unscheduled := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: pending.Name, Namespace: pending.Namespace, UID: "pod-pend", OwnerReferences: []metav1.OwnerReference{fleetOwner(pending)}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: cpu}}}}},
	}
	if err := f.c.Create(context.Background(), unscheduled); err != nil {
		t.Fatal(err)
	}
	spy := &podListSpy{Reader: f.c}
	f.srv.Live = spy
	w := f.doHeader(http.MethodGet, apiv1.FleetPath, tokHuman, nil, http.Header{apiv1.FleetHeader: []string{apiv1.FleetResourcesV1}})
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, hiddenText := range []string{"fleet-probe-secret-name", "print-secret-command", "999m"} {
		if strings.Contains(body, hiddenText) {
			t.Fatalf("response exposes %s", hiddenText)
		}
	}
	if spy.podLists != 1 || spy.namespace != sessionNS || spy.selector != "" {
		t.Fatalf("pod list calls=%d namespace=%s selector=%q", spy.podLists, spy.namespace, spy.selector)
	}
	if spy.deadline < 9*time.Second || spy.deadline > 10*time.Second {
		t.Fatalf("pod list deadline %s", spy.deadline)
	}
	fl := decode[apiv1.Fleet](t, w)
	if fl.Phases["Running"] != 1 || fl.Phases["Pending"] != 1 || fl.Phases["Failed"] != 1 || fl.Phases["Suspended"] != 1 {
		t.Fatalf("phases %v", fl.Phases)
	}
	if len(fl.Sessions) != 3 || fl.Sessions[0].Name != "ended" || fl.Sessions[1].Name != "pend" || fl.Sessions[2].Name != "run" {
		t.Fatalf("order %+v", fl.Sessions)
	}
	if fl.Sessions[1].Pending != "0/9 nodes are available: 3 Insufficient cpu." || fl.Sessions[1].Node != "" {
		t.Fatalf("pending row changed: %+v", fl.Sessions[1])
	}
	pend := fl.Sessions[1].Resources
	if pend == nil || pend.State != apiv1.FleetResourcesObserved || pend.PodUID != "pod-pend" || pend.Node == nil || pend.Node.State != apiv1.FleetNodeUnscheduled {
		t.Fatalf("unscheduled observation %+v", pend)
	}
	if fl.Sessions[0].TaskResult == nil || fl.Sessions[0].TaskResult.State != apiv1.TaskSucceeded || fl.Sessions[0].Phase != "Failed" {
		t.Fatalf("task result changed: %+v", fl.Sessions[0])
	}
	if fl.Sessions[0].Resources == nil || fl.Sessions[0].Resources.Reason != apiv1.FleetResourcesMissing {
		t.Fatalf("missing pod: %+v", fl.Sessions[0].Resources)
	}
	got := fl.Sessions[2].Resources
	if got == nil || got.State != apiv1.FleetResourcesObserved || got.PodUID != "pod-run" || got.ObservedAt == nil || !got.ObservedAt.Equal(f.now) {
		t.Fatalf("running observation %+v", got)
	}
	if fl.Sessions[2].Node != "status-node" || got.Node == nil || got.Node.State != apiv1.FleetNodeSet || got.Node.Name != "spec-node" {
		t.Fatalf("node identity status=%s spec=%+v", fl.Sessions[2].Node, got.Node)
	}
	if len(got.Containers) != 2 || got.Containers[0].Name != "agent" || got.Containers[0].Kind != apiv1.FleetContainerOrdinary || got.Containers[1].Name != "sidecar" {
		t.Fatalf("containers %+v", got.Containers)
	}
	agent := got.Containers[0]
	if agent.CPURequest != (apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "100m"}) || agent.CPULimit != (apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "0"}) || agent.MemoryRequest != (apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: "1Gi"}) || agent.MemoryLimit.State != apiv1.FleetQuantityAbsent || agent.MemoryLimit.Value != "" {
		t.Fatalf("agent quantities %+v", agent)
	}
	if len(got.InitContainers) != 1 || got.InitContainers[0].Kind != apiv1.FleetContainerInit || got.InitContainers[0].Name != "bootstrap" || got.InitContainers[0].MemoryRequest.Value != "64Mi" {
		t.Fatalf("init %+v", got.InitContainers)
	}
	var again corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "run"}, &again); err != nil {
		t.Fatal(err)
	}
	if again.Spec.NodeName != "spec-node" || again.Spec.Containers[0].Resources.Requests.Cpu().String() != "100m" {
		t.Fatalf("pod was written: %+v", again.Spec)
	}
	if suspended.Status.Phase != v1alpha1.PhaseSuspended {
		t.Fatal("suspended fixture changed")
	}
}

func TestFleetResourcesBindingRefusals(t *testing.T) {
	now := time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)
	session := &v1alpha1.AgentSession{}
	session.Name = "run"
	session.Namespace = sessionNS
	session.UID = "session-uid"
	owned := fleetPod("run", sessionNS, "pod-new", "777m", fleetOwner(session))
	cases := []struct {
		name   string
		pods   []corev1.Pod
		reason string
	}{
		{name: "missing", reason: apiv1.FleetResourcesMissing},
		{name: "foreign owner", pods: []corev1.Pod{fleetPod("run", sessionNS, "pod-foreign", "456m", metav1.OwnerReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: session.Name, UID: "other-uid"})}, reason: apiv1.FleetResourcesForeign},
		{name: "label without owner", pods: []corev1.Pod{labeledPod("run", sessionNS, "pod-label", "321m")}, reason: apiv1.FleetResourcesForeign},
		{name: "other namespace", pods: []corev1.Pod{fleetPod("run", "other", "pod-ns", "654m", fleetOwner(session))}, reason: apiv1.FleetResourcesForeign},
		{name: "ambiguous", pods: []corev1.Pod{fleetPod("run", sessionNS, "pod-a", "111m", fleetOwner(session)), fleetPod("run", sessionNS, "pod-b", "222m", fleetOwner(session))}, reason: apiv1.FleetResourcesAmbiguous},
		{name: "nil controller", pods: []corev1.Pod{fleetPod("run", sessionNS, "pod-nil", "333m", ownerWithoutController(session, nil))}, reason: apiv1.FleetResourcesForeign},
		{name: "controller false", pods: []corev1.Pod{fleetPod("run", sessionNS, "pod-false", "444m", ownerWithoutController(session, boolPtr(false)))}, reason: apiv1.FleetResourcesForeign},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bindFleetResources(now, sessionNS, []v1alpha1.AgentSession{*session}, tc.pods)[string(session.UID)]
			if got == nil || got.State != apiv1.FleetResourcesUnknown || got.Reason != tc.reason || len(got.Containers) != 0 {
				t.Fatalf("%s: %+v", tc.name, got)
			}
		})
	}
	session.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{SessionUID: string(session.UID), LastExecutor: &v1alpha1.PrivateExecutorStatus{PodUID: "pod-original", PodName: session.Name, Generation: 1}}
	replaced := bindFleetResources(now, sessionNS, []v1alpha1.AgentSession{*session}, []corev1.Pod{owned})[string(session.UID)]
	if replaced == nil || replaced.Reason != apiv1.FleetResourcesReplaced || strings.Contains(mustJSON(t, replaced), "777m") {
		t.Fatalf("replaced: %+v", replaced)
	}
	matching := owned
	matching.UID = "pod-original"
	observed := bindFleetResources(now, sessionNS, []v1alpha1.AgentSession{*session}, []corev1.Pod{matching})[string(session.UID)]
	if observed == nil || observed.State != apiv1.FleetResourcesObserved || observed.PodUID != "pod-original" {
		t.Fatalf("matching executor: %+v", observed)
	}
}

func TestFleetResourcesOversizedIsIncomplete(t *testing.T) {
	now := time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)
	session := &v1alpha1.AgentSession{}
	session.Name, session.Namespace, session.UID = "wide", sessionNS, "wide-uid"
	pod := fleetPod(session.Name, sessionNS, "pod-wide", "100m", fleetOwner(session))
	pod.Spec.Containers = make([]corev1.Container, fleetResourcesMaxContainers+1)
	for i := range pod.Spec.Containers {
		pod.Spec.Containers[i].Name = "c"
	}
	got := bindFleetResources(now, sessionNS, []v1alpha1.AgentSession{*session}, []corev1.Pod{pod})[string(session.UID)]
	if got == nil || got.Reason != apiv1.FleetResourcesOversized || len(got.Containers) != 0 {
		t.Fatalf("per-pod cap: %+v", got)
	}
	small := fleetPod("tiny", sessionNS, "pod-tiny", "100m", fleetOwner(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "tiny", Namespace: sessionNS, UID: "tiny-uid"}}))
	prev := fleetResourcesMaxBytes
	fleetResourcesMaxBytes = 1
	t.Cleanup(func() { fleetResourcesMaxBytes = prev })
	tinySession := &v1alpha1.AgentSession{}
	tinySession.Name, tinySession.Namespace, tinySession.UID = "tiny", sessionNS, "tiny-uid"
	capped := bindFleetResources(now, sessionNS, []v1alpha1.AgentSession{*tinySession}, []corev1.Pod{small})[string(tinySession.UID)]
	if capped == nil || capped.Reason != apiv1.FleetResourcesOversized || len(capped.Containers) != 0 {
		t.Fatalf("aggregate cap: %+v", capped)
	}
}

func TestFleetResourcesReadFailureAndCancel(t *testing.T) {
	f := newFixture(t)
	addFleetSession(t, f, "run", v1alpha1.PhaseRunning, "status-node", "")
	f.srv.Live = &podListSpy{Reader: f.c, err: errors.New("pod list refused")}
	w := f.doHeader(http.MethodGet, apiv1.FleetPath, tokHuman, nil, http.Header{apiv1.FleetHeader: []string{apiv1.FleetResourcesV1}})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"state":"Set"`) || strings.Contains(w.Body.String(), `"state":"Observed"`) {
		t.Fatalf("read failure looked complete: %d %s", w.Code, w.Body.String())
	}
	fl := decode[apiv1.Fleet](t, w)
	if fl.Phases["Running"] != 1 || fl.Sessions[0].Resources == nil || fl.Sessions[0].Resources.Reason != apiv1.FleetResourcesUnreadable {
		t.Fatalf("unreadable observation %+v", fl.Sessions)
	}
	f.srv.Live = cancelReader{}
	w = f.doHeader(http.MethodGet, apiv1.FleetPath, tokHuman, nil, http.Header{apiv1.FleetHeader: []string{apiv1.FleetResourcesV1}})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"state":"Set"`) || strings.Contains(w.Body.String(), `"state":"Observed"`) {
		t.Fatalf("reader cancel while the caller is active failed the fleet: %d %s", w.Code, w.Body.String())
	}
	fl = decode[apiv1.Fleet](t, w)
	if fl.Phases["Running"] != 1 || fl.Sessions[0].Node != "status-node" || fl.Sessions[0].Resources == nil || fl.Sessions[0].Resources.Reason != apiv1.FleetResourcesUnreadable {
		t.Fatalf("reader cancel observation %+v", fl)
	}
}

func TestFleetResourcesCallerCancelAborts(t *testing.T) {
	f := newFixture(t)
	addFleetSession(t, f, "run", v1alpha1.PhaseRunning, "status-node", "0/9 nodes are available: 3 Insufficient cpu.")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, apiv1.FleetPath, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+tokHuman)
	req.Header.Set(apiv1.FleetHeader, apiv1.FleetResourcesV1)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), `"state":"Observed"`) || strings.Contains(w.Body.String(), `"state":"Set"`) {
		t.Fatalf("caller cancel returned a fleet: %d %s", w.Code, w.Body.String())
	}
}

func TestFleetResourcesLocalDeadlineIsUnknown(t *testing.T) {
	f := newFixture(t)
	addFleetSession(t, f, "run", v1alpha1.PhaseRunning, "status-node", "")
	prev := fleetPodListTimeout
	fleetPodListTimeout = 20 * time.Millisecond
	t.Cleanup(func() { fleetPodListTimeout = prev })
	f.srv.Live = deadlineReader{Reader: f.c}
	start := time.Now()
	w := f.doHeader(http.MethodGet, apiv1.FleetPath, tokHuman, nil, http.Header{apiv1.FleetHeader: []string{apiv1.FleetResourcesV1}})
	if time.Since(start) > time.Second {
		t.Fatalf("local deadline waited %s", time.Since(start))
	}
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"state":"Observed"`) {
		t.Fatalf("local deadline failed the fleet: %d %s", w.Code, w.Body.String())
	}
	fl := decode[apiv1.Fleet](t, w)
	if fl.Phases["Running"] != 1 || fl.Revision == "" || fl.Sessions[0].Resources == nil || fl.Sessions[0].Resources.Reason != apiv1.FleetResourcesUnreadable {
		t.Fatalf("deadline observation %+v", fl)
	}
}

func TestFleetResourcesWrongHeaderSkipsPods(t *testing.T) {
	f := newFixture(t)
	addFleetSession(t, f, "run", v1alpha1.PhaseRunning, "status-node", "")
	spy := &podListSpy{Reader: f.c}
	f.srv.Live = spy
	w := f.doHeader(http.MethodGet, apiv1.FleetPath, tokHuman, nil, http.Header{apiv1.FleetHeader: []string{"resources-v2"}})
	if w.Code != http.StatusOK || spy.podLists != 0 || strings.Contains(w.Body.String(), `"resources"`) {
		t.Fatalf("wrong header listed pods or added resources: lists=%d body=%s", spy.podLists, w.Body.String())
	}
}

func addFleetSession(t *testing.T, f *fixture, name string, phase v1alpha1.SessionPhase, node, pending string) *v1alpha1.AgentSession {
	t.Helper()
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: types.UID("uid-" + name)}, Spec: v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"}}
	if err := f.c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if s.UID == "" {
		t.Fatal("session uid was not kept")
	}
	s.Status.Phase = phase
	s.Status.NodeName = node
	s.Status.PendingReason = pending
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if s.UID == "" {
		t.Fatal("status update cleared the session uid")
	}
	return s
}

func fleetOwner(s *v1alpha1.AgentSession) metav1.OwnerReference {
	return ownerWithoutController(s, boolPtr(true))
}

func ownerWithoutController(s *v1alpha1.AgentSession, controller *bool) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: s.Name, UID: s.UID, Controller: controller}
}

func boolPtr(v bool) *bool { return &v }

func fleetPod(name, namespace, uid, cpu string, owner metav1.OwnerReference) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid), OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}}}}},
	}
}

func labeledPod(name, namespace, uid, cpu string) corev1.Pod {
	pod := fleetPod(name, namespace, uid, cpu, metav1.OwnerReference{})
	pod.OwnerReferences = nil
	pod.Labels = map[string]string{"dev-env.haynesops.com/session": name}
	return pod
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type podListSpy struct {
	client.Reader
	podLists  int
	namespace string
	selector  string
	deadline  time.Duration
	err       error
}

func (p *podListSpy) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*corev1.PodList); ok {
		p.podLists++
		var parsed client.ListOptions
		parsed.ApplyOptions(opts)
		p.namespace = parsed.Namespace
		if parsed.LabelSelector != nil {
			p.selector = parsed.LabelSelector.String()
		}
		if deadline, ok := ctx.Deadline(); ok {
			p.deadline = time.Until(deadline)
		}
		if p.err != nil {
			return p.err
		}
	}
	return p.Reader.List(ctx, list, opts...)
}

type cancelReader struct{}

func (cancelReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return context.Canceled
}

func (cancelReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return context.Canceled
}

type deadlineReader struct {
	client.Reader
}

func (d deadlineReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*corev1.PodList); ok {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("pod list did not observe the deadline")
		}
	}
	return d.Reader.List(ctx, list, opts...)
}

func TestFleetQuantityCanonicalizesCachedSpellings(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{in: "+1", want: "1"},
		{in: "1E3", want: "1e3"},
		{in: "1e+3", want: "1e3"},
		{in: "1e03", want: "1e3"},
		{in: "0", want: "0"},
		{in: "100m", want: "100m"},
		{in: "1Gi", want: "1Gi"},
		{in: "64Mi", want: "64Mi"},
	} {
		got := observeQuantity(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(tc.in)}, corev1.ResourceCPU)
		if got.State != apiv1.FleetQuantitySet || got.Value != tc.want {
			t.Errorf("%s: %+v, want %s", tc.in, got, tc.want)
		}
	}
	absent := observeQuantity(corev1.ResourceList{}, corev1.ResourceMemory)
	if absent.State != apiv1.FleetQuantityAbsent || absent.Value != "" {
		t.Fatalf("unset quantity: %+v", absent)
	}
}
