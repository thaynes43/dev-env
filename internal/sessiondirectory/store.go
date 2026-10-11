// Package sessiondirectory retains bounded original metadata and CAS witnesses.
// It performs no provider invocation, execution, payload read or lifecycle write.
package sessiondirectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

var ErrUnavailable = errors.New("original session directory is unconfirmed; preserved")
var ErrNotFound = errors.New("session directory not found")
var namePattern = regexp.MustCompile(`^session-directory\.[a-f0-9]{64}$`)

func ValidName(name string) bool { return namePattern.MatchString(name) }
func Name(source coord.SessionIdentity) string {
	raw, _ := json.Marshal([]string{source.Namespace, source.UID})
	h := sha256.Sum256(raw)
	return "session-directory." + hex.EncodeToString(h[:])
}

func bounded(value string, max int) bool {
	return len(value) <= max && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}
func boundedFields(v reflect.Value) bool {
	if v.Kind() == reflect.Pointer {
		return v.IsNil() || boundedFields(v.Elem())
	}
	if v.Type() == reflect.TypeFor[metav1.Time]() {
		return true
	}
	switch v.Kind() {
	case reflect.String:
		max := 256
		if v.Type() == reflect.TypeFor[coord.DirectoryImage]() {
			max = 512
		}
		return bounded(v.String(), max)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !boundedFields(v.Field(i)) {
				return false
			}
		}
	}
	return true
}

