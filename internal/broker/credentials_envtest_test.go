package broker

import (
	"context"
	"fmt"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestBrokerCredentialJobWatch(t *testing.T) {
	suite(t)
	r := startBroker(t, nil, nil, func(o *Options) { o.EnableProxmoxGrants = true })
	s, pod := newSession(t)
	newPolicy(t, name("pve-policy"), func(p *v1alpha1.GrantPolicy) {
		p.Spec.Type = v1alpha1.GrantCredential
		p.Spec.Kube = nil
		p.Spec.Credential = &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialProxmox}}
	})
	n := newGrant(t, s, func(g *v1alpha1.AccessGrant) {
		g.Spec.Type, g.Spec.Kube = v1alpha1.GrantCredential, nil
		g.Spec.Credential = &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialProxmox}
	})
	g := waitPhase(t, n, v1alpha1.GrantActive)
	var j v1alpha1.CredentialJob
	key := types.NamespacedName{Namespace: systemNS, Name: v1alpha1.CredentialJobName(g.UID)}
	eventually(t, "credential execution job", func() (bool, string) {
		if err := admin.Get(context.Background(), key, &j); err != nil {
			return false, err.Error()
		}
		return grant(t, n).Annotations[credentialJobUIDAnnotation] == string(j.UID), "job UID not yet pinned"
	})
	now := metav1.Now()
	r.clock.SetTime(now.Time)
	j.Status = v1alpha1.CredentialJobStatus{Phase: v1alpha1.CredentialInstalled, ProviderID: "dev-env@pve!grant-" + string(g.UID), InstalledPodUID: string(pod.UID), InstalledAt: &now}
	if err := admin.Status().Update(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	changedAt := time.Now()
	eventually(t, "installed job receipt", func() (bool, string) {
		g = grant(t, n)
		return g.Status.InstalledPodUID == string(pod.UID), fmt.Sprintf("%+v", g.Status)
	})
	if time.Since(changedAt) >= installRetry {
		t.Fatal("receipt waited for polling rather than a job watch")
	}
	release(t, n)
	eventually(t, "one-way execution release", func() (bool, string) {
		if err := admin.Get(context.Background(), key, &j); err != nil {
			return false, err.Error()
		}
		return j.Spec.Release, "not released"
	})
	if grant(t, n).Status.Phase != v1alpha1.GrantActive {
		t.Fatal("grant claimed cleanup before keeper receipt")
	}
	now = metav1.Now()
	r.clock.SetTime(now.Time)
	j.Status.Phase, j.Status.RevokedAt = v1alpha1.CredentialRevoked, &now
	if err := admin.Status().Update(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, n, v1alpha1.GrantReleased)
}

func TestBrokerCredentialAuthorities(t *testing.T) {
	suite(t)
	const keeperUser = "system:serviceaccount:" + systemNS + ":dev-env-keeper"
	for _, tc := range []struct {
		user, resource, subresource, verb string
		want                              bool
	}{
		{brokerUser, "credentialjobs", "", "create", true},
		{brokerUser, "credentialjobs", "", "watch", true},
		{brokerUser, "credentialjobs", "status", "patch", false},
		{keeperUser, "credentialjobs", "status", "patch", true},
		{keeperUser, "credentialjobs", "", "create", false},
		{keeperUser, "credentialjobs", "", "delete", false},
		{keeperUser, "accessgrants", "status", "patch", false},
	} {
		ns := systemNS
		if tc.resource == "accessgrants" {
			ns = sessionNS
		}
		ra := authorizationv1.ResourceAttributes{Namespace: ns, Group: v1alpha1.GroupVersion.Group, Resource: tc.resource, Subresource: tc.subresource, Verb: tc.verb}
		if got := allowed(t, tc.user, ra); got != tc.want {
			t.Errorf("%s %s %s/%s = %v, want %v", tc.user, tc.verb, tc.resource, tc.subresource, got, tc.want)
		}
	}
	for _, tc := range []struct {
		user, name string
		want       bool
	}{
		{brokerUser, "dev-env-keeper-credential-journal", false},
		{keeperUser, "dev-env-keeper-credential-journal", true},
		{keeperUser, "another-private-secret", false},
	} {
		ra := authorizationv1.ResourceAttributes{Namespace: systemNS, Resource: "secrets", Name: tc.name, Verb: "get"}
		if got := allowed(t, tc.user, ra); got != tc.want {
			t.Errorf("journal scope %s %s = %v, want %v", tc.user, tc.name, got, tc.want)
		}
	}
	c, err := client.New(brokerCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	j := &v1alpha1.CredentialJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: v1alpha1.CredentialJobName("88888888-8888-8888-8888-888888888888"), Labels: map[string]string{v1alpha1.LabelGrant: "grant-auth-test", v1alpha1.LabelSession: "session-auth-test", v1alpha1.LabelManagedBy: ManagedBy}},
		Spec: v1alpha1.CredentialJobSpec{
			Grant:      v1alpha1.CredentialObjectReference{Namespace: sessionNS, Name: "grant-auth-test", UID: "88888888-8888-8888-8888-888888888888"},
			Session:    v1alpha1.CredentialObjectReference{Namespace: sessionNS, Name: "session-auth-test", UID: "99999999-9999-9999-9999-999999999999"},
			Credential: v1alpha1.CredentialProxmox, ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)),
		},
	}
	if err := c.Create(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), j); !apierrors.IsForbidden(err) {
		t.Fatalf("broker deleted unfinished credential job: %v", err)
	}
	kc, err := client.New(impersonate(keeperUser), client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	orig := j.DeepCopy()
	j.Finalizers = []string{v1alpha1.CredentialCleanupFinalizer}
	if err := kc.Patch(context.Background(), j, client.MergeFrom(orig)); err != nil {
		t.Fatal(err)
	}
	orig = j.DeepCopy()
	j.Finalizers = nil
	if err := c.Patch(context.Background(), j, client.MergeFrom(orig)); !apierrors.IsForbidden(err) {
		t.Fatalf("broker removed keeper cleanup fence: %v", err)
	}
	if err := admin.Get(context.Background(), client.ObjectKeyFromObject(j), j); err != nil {
		t.Fatal(err)
	}
	orig = j.DeepCopy()
	j.Spec.Release = true
	if err := kc.Patch(context.Background(), j, client.MergeFrom(orig)); !apierrors.IsForbidden(err) {
		t.Fatalf("keeper changed broker request: %v", err)
	}
	if err := c.Patch(context.Background(), j, client.MergeFrom(orig)); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	j.Status = v1alpha1.CredentialJobStatus{Phase: v1alpha1.CredentialRevoked, RevokedAt: &now}
	if err := kc.Status().Update(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	orig = j.DeepCopy()
	j.Finalizers = nil
	if err := kc.Patch(context.Background(), j, client.MergeFrom(orig)); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}
