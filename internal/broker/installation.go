package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// InstallationAnnotation persists only pod UID, compatibility retry count and
// token expiry. A broker restart needs no in-memory grant state (D-63).
const InstallationAnnotation = v1alpha1.LabelPrefix + "installation"

const (
	maxCompatibilityAttempts = 3
	installRetry             = 10 * time.Second
	tokenRefreshMargin       = time.Minute
)

type installationState struct {
	PodUID       string       `json:"podUID"`
	Failures     int          `json:"failures,omitempty"`
	TokenExpires *metav1.Time `json:"tokenExpires,omitempty"`
}

func installedState(g *v1alpha1.AccessGrant) installationState {
	var s installationState
	if json.Unmarshal([]byte(g.Annotations[InstallationAnnotation]), &s) != nil || s.Failures < 0 {
		return installationState{}
	}
	return s
}

func (b *Broker) recordInstallation(ctx context.Context, g *v1alpha1.AccessGrant, s installationState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return errors.New("could not encode grant installation state")
	}
	orig := g.DeepCopy()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[InstallationAnnotation] = string(data)
	return b.Client.Patch(ctx, g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
}

// install keeps the installation in step with its pod and the API server's
// actual token expiry. TokenRequest may shorten a requested lifetime, so the
// broker records the returned expiry and refreshes it before it ends.
func (b *Broker) install(ctx context.Context, g *v1alpha1.AccessGrant, pod *corev1.Pod, now metav1.Time) (ctrl.Result, bool, error) {
	expires := g.Status.ExpiresAt.Time
	s := installedState(g)
	refreshAt := expires
	if s.PodUID == string(pod.UID) && s.TokenExpires != nil && s.TokenExpires.Before(g.Status.ExpiresAt) {
		issuedAt := now.Time
		if g.Status.InstalledAt != nil {
			issuedAt = g.Status.InstalledAt.Time
		}
		refreshAt = tokenRefreshTime(s.TokenExpires.Time, issuedAt)
	}
	if g.Status.InstalledPodUID == string(pod.UID) && now.Time.Before(refreshAt) {
		return ctrl.Result{RequeueAfter: refreshAt.Sub(now.Time)}, false, nil
	}
	token, tokenExpires, err := b.mint(ctx, g, now)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	if token == "" || tokenExpires.IsZero() || !now.Time.Before(tokenExpires) {
		return ctrl.Result{}, false, errors.New("TokenRequest returned an empty or expired token")
	}
	if err := b.Installer.Install(ctx, pod, g, token, earliest(tokenExpires, expires)); err != nil {
		b.event(g, corev1.EventTypeWarning, "Install", "InstallFailed", fmt.Sprintf("pod %s: %v", pod.Name, err))
		if !errors.Is(err, ErrIncompatiblePod) {
			return ctrl.Result{}, false, fmt.Errorf("install the grant in pod %s: %w", pod.Name, err)
		}
		if s.PodUID != string(pod.UID) {
			s = installationState{PodUID: string(pod.UID)}
		}
		s.Failures++
		if err := b.recordInstallation(ctx, g, s); err != nil {
			res, err := retryOn(err, "record the grant installation attempt")
			return res, false, err
		}
		if s.Failures >= maxCompatibilityAttempts {
			res, err := b.end(ctx, g, v1alpha1.GrantFailed, "", "the session pod does not support kube grants after 3 attempts; a new session pod is needed", now)
			return res, false, err
		}
		return ctrl.Result{RequeueAfter: min(installRetry, expires.Sub(now.Time))}, false, nil
	}
	s = installationState{PodUID: string(pod.UID), TokenExpires: &metav1.Time{Time: tokenExpires}}
	if err := b.recordInstallation(ctx, g, s); err != nil {
		res, err := retryOn(err, "record the grant token expiry")
		return res, false, err
	}
	g.Status.InstalledPodUID = string(pod.UID)
	g.Status.InstalledAt = &now
	log.FromContext(ctx).Info("installed the grant", append(grantFields(g), "pod", pod.Name, "podUID", pod.UID)...)
	b.event(g, corev1.EventTypeNormal, "Install", "Installed", "installed in pod "+pod.Name)
	refreshAt = expires
	if tokenExpires.Before(expires) {
		// TokenRequest normally issues at least ten minutes; never spin if
		// a server nevertheless returns less than the refresh margin.
		refreshAt = tokenRefreshTime(tokenExpires, now.Time)
	}
	return ctrl.Result{RequeueAfter: refreshAt.Sub(now.Time)}, true, nil
}

func tokenRefreshTime(expires, issued time.Time) time.Time {
	return expires.Add(-min(tokenRefreshMargin, expires.Sub(issued)/2))
}
