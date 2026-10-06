package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// buildHomeClaim returns the session volume (D-22): one RWO claim, mounted at
// /home/dev, on the size class's storage class. Like the pod, it is never updated
// after create: a later change to the templates' size or class reaches new
// sessions only. Archive (plan 01 step 5) deletes it and lifts its finalizer.
func buildHomeClaim(s *v1alpha1.AgentSession, t *templates.Templates) (*corev1.PersistentVolumeClaim, error) {
	profile, _, err := t.Profile(s.Spec.Profile)
	if err != nil {
		return nil, err
	}
	if _, err := t.Size(s.Spec.Size); err != nil {
		return nil, err
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            HomeClaimName(s.Name),
			Namespace:       s.Namespace,
			Labels:          sessionLabels(s, profile),
			Annotations:     sessionAnnotations(s),
			OwnerReferences: []metav1.OwnerReference{ownerRef(s)},
			// The volume outlives any delete until its session is rescued
			// and archived (D-45): a direct delete, or the garbage
			// collector's foreground cascade, leaves it terminating with
			// its data.
			Finalizers: []string{Finalizer},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: ptr.To(t.StorageClassFor(s.Spec.Size)),
			VolumeMode:       ptr.To(corev1.PersistentVolumeFilesystem),
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: t.Home.Size.DeepCopy()},
			},
		},
	}, nil
}
