package controller

// DESIGN-025 unit H5: the paths the review of #251 found untested. A Verified
// rescue's Git bundles go into the archive beside the history, and a Verified
// rescue whose bundle directory is not its own refuses export_invalid; a hold
// set while the export runs keeps Archived from being written; a hold pod
// made again at Exporting is rescued again before the export; a verify whose
// answer does not match the entry put stored refuses; and an export refusal
// that only a source change clears gets a hold pod of the new agent image.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// verifiedRescue makes the rescuer answer Verified with one bundle in
// rescue/<session>/<stamp>/, and writes that directory, its bundle and its
// complete manifest to the shared volume, as agentd's rescue leaves them
// (D-48). manifestDir, when set, is the bundle directory the report names
// instead of its own.
func (f *expiryFixture) verifiedRescue(t *testing.T, stamp, manifestDir string) {
	t.Helper()
	f.sharedOnAnotherDevice(t)
	session := f.s.Name
	dir := protocol.RescueDir(session, stamp)
	bundle := "# v2 git bundle\nsynthetic bundle bytes\n"
	sum := sha256.Sum256([]byte(bundle))
	writeExpiryFile(t, filepath.Join(f.x.shared, filepath.FromSlash(dir), "haynes-ops.bundle"), bundle)
	m := protocol.RescueManifest{Version: 1, Session: session, Stamp: stamp, Dir: dir, CreatedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), Complete: true,
		Repos: []protocol.ManifestRepo{{Path: repoPath, Bundle: protocol.RepoBundle{File: dir + "/haynes-ops.bundle", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bundle)), Verified: true}}}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	writeExpiryFile(t, filepath.Join(f.x.shared, filepath.FromSlash(dir), protocol.ManifestFile), string(data))
	if manifestDir == "" {
		manifestDir = dir
	}
	f.rescuer.mu.Lock()
	defer f.rescuer.mu.Unlock()
	f.rescuer.report = func(*corev1.Pod) (protocol.RescueReport, error) {
		rep := goodReport(session)
		rep.Stamp = stamp
		rep.Repos[0].Bundle.File = dir + "/haynes-ops.bundle"
		rep.Bundle = &protocol.BundleReport{Dir: manifestDir, Manifest: manifestDir + "/" + protocol.ManifestFile}
		return rep, nil
	}
}

