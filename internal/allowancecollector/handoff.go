package allowancecollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// HandoffInput is an offline snapshot, not an authorization to mutate a slot.
// ExpectedJournalSHA and ExpectedReportSHA fence the entire original bytes.
type HandoffInput struct {
	OldConfig, NewConfig                  []byte
	Trust                                 providerallowance.AggregateTrust
	PublisherUID, PublisherSA             string
	SlotUID, ResourceVersion              string
	HomeUID, KubeconfigPath, KubeContext  string
	Journal, Report, EvictedPod           []byte
	ExpectedHelperSHA                     string
	ExpectedJournalSHA, ExpectedReportSHA string
	ExpectedCycle                         int64
	ExpectedNonce                         string
	Now                                   time.Time
}

// PreparedHandoff is an offline plan. Its hash is an integrity identifier, not
// proof of approval. Preparing it contacts neither Kubernetes nor a provider.
type PreparedHandoff struct {
	SlotUID, ResourceVersion                string
	HomeUID, KubeconfigPath, KubeContext    string
	OldFingerprint, NewFingerprint          string
	OldJournalSHA, NewJournalSHA, ReportSHA string
	NewJournal                              []byte
	Hash                                    string
}

// PrepareHandoff validates the old journal with the real collector parser and
// changes only the unique fingerprint value in its original byte stream. This
// maintenance operation is intentionally single-use: it accepts cycle 56 and
// the specifically approved 2026-11-04T15:33:23Z deadline only.
func PrepareHandoff(in HandoffInput) (*PreparedHandoff, error) {
	old, err := ParseConfig(in.OldConfig, in.Trust, in.PublisherUID, in.PublisherSA, in.Now)
	if err != nil {
		return nil, ErrCollector
	}
	next, err := ParseConfig(in.NewConfig, in.Trust, in.PublisherUID, in.PublisherSA, in.Now)
	if err != nil || !in.Now.Before(next.until) || !next.until.After(old.until) || old.SlotUID != in.SlotUID || next.SlotUID != in.SlotUID || in.ResourceVersion == "" || !uuid.MatchString(in.HomeUID) || !filepath.IsAbs(in.KubeconfigPath) || in.KubeContext == "" || !next.until.Equal(time.Date(2026, 11, 4, 15, 33, 23, 0, time.UTC)) {
		return nil, ErrCollector
	}
	a, b := old.Host, next.Host
	if a.Name != b.Name || a.SessionUID != b.SessionUID || a.ServiceAccount != b.ServiceAccount || a.PodUID == b.PodUID || b.Generation <= a.Generation || old.Version != next.Version || old.CadenceSeconds != next.CadenceSeconds || old.BackoffSeconds != next.BackoffSeconds || old.fingerprint == next.fingerprint {
		return nil, ErrCollector
	}
	if !remoteDigest(in.ExpectedJournalSHA) || !remoteDigest(in.ExpectedReportSHA) || digest(in.Journal) != in.ExpectedJournalSHA || digest(in.Report) != in.ExpectedReportSHA || in.ExpectedCycle != 56 || !uuid.MatchString(in.ExpectedNonce) {
		return nil, ErrCollector
	}
	held, err := readJournal(in.Journal, in.Report, old, in.Now)
	if err != nil || held.Phase != "idle" || held.Cycle != in.ExpectedCycle || held.Nonce != in.ExpectedNonce || !remoteDigest(in.ExpectedHelperSHA) || held.HelperSHA != in.ExpectedHelperSHA || !validEvictedPod(in.EvictedPod, old.Host) {
		return nil, ErrCollector
	}
	before, after := []byte(`"`+old.fingerprint+`"`), []byte(`"`+next.fingerprint+`"`)
	if bytes.Count(in.Journal, before) != 1 {
		return nil, ErrCollector
	}
	replacement := bytes.Replace(in.Journal, before, after, 1)
	if _, err := readJournal(replacement, in.Report, next, in.Now); err != nil {
		return nil, ErrCollector
	}
	p := &PreparedHandoff{SlotUID: in.SlotUID, ResourceVersion: in.ResourceVersion, HomeUID: in.HomeUID, KubeconfigPath: in.KubeconfigPath, KubeContext: in.KubeContext, OldFingerprint: old.fingerprint, NewFingerprint: next.fingerprint, OldJournalSHA: digest(in.Journal), NewJournalSHA: digest(replacement), ReportSHA: digest(in.Report), NewJournal: replacement}
	// Bind all input bytes and trusted policy as well as the exact proposed output.
	// The parser-owned fingerprint commits to trust, policy and the host identity.
	envelope, err := json.Marshal(struct {
		Domain                           string
		OldConfig, NewConfig, EvictedPod []byte
		Plan                             *PreparedHandoff
	}{"dev-env.allowance-handoff.prepare.v1", in.OldConfig, in.NewConfig, in.EvictedPod, p})
	if err != nil {
		return nil, ErrCollector
	}
	p.Hash = digest(envelope)
	return p, nil
}

