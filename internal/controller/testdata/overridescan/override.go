// Package overridescan is never compiled. Each function writes the owner's
// HOME expiry override (DESIGN-025 9) in a different way, and
// TestNoCodeWritesHomeExpiryOverride's scan must name every one.
package overridescan

func assignHold(s *v1alpha1.AgentSession) {
	s.Annotations[v1alpha1.AnnotationHomeExpiry] = v1alpha1.HomeExpiryHold
}

func deleteOverride(s *v1alpha1.AgentSession) {
	delete(s.Annotations, v1alpha1.AnnotationHomeExpiry)
}

func mapLiteral(s *v1alpha1.AgentSession) {
	s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryReleaseNow}
}

func setterCall(s *v1alpha1.AgentSession) {
	metav1.SetMetaDataAnnotation(&s.ObjectMeta, v1alpha1.AnnotationHomeExpiry, "hold")
}

func mergePatchString(ctx context.Context, c client.Client, s *v1alpha1.AgentSession) error {
	return c.Patch(ctx, s, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"dev-env.haynesops.com/home-expiry":"hold"}}}`)))
}

func jsonPointerPatch(ctx context.Context, c client.Client, s *v1alpha1.AgentSession) error {
	return c.Patch(ctx, s, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/metadata/annotations/dev-env.haynesops.com~1home-expiry"}]`)))
}

func aliasedKey(s *v1alpha1.AgentSession) {
	key := v1alpha1.AnnotationHomeExpiry
	s.Annotations[key] = "release-now"
}

func literalKey(s *v1alpha1.AgentSession) {
	s.Annotations[v1alpha1.LabelPrefix+"home-expiry"] = "hold"
}

func getterAssign(s *v1alpha1.AgentSession) {
	s.GetAnnotations()[v1alpha1.AnnotationHomeExpiry] = "hold"
}

func incrementOverride(s *v1alpha1.AgentSession) {
	s.Annotations[v1alpha1.AnnotationHomeExpiry] += "-now"
}
