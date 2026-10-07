package broker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// What a kube grant is made of (DESIGN-001 6.12, D-54): a ServiceAccount in the
// session namespace named after the grant, and a RoleBinding named after the
// grant in each granted namespace, or one ClusterRoleBinding for a cluster-wide
// role, binding the catalog role to that ServiceAccount alone. Each carries the
// labels of grantLabels. The broker reads and deletes them by name, through
// the API server, and never lists or caches them.

// ensure makes whatever of the grant is missing and reports what it made. An
// object of the grant's name that the broker did not make for this grant, or
// that binds something else, is a permanent error: the grant fails and the
// object is left alone.
func (b *Broker) ensure(ctx context.Context, g *v1alpha1.AccessGrant) ([]string, error) {
	var made []string
	labels := grantLabels(g)
	sa := &corev1.ServiceAccount{
		ObjectMeta:                   metav1.ObjectMeta{Namespace: b.SessionNamespace, Name: g.Name, Labels: labels},
		AutomountServiceAccountToken: ptr.To(false),
	}
	created, err := b.ensureOne(ctx, g, sa, &corev1.ServiceAccount{}, nil)
	if err != nil {
		return made, err
	}
	if created {
		made = append(made, "serviceaccount "+b.SessionNamespace+"/"+g.Name)
	}

	k := g.Spec.Kube
	roleRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: k.Role}
	subjects := []rbacv1.Subject{b.subject(g)}
	if v1alpha1.ClusterWideRole(k.Role) {
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: g.Name, Labels: labels},
			RoleRef:    roleRef,
			Subjects:   subjects,
		}
		created, err := b.ensureOne(ctx, g, crb, &rbacv1.ClusterRoleBinding{}, func(o client.Object) bool {
			have := o.(*rbacv1.ClusterRoleBinding)
			return have.RoleRef == roleRef && apiequality.Semantic.DeepEqual(have.Subjects, subjects)
		})
		if err != nil {
			return made, err
		}
		if created {
			made = append(made, "clusterrolebinding "+g.Name)
		}
		return made, nil
	}
	for _, ns := range k.Namespaces {
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: g.Name, Labels: labels},
			RoleRef:    roleRef,
			Subjects:   subjects,
		}
		created, err := b.ensureOne(ctx, g, rb, &rbacv1.RoleBinding{}, func(o client.Object) bool {
			have := o.(*rbacv1.RoleBinding)
			return have.RoleRef == roleRef && apiequality.Semantic.DeepEqual(have.Subjects, subjects)
		})
		if err != nil {
			return made, err
		}
		if created {
			made = append(made, "rolebinding "+ns+"/"+g.Name)
		}
	}
	return made, nil
}

// ensureOne creates want unless it exists. An existing object must carry the
// grant's label, and pass same when same is set.
func (b *Broker) ensureOne(ctx context.Context, g *v1alpha1.AccessGrant, want, have client.Object, same func(client.Object) bool) (bool, error) {
	kind := kindOf(want)
	for range 2 {
		err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(want), have)
		switch {
		case err == nil:
			if have.GetLabels()[v1alpha1.LabelGrant] != g.Name {
				return false, permanent{fmt.Errorf("%s %s exists and the broker did not make it for this grant", kind, describe(want))}
			}
			if !have.GetDeletionTimestamp().IsZero() {
				return false, fmt.Errorf("%s %s is still being deleted", kind, describe(want))
			}
			if same != nil && !same(have) {
				return false, permanent{fmt.Errorf("%s %s binds something other than the grant's role to the grant's ServiceAccount", kind, describe(want))}
			}
			return false, nil
		case !apierrors.IsNotFound(err):
			return false, fmt.Errorf("read %s %s: %w", kind, describe(want), err)
		}
		err = b.Client.Create(ctx, want)
		switch {
		case err == nil:
			return true, nil
		case apierrors.IsAlreadyExists(err):
			continue // made a moment ago: read it and check it
		case apierrors.IsNotFound(err):
			// The object was not found a moment ago, so this is its namespace.
			return false, permanent{fmt.Errorf("%s %s: the namespace does not exist", kind, describe(want))}
		case apierrors.IsForbidden(err), apierrors.IsInvalid(err):
			// RBAC (bind on the catalog only) or the broker's admission guard
			// refused it; retrying cannot change that.
			return false, permanent{fmt.Errorf("create %s %s: %w", kind, describe(want), err)}
		default:
			return false, fmt.Errorf("create %s %s: %w", kind, describe(want), err)
		}
	}
	return false, fmt.Errorf("%s %s appeared and went while it was being made", kind, describe(want))
}

