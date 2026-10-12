// This file is never compiled. TestGuardScanCatchesReleaseWrites parses it to
// prove that TestOnlyTheGuardDeletes' scan catches every way below of deleting
// an object or lifting a finalizer or an owner reference outside the guards.
// Each function's name is the case the scan must report.
package guardscan

func rawFinalizerReplace(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	return r.Client.Patch(ctx, s, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"replace","path":"/metadata/finalizers","value":[]}]`)))
}

func rawFinalizerRemove(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	return r.Client.Patch(ctx, s, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/metadata/finalizers/0"}]`)))
}

func rawOwnerReplace(ctx context.Context, r *Reconciler, home *corev1.PersistentVolumeClaim) error {
	return r.Client.Patch(ctx, home, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"replace","path":"/metadata/ownerReferences","value":[]}]`)))
}

func mergeFinalizerNull(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	return r.Client.Patch(ctx, s, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`)))
}

func mapPatchFinalizers(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	data, _ := json.Marshal(map[string]any{"metadata": map[string]any{"finalizers": []string{}}})
	return r.Client.Patch(ctx, s, client.RawPatch(types.MergePatchType, data))
}

func setFinalizersThenUpdate(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	s.SetFinalizers(nil)
	return r.Client.Update(ctx, s)
}

func setOwnersThenPatch(ctx context.Context, r *Reconciler, pod *corev1.Pod) error {
	orig := pod.DeepCopy()
	pod.SetOwnerReferences(nil)
	return r.Client.Patch(ctx, pod, client.MergeFrom(orig))
}

func assignFinalizersThenPatch(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	orig := s.DeepCopy()
	s.Finalizers = nil
	return r.Client.Patch(ctx, s, client.MergeFrom(orig))
}

func assignNestedOwnersThenPatch(ctx context.Context, r *Reconciler, home *corev1.PersistentVolumeClaim) error {
	orig := home.DeepCopy()
	home.ObjectMeta.OwnerReferences = home.OwnerReferences[:0]
	return r.Client.Patch(ctx, home, client.MergeFrom(orig))
}

func deleteFuncInPlace(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	orig := s.DeepCopy()
	slices.DeleteFunc(s.Finalizers, func(f string) bool { return f == Finalizer })
	return r.Client.Patch(ctx, s, client.MergeFrom(orig))
}

func removeOwnerThenPatch(ctx context.Context, r *Reconciler, home *corev1.PersistentVolumeClaim, s *v1alpha1.AgentSession) error {
	orig := home.DeepCopy()
	_ = controllerutil.RemoveOwnerReference(s, home, scheme)
	return r.Client.Patch(ctx, home, client.MergeFrom(orig))
}

func wholeObjectUpdate(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	fresh := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, ResourceVersion: s.ResourceVersion}, Spec: s.Spec}
	return r.Client.Update(ctx, fresh)
}

func unguardedDelete(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	return c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID})
}

func unguardedRemoveFinalizer(ctx context.Context, c client.Client, s *v1alpha1.AgentSession) error {
	orig := s.DeepCopy()
	controllerutil.RemoveFinalizer(s, Finalizer)
	return c.Patch(ctx, s, client.MergeFrom(orig))
}

// deletePod is an allowed delete in the right function after its guard, but by
// name only: a replacement object with the same name would go too.
func deletePod(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if err := podRemovalAllowed(s, pod); err != nil {
		return err
	}
	return c.Delete(ctx, pod)
}

// releasePatch keeps the patch text out of the function that sends it.
const releasePatch = `[{"op":"replace","path":"/metadata/finalizers","value":[]}]`

func pkgConstPatch(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	return r.Client.Patch(ctx, s, client.RawPatch(types.JSONPatchType, []byte(releasePatch)))
}

// The merge patch from s to an object with only a name and namespace sets
// finalizers and ownerReferences to null.
func freshObjectMergePatch(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	fresh := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}}
	return r.Client.Patch(ctx, fresh, client.MergeFrom(s))
}

func wholeMetaAssignThenPatch(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	orig := s.DeepCopy()
	s.ObjectMeta = metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}
	return r.Client.Patch(ctx, s, client.MergeFrom(orig))
}

func derefAssignThenPatch(ctx context.Context, r *Reconciler, s *v1alpha1.AgentSession) error {
	orig := s.DeepCopy()
	*s = v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}, Spec: s.Spec}
	return r.Client.Patch(ctx, s, client.MergeFrom(orig))
}

// pkgVarFuncLiteral is a release written as a package-level function value.
var pkgVarFuncLiteral = func(ctx context.Context, c client.Client, s *v1alpha1.AgentSession) error {
	orig := s.DeepCopy()
	s.SetFinalizers(nil)
	return c.Patch(ctx, s, client.MergeFrom(orig))
}
