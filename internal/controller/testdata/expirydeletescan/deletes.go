// Never compiled: TestExpiryDeleteScanCatchesUncheckedDeletes runs
// scanExpiryDelete over this file. Each function but goodDelete and
// goodDeleting breaks one rule of TestExpiryDeleteNeedsVerify, and the scan
// must name it.
package controller

func deleteWithoutVerify(ctx context.Context, v *expiryView) {
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim)
}

func verifyAfterDelete(ctx context.Context, v *expiryView) {
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim)
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
}

func recheckBeforeVerify(ctx context.Context, v *expiryView) {
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim)
}

func podCheckOnStaleView(ctx context.Context, v *expiryView) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(v); ref != nil {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim)
}

func deleteOnStaleView(ctx context.Context, v *expiryView) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, v.s, v.claim)
}

func verifyIgnored(ctx context.Context, v *expiryView) {
	r.historyArchiveVerified(ctx, v, obs)
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim)
}

func noNilCheck(ctx context.Context, v *expiryView) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	after, _, _, _ := r.expiryRechecked(ctx, v, obs)
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim)
}

func deletingWithoutChecks(ctx context.Context, v *expiryView) {
	next := v.record()
	next.Phase = v1alpha1.HomeExpiryDeleting
}

func deletingBeforePodCheck(ctx context.Context, v *expiryView) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	next := after.record()
	next.Phase, next.DeletingAt = v1alpha1.HomeExpiryDeleting, &at
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
}

func verifyReturnsOnSuccess(ctx context.Context, v *expiryView) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); ok {
		return res, stop, err
	}
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim)
}

func podCheckNeverRefuses(ctx context.Context, v *expiryView) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref == nil && false {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim)
}

func goodDelete(ctx context.Context, v *expiryView) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
	deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim)
}

func goodDeleting(ctx context.Context, v *expiryView) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref != nil {
		return
	}
	next := after.record()
	next.Phase, next.DeletingAt = v1alpha1.HomeExpiryDeleting, &at
}