func ValidSpec(s coord.SessionDirectorySpec) bool {
	if !boundedFields(reflect.ValueOf(s.Request)) || !boundedFields(reflect.ValueOf(s.Revisions)) ||
		s.Version != 1 || len(validation.IsDNS1123Label(s.Source.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(s.Source.Name)) != 0 ||
		s.Source.UID == "" || !bounded(s.Source.UID, 128) || s.SourceCreatedAt.IsZero() || len(validation.IsDNS1123Label(s.Project)) != 0 ||
		s.Repository == "" || !bounded(string(s.Repository), 256) || s.Base == "" || !bounded(string(s.Base), 256) ||
		!bounded(s.Parent, 317) || !bounded(string(s.TaskKey), 256) || s.Request.CLI == "" || s.Request.Model == "" ||
		!commonrules.ValidDigest(string(s.Revisions.Catalog)) || !commonrules.ValidDigest(string(s.Revisions.Rules)) || !commonrules.ValidDigest(string(s.Revisions.ProjectSnapshot)) ||
		s.Revisions.CatalogUID == "" || s.Revisions.CatalogResourceVersion == "" || s.Revisions.Template == "" || s.Revisions.Image == "" {
		return false
	}
	if s.Request.Provider == "local" {
		if s.Request.Local == nil || s.Request.Local.Pool == "" || s.Request.Local.Model == "" || s.Request.Local.RuntimeAdapter != "Disabled" {
			return false
		}
	} else if s.Request.Local != nil || (s.Request.Provider != "claude" && s.Request.Provider != "codex" && s.Request.Provider != "Unknown") {
		return false
	}
	c := s.Revisions.Common
	if c.State == "Unknown" {
		return c.Revision == "" && c.SnapshotDigest == "" && c.Common == nil && c.Header == nil
	}
	return c.State == "Bound" && commonrules.ValidDigest(string(c.Revision)) && commonrules.ValidDigest(string(c.SnapshotDigest)) &&
		validCommon(c.Common, commonrules.CommonName, commonrules.CommonKey) && validCommon(c.Header, commonrules.HeaderName, commonrules.HeaderKey)
}
func validCommon(s *coord.DirectoryCommonSource, name, key string) bool {
	return s != nil && s.Namespace == commonrules.Namespace && string(s.Name) == name && string(s.Key) == key && s.UID != "" && s.ResourceVersion != "" && commonrules.ValidDigest(string(s.SHA256))
}

// Spec uses only the accepted immutable Session binding, never current inputs.
func Spec(s *v1alpha1.AgentSession) (coord.SessionDirectorySpec, error) {
	var out coord.SessionDirectorySpec
	if s == nil || s.Spec.SessionDirectory == nil || s.Spec.SessionDirectory.Version != 1 || s.UID == "" || s.CreationTimestamp.IsZero() {
		return out, ErrUnavailable
	}
	r, err := projectcanary.BoundRecord(s)
	if err != nil || s.Spec.SessionDirectory.Project != r.Project {
		return out, ErrUnavailable
	}
	b := s.Spec.ProjectCanary
	out = coord.SessionDirectorySpec{Version: 1, Source: coord.SessionIdentity{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)}, SourceCreatedAt: s.CreationTimestamp,
		Project: r.Project, Repository: coord.DirectoryText(s.Spec.Repo), Base: coord.DirectoryText(s.Spec.Base), Parent: s.Spec.Parent, TaskKey: coord.DirectoryText("canary:" + r.ID),
		Request:   coord.DirectoryRequest{Provider: r.Provider, CLI: coord.DirectoryText(s.Spec.Agent), Model: coord.DirectoryText(s.Spec.Model), Effort: coord.DirectoryText(s.Spec.Effort)},
		Revisions: coord.DirectoryRevisions{CatalogUID: coord.DirectoryText(b.CatalogUID), CatalogResourceVersion: coord.DirectoryText(b.CatalogResourceVersion), Catalog: coord.DirectoryDigest(b.CatalogRevision), Rules: coord.DirectoryDigest(b.RulesRevision), Template: coord.DirectoryText(b.TemplateRevision), Image: coord.DirectoryImage(b.Image), ProjectSnapshot: coord.DirectoryDigest(b.SnapshotDigest), Common: coord.DirectoryCommon{State: "Unknown"}}}
	if s.Spec.CommonRules != nil {
		snapshot, err := commonrules.BoundSession(s)
		if err != nil {
			return coord.SessionDirectorySpec{}, ErrUnavailable
		}
		metadata := func(source commonrules.Source) *coord.DirectoryCommonSource {
			return &coord.DirectoryCommonSource{Namespace: coord.DirectoryText(source.Namespace), Name: coord.DirectoryText(source.Name), Key: coord.DirectoryText(source.Key), UID: coord.DirectoryText(source.UID), ResourceVersion: coord.DirectoryText(source.ResourceVersion), SHA256: coord.DirectoryDigest(source.SHA256)}
		}
		out.Revisions.Common = coord.DirectoryCommon{State: "Bound", Revision: coord.DirectoryDigest(snapshot.CommonRevision), SnapshotDigest: coord.DirectoryDigest(s.Spec.CommonRules.SnapshotDigest), Common: metadata(snapshot.Common), Header: metadata(snapshot.Header)}
	}
	if !ValidSpec(out) {
		return coord.SessionDirectorySpec{}, ErrUnavailable
	}
	return out, nil
}

type KubeStore struct {
	Client    client.Client
	Live      client.Reader
	Namespace string
}

