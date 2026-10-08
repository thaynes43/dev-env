package v1alpha1_test

import (
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func credentialJob() *v1alpha1.CredentialJob {
	uid := types.UID(fmt.Sprintf("10000000-0000-4000-8000-%012x", seq.Add(1)))
	return &v1alpha1.CredentialJob{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: v1alpha1.CredentialJobName(uid)}, Spec: v1alpha1.CredentialJobSpec{Grant: v1alpha1.CredentialObjectReference{Namespace: "dev-agents", Name: "grant-example", UID: uid}, Session: v1alpha1.CredentialObjectReference{Namespace: "dev-agents", Name: "session-example", UID: "20000000-0000-4000-8000-000000000001"}, Credential: v1alpha1.CredentialProxmox, ExpiresAt: metav1.NewTime(time.Now().UTC().Truncate(time.Second).Add(time.Hour))}}
}
func TestCredentialJobRequestImmutableReleaseMonotonic(t *testing.T) {
	job := credentialJob()
	if err := k8s.Create(ctx(t), job); err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		change func(*v1alpha1.CredentialJob)
	}{
		{"grantUID", func(j *v1alpha1.CredentialJob) { j.Spec.Grant.UID = "10000000-0000-4000-8000-00000000abcd" }},
		{"sessionUID", func(j *v1alpha1.CredentialJob) { j.Spec.Session.UID = "20000000-0000-4000-8000-00000000abcd" }},
		{"expiry", func(j *v1alpha1.CredentialJob) { j.Spec.ExpiresAt = metav1.NewTime(j.Spec.ExpiresAt.Add(time.Minute)) }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			copy := job.DeepCopy()
			test.change(copy)
			wantInvalid(t, k8s.Update(ctx(t), copy), "immutable")
		})
	}
	job.Spec.Release = true
	if err := k8s.Update(ctx(t), job); err != nil {
		t.Fatal(err)
	}
	job.Spec.Release = false
	wantInvalid(t, k8s.Update(ctx(t), job), "release")
}
func TestCredentialJobSchemaAndStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*v1alpha1.CredentialJob)
	}{
		{"unsafe UID", func(j *v1alpha1.CredentialJob) { j.Spec.Grant.UID = "uid; shell" }},
		{"hardware not built", func(j *v1alpha1.CredentialJob) { j.Spec.Credential = v1alpha1.CredentialHWSSH }},
		{"missing UID", func(j *v1alpha1.CredentialJob) { j.Spec.Session.UID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := credentialJob()
			test.change(job)
			if err := k8s.Create(ctx(t), job); err == nil {
				t.Fatal("invalid job accepted")
			}
		})
	}
	job := credentialJob()
	job.Status.Phase = v1alpha1.CredentialInstalled
	if err := k8s.Create(ctx(t), job); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.CredentialJob
	if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(job), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "" {
		t.Fatal("main-resource create forged a receipt")
	}
	got.Status.Phase = v1alpha1.CredentialInstalled
	got.Status.InstalledPodUID = "pod-uid"
	if err := k8s.Status().Update(ctx(t), &got); err != nil {
		t.Fatal(err)
	}
	var receipt v1alpha1.CredentialJob
	if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(job), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status.Phase != v1alpha1.CredentialInstalled || receipt.Status.InstalledPodUID != "pod-uid" {
		t.Fatal("status receipt was pruned")
	}
}