// ApplyHandoff is a deliberately explicit maintenance primitive. The caller
// must independently select the approved kubeconfig and context and must obtain
// human approval of the exact prepared hash. A matching hash proves neither.
// The supplied client must be uncached and must target that exact context.
func ApplyHandoff(ctx context.Context, c client.Client, in HandoffInput, approvedHash, backupDir string) error {
	if backupDir == "" {
		return ErrCollector
	}
	return applyHandoff(ctx, c, in, approvedHash, backupDir, time.Now)
}

// handoffSlotBackupMax bounds the whole slot after an in-slot backup is added.
// A ConfigMap is limited to 1 MiB; leave room for metadata and a later report.
const handoffSlotBackupMax = 900 << 10

const handoffBackupPrefix = "handoff-backup-"

// Refusal reasons for the operator's log. Both wrap ErrCollector and carry no
// slot bytes. A stale snapshot or a hash that no longer matches both mean the
// request must be prepared again; any other refusal may clear by itself.
var (
	ErrHandoffHash  = errors.Join(ErrCollector, errors.New("approved hash does not match the prepared plan"))
	ErrHandoffStale = errors.Join(ErrCollector, errors.New("slot changed since the snapshot"))
)

// HandoffBackupKey is the slot entry that carries the full pre-image of an
// in-slot handoff, named by the approved prepared hash.
func HandoffBackupKey(hash string) string { return handoffBackupPrefix + hash + ".json" }

// ApplyHandoffInSlot is ApplyHandoff for a caller with no durable directory,
// such as the elected operator. The full pre-image is written into the slot
// object itself in the same compare-and-swap update as the journal change, so
// it is durable exactly when the new journal is and can never be missing after
// it. The same fences apply. A second call after the collector has advanced is
// acknowledged only when the exact backup entry is present and the current
// journal parses under the new configuration; it never writes again.
func ApplyHandoffInSlot(ctx context.Context, c client.Client, in HandoffInput, approvedHash string) error {
	return applyHandoff(ctx, c, in, approvedHash, "", time.Now)
}

func handoffBackupBytes(in HandoffInput) ([]byte, error) {
	return json.Marshal(struct {
		Schema                     string
		OldConfig, Journal, Report []byte
	}{"dev-env.allowance-handoff.backup.v1", in.OldConfig, in.Journal, in.Report})
}

