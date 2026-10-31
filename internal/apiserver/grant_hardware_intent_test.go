package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

type hardwareFixture struct {
	f   *fixture
	c   *caller
	g   *v1alpha1.AccessGrant
	req apiv1.HardwareIntentRequest
}

func hardwareCase(t *testing.T, edit func(*v1alpha1.AccessGrant, *v1alpha1.CredentialJob, *v1alpha1.AgentSession, *corev1.Pod)) *hardwareFixture {
	t.Helper()
	now := time.Date(2026, 10, 6, 17, 22, 26, 0, time.UTC)
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "hardware-owner", UID: "11111111-1111-1111-1111-111111111111"}, Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseRunning}}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: s.Name, UID: "22222222-2222-2222-2222-222222222222", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-agent"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	approved, installed, expires := metav1.NewTime(now.Add(-time.Minute)), metav1.NewTime(now.Add(-30*time.Second)), metav1.NewTime(now.Add(59*time.Minute))
	g := &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "grant-hardware-intent", UID: "33333333-3333-3333-3333-333333333333", Annotations: map[string]string{v1alpha1.LabelPrefix + "credential-job-uid": "44444444-4444-4444-4444-444444444444"}},
		Spec:   v1alpha1.AccessGrantSpec{Type: v1alpha1.GrantCredential, Requester: v1alpha1.GrantRequester{Session: s.Name, SessionUID: s.UID}, Credential: &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialHWSSH}, TTL: metav1.Duration{Duration: time.Hour}, Reason: "synthetic hardware maintenance"},
		Status: v1alpha1.AccessGrantStatus{Phase: v1alpha1.GrantActive, ApprovedBy: "policy/c4", ApprovedAt: &approved, ApprovedTTL: &metav1.Duration{Duration: time.Hour}, ExpiresAt: &expires, InstalledAt: &installed, InstalledPodUID: string(p.UID)}}
	j := &v1alpha1.CredentialJob{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: v1alpha1.CredentialJobName(g.UID), UID: "44444444-4444-4444-4444-444444444444", Labels: map[string]string{v1alpha1.LabelGrant: g.Name, v1alpha1.LabelSession: s.Name, v1alpha1.LabelManagedBy: broker.ManagedBy}},
		Spec:   v1alpha1.CredentialJobSpec{Grant: v1alpha1.CredentialObjectReference{Namespace: g.Namespace, Name: g.Name, UID: g.UID}, Session: v1alpha1.CredentialObjectReference{Namespace: s.Namespace, Name: s.Name, UID: s.UID}, Credential: g.Spec.Credential.Name, ExpiresAt: expires},
		Status: v1alpha1.CredentialJobStatus{Phase: v1alpha1.CredentialInstalled, ProviderID: v1alpha1.CredentialProviderID(g.Spec.Credential.Name, g.UID), InstalledAt: &installed, InstalledPodUID: string(p.UID)}}
	if edit != nil {
		edit(g, j, s, p)
	}
	f := newFixture(t, g, j, s, p)
	f.srv.PolicyNamespace = "dev-env-system"
	id := saIdentity(sessionNS + "/dev-env-agent")
	id.PodName, id.PodUID = p.Name, string(p.UID)
	c := &caller{kind: kindSession, identity: id, parent: s.Name, session: s, pod: p}
	return &hardwareFixture{f: f, c: c, g: g, req: apiv1.HardwareIntentRequest{GrantUID: string(g.UID), Digest: grantattest.ScopeDigest(g), Operation: "firmware-update", Host: "host-a.example", Device: "disk-serial-1", Firmware: "version-2", TomRef: "msg-tom-42"}}
}

