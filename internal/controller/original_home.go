package controller

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/homeguard"
)

// CheckOriginalHome always uses the supplied uncached reader. No writes, repair,
// replacement, finalizer release or data inspection are permitted here.
func CheckOriginalHome(ctx context.Context, live client.Reader, s *v1alpha1.AgentSession, b *homeguard.Binding, running bool) error {
	if b == nil {
		return nil
	}
	if live == nil {
		return homeguard.ErrUnsafe
	}
	var fresh v1alpha1.AgentSession
	var pvc corev1.PersistentVolumeClaim
	var pv corev1.PersistentVolume
	if live.Get(ctx, client.ObjectKeyFromObject(s), &fresh) != nil || fresh.UID != s.UID || fresh.ResourceVersion != s.ResourceVersion || live.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: b.Claim()}, &pvc) != nil || live.Get(ctx, client.ObjectKey{Name: b.PV}, &pv) != nil {
		return homeguard.ErrUnsafe
	}
	sb, _ := json.Marshal(&fresh)
	cb, _ := json.Marshal(&pvc)
	vb, _ := json.Marshal(&pv)
	return b.Verify(sb, cb, vb, running)
}

func (r *Reconciler) originalHome(ctx context.Context, s *v1alpha1.AgentSession) (*homeguard.Binding, error) {
	// Always consult live metadata before a missing Pod may create a claim.
	// Configuration can be removed while the cache still predates admission.
	if r.APIReader == nil {
		return nil, homeguard.ErrUnsafe
	}
	var live v1alpha1.AgentSession
	if r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &live) != nil || live.UID != s.UID {
		return nil, homeguard.ErrUnsafe
	}
	b, err := homeguard.Select(r.OriginalHome, live.Namespace, live.Name, string(live.UID), live.Annotations)
	if err != nil || b == nil {
		return b, err
	}
	if s.Annotations[homeguard.Annotation] != b.String() {
		return nil, homeguard.ErrUnsafe
	}
	// Only an API lifecycle admission can durably bind this existing HOME.
	bound, err := homeguard.Select(nil, live.Namespace, live.Name, string(live.UID), live.Annotations)
	if err != nil || bound == nil {
		return nil, homeguard.ErrUnsafe
	}
	return b, CheckOriginalHome(ctx, r.APIReader, s, b, true)
}

// The init verifier mounts exactly the main container's HOME read-only. It must
// complete before boot writes. The supervisor repeats verification on each start.
func addOriginalHomeVerifier(p *corev1.Pod, b homeguard.Binding) {
	c := &p.Spec.Containers[0]
	c.Env = append(c.Env, corev1.EnvVar{Name: homeguard.Env, Value: b.String()})
	p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{
		Name: "original-home-verify", Image: c.Image, ImagePullPolicy: c.ImagePullPolicy,
		Command: []string{"/usr/local/bin/agentd"}, Args: []string{"home-verify"},
		Env:          []corev1.EnvVar{{Name: homeguard.Env, Value: b.String()}},
		VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: "/home/dev", ReadOnly: true}},
		Resources:    *c.Resources.DeepCopy(), SecurityContext: c.SecurityContext.DeepCopy(),
	})
}
