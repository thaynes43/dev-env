// Package podwritescan is never compiled. TestPodWriteScanCatchesPodWrites
// runs TestOnlyStopPrivateExecutorPatchesPods's scan over this file, and the
// scan must name every function here: each writes a Pod, or reaches the stop
// patch, in a way DESIGN-025 section 11 forbids.
package podwritescan

import (
	"context"

	core "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func patchPodElsewhere(ctx context.Context, r *Reconciler, pod *core.Pod) error {
	return r.Client.Patch(ctx, pod, client.RawPatch(types.JSONPatchType, []byte(`[]`)))
}

func updateDeclaredPod(ctx context.Context, r *Reconciler) error {
	var executor core.Pod
	return r.Client.Update(ctx, &executor)
}

func statusUpdatePodParam(ctx context.Context, r *Reconciler, p *core.Pod) error {
	return r.Client.Status().Update(ctx, p)
}

func ephemeralContainersUpdate(ctx context.Context, r *Reconciler, target *core.Pod) error {
	return r.Client.SubResource("ephemeralcontainers").Update(ctx, target)
}

func evictionCreate(ctx context.Context, r *Reconciler, victim *core.Pod) error {
	return r.Client.SubResource("eviction").Create(ctx, victim, &policyv1.Eviction{})
}

func deepCopyThenPatch(ctx context.Context, r *Reconciler, executor *core.Pod) error {
	copied := executor.DeepCopy()
	return r.Client.Patch(ctx, copied, client.MergeFrom(executor))
}

func patchOwnedObj(ctx context.Context, r *Reconciler, obs *observation) error {
	return r.Client.Patch(ctx, obs.pod.obj, client.RawPatch(types.MergePatchType, []byte(`{}`)))
}

func typedClientsetPatch(ctx context.Context, cs Clientset) error {
	_, err := cs.CoreV1().Pods("dev-agents").Patch(ctx, "x", types.JSONPatchType, []byte(`[]`), metav1PatchOptions)
	return err
}

func typedClientsetEvict(ctx context.Context, cs Clientset) error {
	return cs.CoreV1().Pods("dev-agents").EvictV1(ctx, &policyv1.Eviction{})
}

func restClientPatch(ctx context.Context, rc RESTClient) error {
	return rc.Patch(types.JSONPatchType).Resource("pods").Namespace("dev-agents").Name("x").Body([]byte(`[]`)).Do(ctx).Error()
}

func deletePodElsewhere(ctx context.Context, r *Reconciler, pod *core.Pod) error {
	return r.Client.Delete(ctx, pod, client.Preconditions{UID: &pod.UID})
}

func applyPod(ctx context.Context, r *Reconciler) error {
	desired := &core.Pod{}
	return r.Client.Apply(ctx, desired)
}

// requestPrivateExecutorStop's shape, from another function: stopPrivateExecutor
// may be called only from requestPrivateExecutorStop, after stopIntentConfirmed.
func stopWithoutIntent(ctx context.Context, r *Reconciler, s *Session) error {
	_, err := r.stopPrivateExecutor(ctx, s)
	return err
}

func stopIntentAfterStop(ctx context.Context, r *Reconciler, s *Session) error {
	if _, err := r.stopPrivateExecutor(ctx, s); err != nil {
		return err
	}
	_, err := r.stopIntentConfirmed(ctx, s)
	return err
}

func forgedIntent(s *Session, p *core.Pod) confirmedStopIntent {
	return confirmedStopIntent{confirmedSession: s, readExecutor: p}
}

func intentFieldWrite(s *Session) confirmedStopIntent {
	var intent confirmedStopIntent
	intent.confirmedSession = s
	return intent
}

// stopIntentConfirmed here builds the intent before its confirmed write.
func stopIntentConfirmed(ctx context.Context, r *Reconciler, s, fresh *Session, p *core.Pod) (confirmedStopIntent, error) {
	intent := confirmedStopIntent{confirmedSession: s, readExecutor: p}
	if _, err := r.writePrivateHistoryAcknowledged(ctx, s, fresh); err != nil {
		return confirmedStopIntent{}, err
	}
	return intent, nil
}

// stopPrivateExecutor here sends a merge patch, then a second patch, and builds
// neither with privateStopPatch.
func stopPrivateExecutor(ctx context.Context, r *Reconciler, pod *core.Pod) error {
	if err := r.Client.Patch(ctx, pod, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"activeDeadlineSeconds":1}}`))); err != nil {
		return err
	}
	return r.Client.Patch(ctx, pod, client.RawPatch(types.JSONPatchType, []byte(`[]`)))
}