func applyHandoff(ctx context.Context, c client.Client, in HandoffInput, approvedHash, backupDir string, clock func() time.Time) error {
	if clock == nil {
		return ErrCollector
	}
	inSlot := backupDir == ""
	in.Now = clock().UTC()
	if ctx == nil || c == nil || ctx.Err() != nil || !remoteDigest(approvedHash) {
		return ErrCollector
	}
	p, err := PrepareHandoff(in)
	if err != nil {
		return ErrCollector
	}
	if p.Hash != approvedHash {
		return ErrHandoffHash
	}
	next, err := ParseConfig(in.NewConfig, in.Trust, in.PublisherUID, in.PublisherSA, in.Now)
	if err != nil {
		return ErrCollector
	}
	if verifyHandoffHost(ctx, c, next, in.HomeUID, in.Now) != nil {
		return ErrCollector
	}
	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: apiserver.AggregateAllowanceNamespace, Name: apiserver.AggregateAllowanceName}
	if c.Get(ctx, key, &cm) != nil || !validHandoffSlot(&cm, in.SlotUID) {
		return ErrCollector
	}
	backupRaw, err := handoffBackupBytes(in)
	if err != nil {
		return ErrCollector
	}
	backupKey := HandoffBackupKey(p.Hash)
	// A previously completed identical operation may be acknowledged only with
	// its matching durable backup and exact report; no second write is made.
	if cm.Data[StateKey] == string(p.NewJournal) && cm.Data[apiserver.AggregateAllowanceDataKey] == string(in.Report) && cm.ResourceVersion != in.ResourceVersion {
		if inSlot {
			if cm.Data[backupKey] != string(backupRaw) {
				return ErrCollector
			}
			return nil
		}
		if checkHandoffBackup(backupDir, p.Hash, in, false) != nil {
			return ErrCollector
		}
		return nil
	}
	// The collector may already have run natural cycles under the new
	// configuration. That is the same completed operation only with the exact
	// backup this plan wrote and a journal the new configuration still accepts.
	if inSlot && cm.Data[backupKey] == string(backupRaw) && cm.ResourceVersion != in.ResourceVersion {
		if _, e := readJournal([]byte(cm.Data[StateKey]), []byte(cm.Data[apiserver.AggregateAllowanceDataKey]), next, in.Now); e == nil {
			return nil
		}
		return ErrCollector
	}
	if cm.ResourceVersion != in.ResourceVersion || cm.Data[StateKey] != string(in.Journal) || cm.Data[apiserver.AggregateAllowanceDataKey] != string(in.Report) {
		return ErrHandoffStale
	}
	if inSlot {
		total := len(backupRaw)
		for k, v := range cm.Data {
			if strings.HasPrefix(k, handoffBackupPrefix) {
				// Any earlier backup entry means another operation; this one is single-use.
				return ErrCollector
			}
			total += len(k) + len(v)
		}
		if total > handoffSlotBackupMax {
			return ErrCollector
		}
	} else if checkHandoffBackup(backupDir, p.Hash, in, true) != nil {
		return ErrCollector
	}
	in.Now = clock().UTC()
	if !in.Now.Before(next.until) || verifyHandoffHost(ctx, c, next, in.HomeUID, in.Now) != nil {
		return ErrCollector
	}
	var fresh corev1.ConfigMap
	if c.Get(ctx, key, &fresh) != nil || !validHandoffSlot(&fresh, in.SlotUID) || fresh.ResourceVersion != in.ResourceVersion || fresh.Data[StateKey] != string(in.Journal) || fresh.Data[apiserver.AggregateAllowanceDataKey] != string(in.Report) {
		return ErrCollector
	}
	fresh.Data[StateKey] = string(p.NewJournal)
	if inSlot {
		fresh.Data[backupKey] = string(backupRaw)
	}
	updateErr := c.Update(ctx, &fresh)
	var confirmed corev1.ConfigMap
	if c.Get(ctx, key, &confirmed) != nil || !validHandoffSlot(&confirmed, in.SlotUID) || confirmed.ResourceVersion == in.ResourceVersion || confirmed.Data[StateKey] != string(p.NewJournal) || confirmed.Data[apiserver.AggregateAllowanceDataKey] != string(in.Report) || inSlot && confirmed.Data[backupKey] != string(backupRaw) {
		return ErrCollector
	}
	// An error with exact changed-RV readback is a lost acknowledgement. Never
	// retry the mutation; this is the same already-completed operation.
	_ = updateErr
	return nil
}

// validEvictedPod checks the identity and terminal shape of an archived API
// object. Its authenticity is an external precondition, not proved by JSON.
func validEvictedPod(raw []byte, old Host) bool {
	if len(raw) == 0 || len(raw) > 256<<10 || !providerallowance.ValidJSONDocument(raw, 256<<10) {
		return false
	}
	var pod corev1.Pod
	if json.Unmarshal(raw, &pod) != nil || pod.APIVersion != "v1" || pod.Kind != "Pod" || pod.Namespace != Namespace || pod.Name != old.Name || string(pod.UID) != old.PodUID || pod.ResourceVersion == "" || pod.Spec.ServiceAccountName != old.ServiceAccount || pod.Status.Phase != corev1.PodFailed || pod.Status.Reason != "Evicted" {
		return false
	}
	session := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: old.Name, UID: types.UID(old.SessionUID)}}
	if !exactHandoffOwner(pod.OwnerReferences, session) {
		return false
	}
	agents := 0
	for _, container := range pod.Spec.Containers {
		if container.Name == "agent" {
			if container.Image != old.Image {
				return false
			}
			agents++
		}
	}
	return agents == 1
}

func validHandoffSlot(cm *corev1.ConfigMap, uid string) bool {
	return cm != nil && cm.Namespace == apiserver.AggregateAllowanceNamespace && cm.Name == apiserver.AggregateAllowanceName && string(cm.UID) == uid && cm.ResourceVersion != "" && cm.DeletionTimestamp.IsZero() && (cm.Immutable == nil || !*cm.Immutable) && len(cm.BinaryData[StateKey]) == 0 && len(cm.BinaryData[apiserver.AggregateAllowanceDataKey]) == 0 && cm.Data != nil
}