func (h *hardwareFixture) record(classes []string, request any) (int, any, error) {
	raw, _ := json.Marshal(request)
	r := httptest.NewRequest(http.MethodPost, apiv1.GrantHardwareIntentPath(h.g.Name), bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("name", h.g.Name)
	return h.f.srv.recordHardwareIntent(context.Background(), httptest.NewRecorder(), r, h.c, classes)
}

func TestHardwareIntentRecordsOnlyAuditOnOwnInstalledC4Lease(t *testing.T) {
	for _, credential := range []v1alpha1.CredentialName{v1alpha1.CredentialHWSSH, v1alpha1.CredentialProxmox} {
		t.Run(string(credential), func(t *testing.T) {
			h := hardwareCase(t, func(g *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
				g.Spec.Credential.Name = credential
				j.Spec.Credential = credential
				j.Status.ProviderID = v1alpha1.CredentialProviderID(credential, g.UID)
			})
			before := h.g.Status.DeepCopy()
			status, out, err := h.record([]string{"rd4-hardware"}, h.req)
			if err != nil || status != http.StatusCreated {
				t.Fatalf("record: %d %v", status, err)
			}
			receipt := out.(apiv1.HardwareIntentReceipt)
			if receipt.HardwareIntentRequest != h.req || receipt.SessionUID != string(h.c.session.UID) || receipt.PodUID != h.c.identity.PodUID || !receipt.RecordedAt.Equal(h.f.now) {
				t.Fatal("receipt lost authenticated request correlation", receipt)
			}
			got := h.f.grant(h.g.Name)
			if got.Annotations[hardwareIntentAnnotation] == "" || !apiequality.Semantic.DeepEqual(*before, got.Status) || !apiequality.Semantic.DeepEqual(h.g.Spec, got.Spec) {
				t.Fatal("intent changed credential authority or omitted audit")
			}
			if _, _, err := h.record([]string{"rd4-hardware"}, h.req); err == nil {
				t.Fatal("replayed intent accepted")
			}
			var job v1alpha1.CredentialJob
			if err := h.f.c.Get(context.Background(), types.NamespacedName{Namespace: "dev-env-system", Name: v1alpha1.CredentialJobName(h.g.UID)}, &job); err != nil || job.Spec.Release || job.Status.Phase != v1alpha1.CredentialInstalled {
				t.Fatal("audit changed keeper state", err)
			}
		})
	}
}

func TestHardwareIntentRefusesInvalidLeaseAndIdentityWithoutAudit(t *testing.T) {
	cases := map[string]func(*v1alpha1.AccessGrant, *v1alpha1.CredentialJob, *v1alpha1.AgentSession, *corev1.Pod){
		"released": func(g *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			g.Spec.Release = true
		},
		"ended": func(g *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			g.Status.Phase = v1alpha1.GrantReleased
		},
		"expired": func(g *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			g.Status.ExpiresAt = g.Status.ApprovedAt.DeepCopy()
		},
		"no-policy-approval": func(g *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			g.Status.ApprovedBy = "delegate/dev-env-system/actor"
		},
		"no-install": func(g *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			g.Status.InstalledAt = nil
		},
		"wrong-installed-pod": func(g *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			g.Status.InstalledPodUID = "other"
		},
		"wrong-requester": func(g *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			g.Spec.Requester.SessionUID = "other"
		},
		"no-job-pin": func(g *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			delete(g.Annotations, v1alpha1.LabelPrefix+"credential-job-uid")
		},
		"replaced-job": func(_ *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			j.UID = "other"
		},
		"job-cleanup": func(_ *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			j.Status.Phase = v1alpha1.CredentialCleanupPending
		},
		"job-release": func(_ *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			j.Spec.Release = true
		},
		"job-other-grant": func(_ *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			j.Spec.Grant.UID = "other"
		},
		"job-other-session": func(_ *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			j.Spec.Session.UID = "other"
		},
		"job-expiry": func(_ *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			j.Spec.ExpiresAt = metav1.NewTime(time.Time{})
		},
		"job-provider": func(_ *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, _ *corev1.Pod) {
			j.Status.ProviderID = "other"
		},
		"session-suspended": func(_ *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, s *v1alpha1.AgentSession, _ *corev1.Pod) {
			s.Status.Phase = v1alpha1.PhaseSuspended
		},
		"pod-ended": func(_ *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.Status.Phase = corev1.PodFailed
		},
		"pod-owner": func(_ *v1alpha1.AccessGrant, _ *v1alpha1.CredentialJob, _ *v1alpha1.AgentSession, p *corev1.Pod) {
			p.OwnerReferences = nil
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			h := hardwareCase(t, edit)
			_, _, err := h.record([]string{"rd4-hardware"}, h.req)
			if err == nil {
				t.Fatal("invalid lease accepted")
			}
			if h.f.grant(h.g.Name).Annotations[hardwareIntentAnnotation] != "" {
				t.Fatal("refused intent wrote audit")
			}
		})
	}
	for _, classes := range [][]string{nil, {"rd2"}} {
		h := hardwareCase(t, nil)
		if _, _, err := h.record(classes, h.req); err == nil {
			t.Fatal("disabled hardware intent accepted")
		}
	}
	h := hardwareCase(t, nil)
	h.req.TomRef = ""
	if _, _, err := h.record([]string{"rd4-hardware"}, h.req); err == nil {
		t.Fatal("missing Tom reference accepted")
	}
	h = hardwareCase(t, nil)
	h.req.GrantUID = "55555555-5555-5555-5555-555555555555"
	if _, _, err := h.record([]string{"rd4-hardware"}, h.req); err == nil {
		t.Fatal("wrong grant UID accepted")
	}
	h = hardwareCase(t, nil)
	h.c.kind = kindHuman
	if _, _, err := h.record([]string{"rd4-hardware"}, h.req); err == nil {
		t.Fatal("nonrequester accepted")
	}
}