// sharedOnAnotherDevice moves the fixture's shared volume to a device other
// than its HOME's: agentd reads a Git rescue only from a shared volume that
// is mounted apart from HOME (D-48's check). /dev/shm is one on Linux.
func (f *expiryFixture) sharedOnAnotherDevice(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/dev/shm", "dev-env-expiry-shared-")
	if err != nil {
		t.Skip("no writable /dev/shm for a shared volume apart from HOME:", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	var sh, hm syscall.Stat_t
	if syscall.Stat(dir, &sh) != nil || syscall.Stat(f.x.home, &hm) != nil || sh.Dev == hm.Dev {
		t.Skip("/dev/shm is on HOME's device")
	}
	f.x.shared, f.x.archive.SharedDir = dir, dir
}

// A Verified rescue: the export names its bundle directory, the archive keeps
// the manifest and the bundle beside the history (gitFiles), and content.json
// names the rescue. A Verified rescue whose bundle directory is another
// Session's refuses export_invalid before any export runs.
func TestHomeExpiryArchivesAVerifiedRescueWithItsGitBundles(t *testing.T) {
	t.Run("its own bundle directory", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		f.verifiedRescue(t, "20261111-1200", "")
		f.at(f.due().Add(time.Minute))
		f.reconcile(t, 6)
		hold := f.runHold(t)
		f.reconcile(t, 8)
		s := f.session(t)
		e := s.Status.PrivateHistory.Expiry
		if r := s.Status.Rescue; r == nil || r.Result != v1alpha1.RescueVerified || r.PodUID != string(hold.UID) {
			t.Fatalf("the rescue is %+v; want Verified in the hold pod", r)
		}
		if e.Phase != v1alpha1.HomeExpiryArchived || e.Archive == nil || e.Archive.GitFiles != 2 || e.RescueStamp != "20261111-1200" {
			t.Fatalf("the record is %+v (HomeExpiry %+v, execs %v); want Archived with the manifest and the bundle as gitFiles", e, f.condition(t, ConditionHomeExpiry), f.x.called())
		}
		get, err := f.x.archive.Get(string(f.s.UID), e.Archive.ContentSHA256)
		if err != nil {
			t.Fatal(err)
		}
		if g := get.Content.Git; g == nil || g.Rescue != f.s.Name+"/20261111-1200" || len(g.Files) != 2 ||
			get.Content.Rescue.Result != protocol.HistoryRescueVerified || get.Content.Rescue.Stamp != "20261111-1200" {
			t.Fatalf("content.json's Git part %+v and rescue %+v; want the Verified rescue's directory", get.Content.Git, get.Content.Rescue)
		}
		ver, err := f.x.archive.Verify(string(f.s.UID), e.Archive.ContentSHA256)
		if err != nil || ver.ContentSHA256 != e.Archive.ContentSHA256 {
			t.Fatalf("the entry with its Git rescue does not verify: %+v, %v", ver, err)
		}
		if bundles, _ := filepath.Glob(filepath.Join(f.x.archive.Root, filepath.FromSlash(e.Archive.Entry), "git", "*", "*.bundle")); len(bundles) != 1 {
			t.Fatalf("the archived entry holds bundles %v; want the rescue's one", bundles)
		}
		f.claimIntact(t)
	})

	t.Run("a bundle directory that is not its own", func(t *testing.T) {
		f := newExpiryFixture(t, nil)
		f.verifiedRescue(t, "20261111-1200", protocol.RescueDir("another-session", "20261111-1200"))
		f.at(f.due().Add(time.Minute))
		f.reconcile(t, 6)
		f.runHold(t)
		f.reconcile(t, 5)
		e := f.expiry(t)
		if e.Phase != v1alpha1.HomeExpiryExporting || e.Refusal != expiryExportInvalid || e.Archive != nil || refusedCount(f.counters, expiryExportInvalid) != 1 {
			t.Fatalf("record %+v; want export_invalid at Exporting", e)
		}
		if calls := f.x.called(); len(calls) != 0 {
			t.Fatalf("history execs %v ran for a rescue that names no directory of its own", calls)
		}
		if got := f.x.entries(t); len(got) != 0 {
			t.Fatalf("archived %v", got)
		}
		f.claimIntact(t)
	})
}

// A hold set while the export runs: put stores the entry, but Archived is not
// written over the hold (the re-read before it finds the hold). The hold pod
// goes; once the hold is lifted, a new hold pod is made at Exporting and
// rescued again before its export, which then archives with the new rescue.
func TestHomeExpiryHoldDuringTheExportKeepsArchivedAndRescuesTheNewHoldPod(t *testing.T) {
	f := newExpiryFixture(t, nil)
	f.at(f.due().Add(time.Minute))
	f.reconcile(t, 6)
	first := f.runHold(t)
	f.reconcile(t, 2) // the rescue, then Exporting
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryExporting {
		t.Fatalf("not at Exporting: %+v", e)
	}
	f.x.before = map[string]func(){"verify": func() {
		f.update(t, func(s *v1alpha1.AgentSession) {
			s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
		})
	}}
	f.reconcile(t, 1)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryExporting || e.Archive != nil {
		t.Fatalf("Archived was written over a hold set during the export: %+v", e)
	}
	if counterValue(f.counters.ArchivedBytes) != 0 {
		t.Fatal("archived bytes counted for an export the record does not hold")
	}
	if got := f.x.entries(t); len(got) != 1 {
		t.Fatalf("archive entries %v; put's entry stays, unreferenced", got)
	}
	f.reconcile(t, 2)
	if p := f.named(t); p != nil {
		t.Fatalf("the hold pod stays under the owner hold: %s", p.UID)
	}

	f.update(t, func(s *v1alpha1.AgentSession) { delete(s.Annotations, v1alpha1.AnnotationHomeExpiry) })
	f.reconcile(t, 3)
	second := f.named(t)
	if second == nil || second.UID == first.UID || !isHoldPod(second) {
		t.Fatalf("lifted: hold pod %v; want a new one", second)
	}
	f.runHold(t)
	execsBefore := len(f.x.called())
	f.reconcile(t, 1)
	if ran := f.rescuer.ranIn(); len(ran) != 2 || ran[1] != string(second.UID) {
		t.Fatalf("rescues ran in %v; want the new hold pod rescued before its export", ran)
	}
	if len(f.x.called()) != execsBefore {
		t.Fatalf("the export ran before the new hold pod was rescued: %v", f.x.called())
	}
	f.reconcile(t, 6)
	e := f.expiry(t)
	if e.Phase != v1alpha1.HomeExpiryArchived || e.Archive == nil || e.HoldPodUID != string(second.UID) {
		t.Fatalf("the record is %+v; want Archived from the new hold pod", e)
	}
	get, err := f.x.archive.Get(string(f.s.UID), e.Archive.ContentSHA256)
	if err != nil || get.Content.Rescue.PodUID != string(second.UID) {
		t.Fatalf("content.json's rescue %+v, %v; want the new hold pod's", get.Content.Rescue, err)
	}
	if got := f.x.entries(t); len(got) != 2 || !slices.Contains(got, e.Archive.Entry) {
		t.Fatalf("archive entries %v; want the first, unreferenced, and the recorded %s", got, e.Archive.Entry)
	}
	f.claimIntact(t)
}

// verifyAnswer is a HistoryRelay whose Verify answers as the test says.
type verifyAnswer struct {
	HistoryRelay
	verify func(sessionUID, contentSHA256 string) (protocol.HistoryVerifyResult, error)
}

func (v verifyAnswer) Verify(_ context.Context, sessionUID, contentSHA256 string) (protocol.HistoryVerifyResult, error) {
	return v.verify(sessionUID, contentSHA256)
}

// A verify that answers another digest, another entry, corrupt or not_found
// refuses (archive_mismatch, archive_missing); Archived is not written, the
// hold pod and the HOME stay. Once verify answers the entry, the re-check
// archives it (put answers exists).
func TestHomeExpiryVerifyThatDoesNotMatchRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(real protocol.HistoryVerifyResult) (protocol.HistoryVerifyResult, error)
		reason string
	}{
		{"another digest", func(r protocol.HistoryVerifyResult) (protocol.HistoryVerifyResult, error) {
			r.ContentSHA256 = strings.Repeat("cd", 32)
			return r, nil
		}, expiryArchiveMismatch},
		{"another entry", func(r protocol.HistoryVerifyResult) (protocol.HistoryVerifyResult, error) {
			r.Entry = "v1/_none/other/0123456789abcdef"
			return r, nil
		}, expiryArchiveMismatch},
		{"corrupt", func(protocol.HistoryVerifyResult) (protocol.HistoryVerifyResult, error) {
			return protocol.HistoryVerifyResult{}, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "a block differs")
		}, expiryArchiveMismatch},
		{"not found", func(protocol.HistoryVerifyResult) (protocol.HistoryVerifyResult, error) {
			return protocol.HistoryVerifyResult{}, protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "no such entry")
		}, expiryArchiveMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExpiryFixture(t, nil)
			f.at(f.due().Add(time.Minute))
			f.reconcile(t, 6)
			hold := f.runHold(t)
			wrong := true
			run := func(times int) {
				t.Helper()
				r := f.reconciler()
				r.HistoryRelay = verifyAnswer{HistoryRelay: r.HistoryRelay, verify: func(uid, sum string) (protocol.HistoryVerifyResult, error) {
					real, err := f.x.archive.Verify(uid, sum)
					if err != nil || !wrong {
						return real, err
					}
					return tc.answer(real)
				}}
				for range times {
					if _, err := r.Reconcile(context.Background(), reqFor(f.s)); err != nil {
						t.Fatal(err)
					}
				}
			}
			run(5)
			e := f.expiry(t)
			if e.Phase != v1alpha1.HomeExpiryExporting || e.Archive != nil || e.Refusal != tc.reason || refusedCount(f.counters, tc.reason) != 1 {
				t.Fatalf("record %+v; want %s at Exporting, nothing archived in the record", e, tc.reason)
			}
			if p := f.named(t); p == nil || p.UID != hold.UID {
				t.Fatal("the hold pod went after a verify that did not match")
			}
			f.claimIntact(t)

			wrong = false
			f.at(f.clock.Add(homeExpiryRefusalRetry + time.Minute))
			run(4)
			if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryArchived || e.Refusal != "" {
				t.Fatalf("verify answers the entry now, the record is %+v; want Archived", e)
			}
		})
	}
}