// revoke deletes what the grant made, the bindings before the ServiceAccount,
// then asks the installer to take the token out of the session's pod. It
// deletes only objects named after the grant and labelled with it; NotFound is
// success, so it can run again. Deleting the ServiceAccount ends every token
// of the grant at once.
func (b *Broker) revoke(ctx context.Context, g *v1alpha1.AccessGrant, why string) error {
	var deleted, kept []string
	var errs []error
	del := func(o client.Object) {
		gone, err := b.deleteOwn(ctx, g, o)
		switch {
		case err != nil:
			errs = append(errs, err)
		case gone:
			deleted = append(deleted, kindOf(o)+" "+describe(o))
		default:
			kept = append(kept, kindOf(o)+" "+describe(o))
		}
	}
	if k := g.Spec.Kube; k != nil {
		if v1alpha1.ClusterWideRole(k.Role) {
			del(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: g.Name}})
		} else {
			for _, ns := range k.Namespaces {
				del(&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: g.Name}})
			}
		}
	}
	if len(errs) == 0 {
		// The identity goes last, once nothing binds it.
		del(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: b.SessionNamespace, Name: g.Name}})
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("revoke %s: %w", g.Name, err)
	}

	l := log.FromContext(ctx)
	if b.Installer != nil {
		// A session being reaped keeps its pod until its rescue, so a
		// deleting session's pod still has the token taken out.
		// Exec may have delivered a token before its installation status
		// was persisted. Removal is idempotent and fences the current pod
		// by UID, so even an empty InstalledPodUID must attempt cleanup.
		if s, err := b.lookupSession(ctx, g.Spec.Requester.Session); err == nil && s != nil {
			if pod := b.sessionPod(ctx, s, false); pod != nil {
				// Best effort: the token is dead already, with its
				// ServiceAccount.
				if err := b.Installer.Remove(ctx, pod, g); err != nil {
					l.Error(err, "could not remove the grant from the session pod", "grant", g.Name, "pod", pod.Name)
				}
			}
		}
	}
	l.Info("revoked the grant", append(grantFields(g), "why", why, "deleted", deleted, "leftAlone", kept)...)
	note := "revoked (" + why + ")"
	if len(deleted) > 0 {
		note += ": deleted " + strings.Join(deleted, ", ")
	}
	b.event(g, corev1.EventTypeNormal, "Revoke", "Revoked", note)
	return nil
}

// deleteOwn deletes o if it exists, is named after the grant and carries the
// grant's label; gone reports that it is not there any more. The delete is
// conditional on the UID that was read, so it never removes a newer object of
// the same name.
func (b *Broker) deleteOwn(ctx context.Context, g *v1alpha1.AccessGrant, o client.Object) (gone bool, err error) {
	if o.GetName() != g.Name {
		return false, nil
	}
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("read %s %s: %w", kindOf(o), describe(o), err)
	}
	if o.GetLabels()[v1alpha1.LabelGrant] != g.Name {
		log.FromContext(ctx).Info("left alone: not made for this grant", "grant", g.Name, "kind", kindOf(o), "object", describe(o))
		return false, nil
	}
	uid := o.GetUID()
	if err := b.Client.Delete(ctx, o, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("delete %s %s: %w", kindOf(o), describe(o), err)
	}
	return true, nil
}

func kindOf(o client.Object) string {
	switch o.(type) {
	case *corev1.ServiceAccount:
		return "serviceaccount"
	case *rbacv1.RoleBinding:
		return "rolebinding"
	case *rbacv1.ClusterRoleBinding:
		return "clusterrolebinding"
	}
	return fmt.Sprintf("%T", o)
}

func describe(o client.Object) string {
	if o.GetNamespace() == "" {
		return o.GetName()
	}
	return o.GetNamespace() + "/" + o.GetName()
}