func verifyHandoffHost(ctx context.Context, r client.Reader, cfg *Config, homeUID string, now time.Time) error {
	if CheckHost(ctx, r, cfg.Host, now) != nil {
		return ErrCollector
	}
	key := types.NamespacedName{Namespace: Namespace, Name: cfg.Host.Name}
	var session v1alpha1.AgentSession
	var pod corev1.Pod
	var pvc corev1.PersistentVolumeClaim
	if r.Get(ctx, key, &session) != nil || r.Get(ctx, key, &pod) != nil || r.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: "home-" + cfg.Host.Name}, &pvc) != nil || string(session.UID) != cfg.Host.SessionUID || string(pod.UID) != cfg.Host.PodUID || string(pvc.UID) != homeUID || !pvc.DeletionTimestamp.IsZero() || pvc.Status.Phase != corev1.ClaimBound || !exactHandoffOwner(pvc.OwnerReferences, &session) || !exactHandoffOwner(pod.OwnerReferences, &session) {
		return ErrCollector
	}
	volumes := 0
	for _, v := range pod.Spec.Volumes {
		if v.Name == "home" && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == pvc.Name && !v.PersistentVolumeClaim.ReadOnly {
			volumes++
		}
	}
	mounts := 0
	for _, c := range pod.Spec.Containers {
		if c.Name == "agent" {
			for _, m := range c.VolumeMounts {
				if m.Name == "home" && m.MountPath == "/home/dev" && !m.ReadOnly && m.SubPath == "" && m.SubPathExpr == "" {
					mounts++
				}
			}
		}
	}
	if volumes != 1 || mounts != 1 {
		return ErrCollector
	}
	return nil
}

func exactHandoffOwner(refs []metav1.OwnerReference, session *v1alpha1.AgentSession) bool {
	count := 0
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			count++
			if ref.APIVersion != v1alpha1.GroupVersion.String() || ref.Kind != "AgentSession" || ref.Name != session.Name || ref.UID != session.UID {
				return false
			}
		}
	}
	return count == 1
}

// checkHandoffBackup writes only a private full snapshot, fsyncs the file and
// its directory before a mutation, and checks the exact bytes on a retry.
func checkHandoffBackup(dir, hash string, in HandoffInput, create bool) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return ErrCollector
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return ErrCollector
	}
	d, err := os.Open(dir)
	if err != nil {
		return ErrCollector
	}
	defer func() { _ = d.Close() }()
	di, err := d.Stat()
	if err != nil || !di.IsDir() || di.Mode().Perm() != 0700 {
		return ErrCollector
	}
	owner, ok := di.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return ErrCollector
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ErrCollector
	}
	defer func() { _ = root.Close() }()
	raw, err := handoffBackupBytes(in)
	if err != nil {
		return ErrCollector
	}
	name := hash + ".json"
	flags := os.O_RDONLY
	if create {
		flags = os.O_CREATE | os.O_EXCL | os.O_WRONLY
	}
	f, err := root.OpenFile(name, flags, 0600)
	if errors.Is(err, os.ErrExist) && create {
		return checkHandoffBackup(dir, hash, in, false)
	}
	if err != nil {
		return ErrCollector
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	st, ok := fiSys(fi)
	if err != nil || !ok || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return ErrCollector
	}
	// A preexisting name must never be a symlink, even to a file inside dir.
	li, err := os.Lstat(filepath.Join(dir, name))
	if err != nil || li.Mode()&os.ModeSymlink != 0 || !os.SameFile(li, fi) {
		return ErrCollector
	}
	if create {
		if n, e := f.Write(raw); e != nil || n != len(raw) {
			return ErrCollector
		}
		if f.Sync() != nil || d.Sync() != nil {
			return ErrCollector
		}
	} else {
		got, e := io.ReadAll(io.LimitReader(f, int64(len(raw))+1))
		if e != nil || !bytes.Equal(got, raw) {
			return ErrCollector
		}
		// Re-sync on retry, so a prior failed sync cannot be treated as durable.
		if f.Sync() != nil || d.Sync() != nil {
			return ErrCollector
		}
	}
	return nil
}
func fiSys(fi os.FileInfo) (*syscall.Stat_t, bool) {
	if fi == nil {
		return nil, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}