func (s KubeStore) ready() bool {
	return s.Client != nil && s.Live != nil && len(validation.IsDNS1123Label(s.Namespace)) == 0
}
func (s KubeStore) valid(d *coord.SessionDirectory) bool {
	return d.Namespace == s.Namespace && d.Name == Name(d.Spec.Source) && ValidSpec(d.Spec) && d.UID != "" && d.ResourceVersion != "" && !d.CreationTimestamp.IsZero() && d.DeletionTimestamp.IsZero() && len(d.OwnerReferences) == 0 && len(d.Finalizers) == 0 && len(d.Annotations) == 0 && len(d.Labels) == 1 && d.Labels[taskclaim.ProjectLabel] == d.Spec.Project && validStatus(d.Status)
}
func validStatus(status coord.SessionDirectoryStatus) bool {
	if reflect.DeepEqual(status, coord.SessionDirectoryStatus{}) {
		return true
	}
	if status.SourceState != "SourceMissing" && status.SourceState != "Unknown" && status.SourceState != "Present" {
		return false
	}
	if status.ObservedAt.IsZero() || status.StopState != "Unknown" || status.ActualModelState != "Unknown" || status.UsageState != "Unknown" || status.CostState != "Unknown" {
		return false
	}
	w := status.LastWitness
	return w == nil || (boundedFields(reflect.ValueOf(*w)) && w.SourceGeneration >= 1 && w.SourceResourceVersion != "" && w.PodUID != "" && w.PodName != "" && !w.ObservedAt.IsZero() && (w.PodPhase == "Pending" || w.PodPhase == "Running" || w.PodPhase == "Succeeded" || w.PodPhase == "Failed" || w.PodPhase == "Unknown"))
}
func (s KubeStore) Get(ctx context.Context, name string) (*coord.SessionDirectory, error) {
	if !s.ready() || !ValidName(name) {
		return nil, ErrUnavailable
	}
	var d coord.SessionDirectory
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &d); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	if !s.valid(&d) {
		return nil, ErrUnavailable
	}
	return &d, nil
}

// Ensure independently confirms one exact prospective record. It never repairs
// missing history after an executor attempt, including a lost create ACK.
func (s KubeStore) Ensure(ctx context.Context, source *v1alpha1.AgentSession) error {
	if !s.ready() {
		return ErrUnavailable
	}
	var fresh v1alpha1.AgentSession
	if s.Live.Get(ctx, client.ObjectKeyFromObject(source), &fresh) != nil || fresh.UID != source.UID || fresh.Generation != source.Generation || !fresh.DeletionTimestamp.IsZero() {
		return ErrUnavailable
	}
	spec, err := Spec(&fresh)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(spec)
	digest, name := commonrules.Digest(raw), Name(spec.Source)
	receipt := fresh.Status.SessionDirectory
	if receipt != nil && (receipt.Name != name || receipt.SpecDigest != digest || (receipt.State != "Preparing" && receipt.State != "Confirmed") || (receipt.State == "Confirmed" && receipt.UID == "")) {
		return ErrUnavailable
	}
	d, err := s.Get(ctx, name)
	if err == nil {
		if !reflect.DeepEqual(d.Spec, spec) || receipt == nil || (receipt.UID != "" && receipt.UID != string(d.UID)) {
			return ErrUnavailable
		}
		if receipt.State == "Confirmed" {
			*source = fresh
			return nil
		}
		receipt.State, receipt.UID = "Confirmed", string(d.UID)
		if s.writeReceipt(ctx, &fresh) != nil {
			return ErrUnavailable
		}
		*source = fresh
		return nil
	}
	if !errors.Is(err, ErrNotFound) || receipt != nil || fresh.Status.PrivateHistory == nil || fresh.Status.PrivateHistory.ExecutorAttempted || fresh.Status.PrivateHistory.Admission == "Unknown" || fresh.Status.PrivateHistory.LastExecutor != nil || fresh.Status.PodName != "" || fresh.Status.Agent != nil {
		return ErrUnavailable
	}
	var pod corev1.Pod
	if !apierrors.IsNotFound(s.Live.Get(ctx, client.ObjectKeyFromObject(&fresh), &pod)) {
		return ErrUnavailable
	}
	fresh.Status.SessionDirectory = &v1alpha1.SessionDirectoryReceipt{State: "Preparing", Name: name, SpecDigest: digest}
	if s.writeReceipt(ctx, &fresh) != nil {
		return ErrUnavailable
	}
	d = &coord.SessionDirectory{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: name, Labels: map[string]string{taskclaim.ProjectLabel: spec.Project}}, Spec: spec}
	createErr := s.Client.Create(ctx, d)
	confirmed, err := s.Get(ctx, name)
	if err != nil || !reflect.DeepEqual(confirmed.Spec, spec) || (createErr == nil && confirmed.UID != d.UID) {
		return ErrUnavailable
	}
	fresh.Status.SessionDirectory.State, fresh.Status.SessionDirectory.UID = "Confirmed", string(confirmed.UID)
	if s.writeReceipt(ctx, &fresh) != nil {
		return ErrUnavailable
	}
	*source = fresh
	return nil
}
func (s KubeStore) writeReceipt(ctx context.Context, source *v1alpha1.AgentSession) error {
	if s.Client.Status().Update(ctx, source) != nil {
		return ErrUnavailable
	}
	var confirmed v1alpha1.AgentSession
	if s.Live.Get(ctx, client.ObjectKeyFromObject(source), &confirmed) != nil || confirmed.UID != source.UID || confirmed.ResourceVersion != source.ResourceVersion || !reflect.DeepEqual(confirmed.Status.SessionDirectory, source.Status.SessionDirectory) {
		return ErrUnavailable
	}
	*source = confirmed
	return nil
}