// DESIGN-025 5.4: an export refusal that only a source change clears
// (unknown_entry here) is re-checked every 15 minutes in the same hold pod
// while the templates name its image. Once they name another, the re-check
// removes the hold pod, makes one of the new image, rescues it and exports
// from it; the refusal stands, and keeps the alert, until that export answers.
func TestHomeExpiryRemakesTheHoldPodForANewImage(t *testing.T) {
	ctx := context.Background()
	f := newExpiryFixture(t, nil)
	unknown := filepath.Join(f.x.home, ".claude", "brand-new-cache")
	writeExpiryFile(t, filepath.Join(unknown, "x"), "x")
	f.at(f.due().Add(time.Minute))
	f.reconcile(t, 6)
	first := f.runHold(t)
	f.reconcile(t, 5)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryExporting || e.Refusal != protocol.HistoryErrUnknownEntry {
		t.Fatalf("not refused unknown_entry at Exporting: %+v", e)
	}

	// The same image: the re-check exports again from the same hold pod.
	plans := func() int {
		n := 0
		for _, c := range f.x.called() {
			if c == "plan" {
				n++
			}
		}
		return n
	}
	f.at(f.clock.Add(homeExpiryRefusalRetry + time.Minute))
	f.reconcile(t, 3)
	if p := f.named(t); p == nil || p.UID != first.UID || plans() != 2 {
		t.Fatalf("hold pod %v, plans %d; want the same hold pod exported from again", p, plans())
	}
	if e := f.expiry(t); e.Refusal != protocol.HistoryErrUnknownEntry {
		t.Fatalf("the refusal changed: %+v", e)
	}

	// A new agent image in the templates, which classifies the entry.
	var cm corev1.ConfigMap
	if err := f.c.Get(ctx, templatesKey, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[templates.Key] = strings.Replace(exampleDoc, "0000000000000000000000000000000000000000000000000000000000000001",
		"0000000000000000000000000000000000000000000000000000000000000002", 1)
	if err := f.c.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(unknown); err != nil {
		t.Fatal(err)
	}
	f.at(f.clock.Add(homeExpiryRefusalRetry + time.Minute))
	f.reconcile(t, 1)
	if p := f.named(t); p != nil {
		t.Fatalf("the hold pod of the old image stays: %s", p.UID)
	}
	f.reconcile(t, 3)
	second := f.named(t)
	e := f.expiry(t)
	if second == nil || second.UID == first.UID || second.Spec.Containers[0].Image == first.Spec.Containers[0].Image {
		t.Fatalf("hold pod %v; want one of the new image", second)
	}
	if e.Refusal != protocol.HistoryErrUnknownEntry || e.HoldPodUID != string(second.UID) {
		t.Fatalf("the record %+v; want the refusal kept until the new export answers", e)
	}
	if g := gaugeValues(t, &HomeExpiryCollector{Reader: f.c, Namespace: f.s.Namespace, Now: func() time.Time { return *f.clock }}); g[MetricHomeExpiryRefusing+"/"+protocol.HistoryErrUnknownEntry] != 1 {
		t.Fatalf("the refusing gauge dropped the refusal while the hold pod was remade: %v", g)
	}
	f.runHold(t)
	f.reconcile(t, 6)
	e = f.expiry(t)
	if e.Phase != v1alpha1.HomeExpiryArchived || e.Refusal != "" {
		t.Fatalf("the record is %+v; want Archived from the new hold pod", e)
	}
	if ran := f.rescuer.ranIn(); len(ran) != 2 || ran[1] != string(second.UID) {
		t.Fatalf("rescues ran in %v; want the new hold pod rescued again", ran)
	}
	f.claimIntact(t)
}

// The operator's own plan cap (plan_too_large) is no source-change refusal: a
// new agent image cannot clear it, so the hold pod is not remade for one. The
// re-check exports again from the same hold pod, which an operator with a
// higher cap passes.
func TestHomeExpiryPlanCapKeepsItsHoldPodAcrossImages(t *testing.T) {
	ctx := context.Background()
	f := newExpiryFixture(t, nil)
	planMax := 64
	run := func(times int) {
		t.Helper()
		r := f.reconciler()
		r.HistoryRelay.(*ExecHistoryRelay).PlanMax = planMax
		for range times {
			if _, err := r.Reconcile(ctx, reqFor(f.s)); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.at(f.due().Add(time.Minute))
	run(6)
	hold := f.runHold(t)
	run(5)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryExporting || e.Refusal != expiryPlanTooLarge || refusedCount(f.counters, expiryPlanTooLarge) != 1 {
		t.Fatalf("record %+v; want plan_too_large at Exporting", e)
	}
	var cm corev1.ConfigMap
	if err := f.c.Get(ctx, templatesKey, &cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[templates.Key] = strings.Replace(exampleDoc, "0000000000000000000000000000000000000000000000000000000000000001",
		"0000000000000000000000000000000000000000000000000000000000000002", 1)
	if err := f.c.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	f.at(f.clock.Add(homeExpiryRefusalRetry + time.Minute))
	run(3)
	if p := f.named(t); p == nil || p.UID != hold.UID {
		t.Fatalf("hold pod %v; a new agent image cannot clear the operator's cap, so the hold pod stays", p)
	}
	if e := f.expiry(t); e.Refusal != expiryPlanTooLarge || len(f.rescuer.ranIn()) != 1 {
		t.Fatalf("record %+v, rescues %v; want plan_too_large again and no second rescue", e, f.rescuer.ranIn())
	}
	// An operator release with a higher cap passes at the next re-check.
	planMax = 0
	f.at(f.clock.Add(homeExpiryRefusalRetry + time.Minute))
	run(4)
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryArchived || e.Refusal != "" {
		t.Fatalf("with the default cap, the record is %+v; want Archived", e)
	}
	f.claimIntact(t)
}
