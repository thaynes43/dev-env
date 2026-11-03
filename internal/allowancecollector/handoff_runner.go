package allowancecollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// The elected operator has no kubeconfig. A plan prepared for it binds these
// two fixed values in place of the CLI's --kubeconfig and --context, so the
// approver can reproduce the hash with `dev-env-allowance-handoff prepare`.
const (
	OperatorKubeconfigPath = "/var/run/secrets/kubernetes.io/serviceaccount"
	OperatorKubeContext    = "in-cluster:dev-env-operator"
	handoffFileMax         = 512 << 10
)

// HandoffSnapshot is the offline evidence for one handoff. Holding it is not
// approval and its journal and report fields are fenced by their digests.
type HandoffSnapshot struct {
	OldConfig       []byte `json:"oldConfig"`
	NewConfig       []byte `json:"newConfig"`
	Trust           []byte `json:"trust"`
	PublisherUID    string `json:"publisherUID"`
	PublisherSA     string `json:"publisherSA"`
	SlotUID         string `json:"slotUID"`
	ResourceVersion string `json:"resourceVersion"`
	HomeUID         string `json:"homeUID"`
	Journal         []byte `json:"journal"`
	Report          []byte `json:"report"`
	EvictedPod      []byte `json:"evictedPod"`
	HelperSHA       string `json:"helperSHA256"`
	JournalSHA      string `json:"journalSHA256"`
	ReportSHA       string `json:"reportSHA256"`
	Cycle           int64  `json:"cycle"`
	Nonce           string `json:"nonce"`
}

var handoffSnapshotKeys = []string{"oldConfig", "newConfig", "trust", "publisherUID", "publisherSA", "slotUID", "resourceVersion", "homeUID", "journal", "report", "evictedPod", "helperSHA256", "journalSHA256", "reportSHA256", "cycle", "nonce"}

// DecodeHandoffSnapshot accepts exactly the snapshot keys and nothing after.
func DecodeHandoffSnapshot(raw []byte) (HandoffSnapshot, error) {
	var s HandoffSnapshot
	if len(raw) == 0 || len(raw) > handoffFileMax || !utf8.Valid(raw) || !providerallowance.ExactObjectKeys(raw, handoffSnapshotKeys) {
		return s, ErrCollector
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&s) != nil || dec.Decode(new(any)) != io.EOF {
		return HandoffSnapshot{}, ErrCollector
	}
	return s, nil
}

// Input builds the fenced input. The caller chooses the trust and publisher.
func (s HandoffSnapshot) Input(trust providerallowance.AggregateTrust, publisherUID, publisherSA, kubeconfig, kubeContext string, now time.Time) HandoffInput {
	return HandoffInput{OldConfig: s.OldConfig, NewConfig: s.NewConfig, Trust: trust, PublisherUID: publisherUID, PublisherSA: publisherSA, SlotUID: s.SlotUID, ResourceVersion: s.ResourceVersion, HomeUID: s.HomeUID, KubeconfigPath: kubeconfig, KubeContext: kubeContext, Journal: s.Journal, Report: s.Report, EvictedPod: s.EvictedPod, ExpectedHelperSHA: s.HelperSHA, ExpectedJournalSHA: s.JournalSHA, ExpectedReportSHA: s.ReportSHA, ExpectedCycle: s.Cycle, ExpectedNonce: s.Nonce, Now: now}
}

// HandoffRequest is the operator's private file: one snapshot and the prepared
// hash a human approved. It carries no kubeconfig, token or provider output.
type HandoffRequest struct {
	ApprovedHash string          `json:"approvedHash"`
	Snapshot     json.RawMessage `json:"snapshot"`
}

// DecodeHandoffRequest accepts exactly the two request keys.
func DecodeHandoffRequest(raw []byte) (HandoffRequest, HandoffSnapshot, error) {
	var r HandoffRequest
	if len(raw) == 0 || len(raw) > handoffFileMax || !utf8.Valid(raw) || !providerallowance.ExactObjectKeys(raw, []string{"approvedHash", "snapshot"}) {
		return r, HandoffSnapshot{}, ErrCollector
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil || dec.Decode(new(any)) != io.EOF || !remoteDigest(r.ApprovedHash) {
		return HandoffRequest{}, HandoffSnapshot{}, ErrCollector
	}
	snap, err := DecodeHandoffSnapshot(r.Snapshot)
	if err != nil {
		return HandoffRequest{}, HandoffSnapshot{}, err
	}
	return r, snap, nil
}

// HandoffRunner applies one approved handoff from the elected operator, which
// already holds the slot's write authority. It borrows no identity and opens no
// provider connection. Every attempt is fenced by the approved hash and the
// slot's resource version, so a refusal or a retry cannot write twice.
type HandoffRunner struct {
	Approved     string
	Snapshot     HandoffSnapshot
	Running      *Config
	Trust        providerallowance.AggregateTrust
	PublisherUID string
	PublisherSA  string
	// Client must be uncached: ApplyHandoffInSlot reads the live slot and host.
	Client client.Client
	Now    func() time.Time
	Retry  time.Duration
	// Logf receives fixed status words only, never bytes from the slot.
	Logf func(string)
}

func (*HandoffRunner) NeedLeaderElection() bool { return true }

func (r *HandoffRunner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *HandoffRunner) logf(s string) {
	if r.Logf != nil {
		r.Logf(s)
	}
}

// Attempt makes one fenced attempt. It refuses unless the new configuration in
// the snapshot is exactly the configuration this process is running.
func (r *HandoffRunner) Attempt(ctx context.Context) error {
	if r == nil || r.Running == nil || r.Client == nil {
		return ErrCollector
	}
	in := r.Snapshot.Input(r.Trust, r.PublisherUID, r.PublisherSA, OperatorKubeconfigPath, OperatorKubeContext, r.now())
	next, err := ParseConfig(in.NewConfig, in.Trust, in.PublisherUID, in.PublisherSA, in.Now)
	if err != nil || next.fingerprint != r.Running.fingerprint || !next.until.Equal(r.Running.until) || next.Host != r.Running.Host || next.SlotUID != r.Running.SlotUID {
		return ErrCollector
	}
	return applyHandoff(ctx, r.Client, in, r.Approved, "", r.now)
}

// Start retries until the handoff is acknowledged, the new window ends or the
// operator stops. A returned error would stop the manager, so none is returned.
func (r *HandoffRunner) Start(ctx context.Context) error {
	if r == nil || r.Running == nil {
		return nil
	}
	wait := r.Retry
	if wait <= 0 {
		wait = time.Minute
	}
	for ctx.Err() == nil && r.now().Before(r.Running.until) {
		err := r.Attempt(ctx)
		switch {
		case err == nil:
			r.logf("allowance handoff acknowledged")
			return nil
		case errors.Is(err, ErrHandoffHash):
			r.logf("allowance handoff refused: approved hash does not match; prepare it again")
		case errors.Is(err, ErrHandoffStale):
			r.logf("allowance handoff refused: slot changed since the snapshot; prepare it again")
		default:
			r.logf("allowance handoff refused")
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	return nil
}