func (s KubeStore) List(ctx context.Context, project string, limit int64, cursor string) ([]coord.SessionDirectory, string, string, error) {
	if !s.ready() || len(validation.IsDNS1123Label(project)) != 0 || limit < 1 || limit > 20 || len(cursor) > 4096 {
		return nil, "", "", ErrUnavailable
	}
	var list coord.SessionDirectoryList
	if s.Live.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingLabels{taskclaim.ProjectLabel: project}, client.Limit(limit), client.Continue(cursor)) != nil || int64(len(list.Items)) > limit || len(list.Continue) > 4096 {
		return nil, "", "", ErrUnavailable
	}
	for i := range list.Items {
		if !s.valid(&list.Items[i]) || list.Items[i].Spec.Project != project {
			return nil, "", "", ErrUnavailable
		}
	}
	return list.Items, list.Continue, list.ResourceVersion, nil
}

type Current struct {
	SourceState           string      `json:"sourceState"`
	PlacementState        string      `json:"placementState"`
	ObservedAt            metav1.Time `json:"observedAt"`
	SourceResourceVersion string      `json:"sourceResourceVersion,omitempty"`
	// +optional
	Witness   *coord.DirectoryWitness `json:"witness,omitempty"`
	StopState string                  `json:"stopState"`
}

// Current observes exact original identity. Archived or missing executors are
// Unknown placement, never StopConfirmed; no persisted witness is inferred.
func (s KubeStore) Current(ctx context.Context, d *coord.SessionDirectory, now time.Time) Current {
	out := Current{SourceState: "Unknown", PlacementState: "Unknown", ObservedAt: metav1.NewTime(now.UTC()), StopState: "Unknown"}
	var source v1alpha1.AgentSession
	err := s.Live.Get(ctx, client.ObjectKey{Namespace: d.Spec.Source.Namespace, Name: d.Spec.Source.Name}, &source)
	if apierrors.IsNotFound(err) {
		out.SourceState = "SourceMissing"
		return out
	}
	if err != nil || string(source.UID) != d.Spec.Source.UID || !source.DeletionTimestamp.IsZero() {
		return out
	}
	spec, err := Spec(&source)
	if err != nil || !reflect.DeepEqual(spec, d.Spec) {
		return out
	}
	receipt := source.Status.SessionDirectory
	raw, _ := json.Marshal(d.Spec)
	if receipt == nil || receipt.State != "Confirmed" || receipt.Name != d.Name || receipt.UID != string(d.UID) || receipt.SpecDigest != commonrules.Digest(raw) {
		return out
	}
	out.SourceState, out.SourceResourceVersion = "Present", source.ResourceVersion
	h := source.Status.PrivateHistory
	if h == nil || h.SessionUID != string(source.UID) || !h.ExecutorAttempted || h.ExecutorIntent == "" || h.HomeUID == "" || h.LastExecutor == nil || h.LastExecutor.PodUID == "" || source.Status.Phase == v1alpha1.PhaseArchived {
		return out
	}
	var pod corev1.Pod
	if s.Live.Get(ctx, client.ObjectKey{Namespace: source.Namespace, Name: h.LastExecutor.PodName}, &pod) != nil || pod.UID == "" || string(pod.UID) != h.LastExecutor.PodUID || !pod.DeletionTimestamp.IsZero() || !metav1.IsControlledBy(&pod, &source) || pod.Labels[v1alpha1.LabelHold] == "true" || pod.Annotations[v1alpha1.LabelPrefix+"private-history-executor-intent"] != h.ExecutorIntent || pod.Annotations[v1alpha1.LabelPrefix+"private-history-home-uid"] != h.HomeUID || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != string(d.Spec.Revisions.Image) || pod.Labels[v1alpha1.LabelRevision] != string(d.Spec.Revisions.Template) {
		return out
	}
	w := &coord.DirectoryWitness{SourceGeneration: source.Generation, SourceResourceVersion: coord.DirectoryText(source.ResourceVersion), ObservedAt: out.ObservedAt, PodName: coord.DirectoryText(pod.Name), PodUID: string(pod.UID), Node: coord.DirectoryText(pod.Spec.NodeName), Template: coord.DirectoryText(pod.Labels[v1alpha1.LabelRevision]), Image: coord.DirectoryImage(pod.Spec.Containers[0].Image), PodPhase: string(pod.Status.Phase)}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			w.Ready = true
		}
	}
	if a := source.Status.Agent; a != nil {
		w.ReportedState, w.DeclaredConversationID = coord.DirectoryText(a.Status), coord.DirectoryText(a.ConversationID)
		w.ReportedHeartbeatAt = a.LastHeartbeat.DeepCopy()
		if a.Task != nil {
			w.ReportedTaskFinishedAt = a.Task.FinishedAt.DeepCopy()
			w.ReportedTask = &coord.DirectoryTaskReport{ExitCode: a.Task.ExitCode, FinishedAt: a.Task.FinishedAt, TimedOut: a.Task.TimedOut, Subtype: coord.DirectoryText(a.Task.Subtype), IsError: a.Task.IsError}
		}
	}
	if !validStatus(coord.SessionDirectoryStatus{LastWitness: w, SourceState: "Present", ObservedAt: out.ObservedAt, StopState: "Unknown", ActualModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"}) {
		return out
	}
	out.PlacementState, out.Witness = "OwnedOriginalPod", w
	return out
}

// Observe reads record RV first, then fresh source/Pod, then makes one CAS write.
// A conflict or unknown write acknowledgement is never retried with this witness.
func (s KubeStore) Observe(ctx context.Context, name string, now time.Time) error {
	d, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	current := s.Current(ctx, d, now)
	next := d.Status.DeepCopy()
	if current.Witness != nil {
		if next.LastWitness != nil && (next.LastWitness.PodUID != current.Witness.PodUID || current.Witness.ObservedAt.Before(&next.LastWitness.ObservedAt)) {
			return ErrUnavailable
		}
		next.LastWitness = current.Witness
	}
	next.SourceState, next.ObservedAt = current.SourceState, current.ObservedAt
	next.StopState, next.ActualModelState, next.UsageState, next.CostState = "Unknown", "Unknown", "Unknown", "Unknown"
	d.Status = *next
	if err := s.Client.Status().Update(ctx, d); err != nil {
		return ErrUnavailable
	}
	confirmed, err := s.Get(ctx, name)
	if err != nil || confirmed.UID != d.UID || confirmed.ResourceVersion != d.ResourceVersion || !reflect.DeepEqual(confirmed.Status, d.Status) {
		return ErrUnavailable
	}
	return nil
}
