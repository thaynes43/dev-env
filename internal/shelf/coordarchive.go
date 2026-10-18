package shelf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// CoordinationArchive is the typed operator-side transport for the shelf's
// explicitly configured coordination archive. Callers own ExecSlots: this
// client neither creates nor acquires a slot.
type CoordinationArchive struct{ Shelf *Shelf }

func (c *CoordinationArchive) Put(ctx context.Context, req protocol.CoordArchivePutRequest, mark bool) (protocol.CoordArchivePutResult, error) {
	var zero protocol.CoordArchivePutResult
	if mark != (req.Event != nil) {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--mark and absent event must be supplied together")
	}
	if _, err := protocol.VerifyCoordArchiveObject(req.Manifest, bytes.NewReader(req.Object)); err != nil {
		return zero, err
	}
	if req.Event != nil && (req.Event.Validate(req.Manifest.Resource) != nil || req.Event.State != "absent") {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "invalid absent coordination event")
	}
	body, err := protocol.CanonicalJSON(req)
	if err != nil || len(body) > protocol.MaxCoordArchiveRequestBytes {
		return zero, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "bounded coordination put request required")
	}
	cmd := []string{"agentd", "ctl", "coord-archive", "put"}
	if mark {
		cmd = append(cmd, "--mark")
	}
	raw, err := c.run(ctx, cmd, bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	var out protocol.CoordArchivePutResult
	if err := decodeObject(raw, &out, "manifest"); err != nil {
		return zero, fmt.Errorf("coordination archive put response is incomplete: %w", err)
	}
	if !hasField(raw, "graceStart") {
		return zero, errors.New("coordination archive put response is incomplete")
	}
	if _, err := protocol.CoordArchiveRevisionDir(out.Manifest.CoordArchiveManifest); err != nil {
		return zero, errors.New("coordination archive put returned an invalid manifest")
	}
	if !sameManifest(out.Manifest.CoordArchiveManifest, req.Manifest) || !utcTime(out.Manifest.ArchivedAt) {
		return zero, errors.New("coordination archive put returned an invalid manifest")
	}
	if (mark && out.GraceStart == nil) || (out.GraceStart != nil && !utcTimePtr(out.GraceStart)) {
		return zero, errors.New("coordination archive put returned an invalid grace state")
	}
	return out, nil
}

func (c *CoordinationArchive) Mark(ctx context.Context, req []protocol.CoordArchiveMarkRequest) ([]protocol.CoordArchiveMarkResult, error) {
	if len(req) == 0 || len(req) > protocol.MaxCoordArchiveLookupItems {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "bounded coordination mark batch required")
	}
	for _, item := range req {
		if _, err := item.RecordDir(); err != nil || item.Event.Validate(item.Resource) != nil || (item.Event.State != "present" && item.Event.State != "reset" && item.Event.State != "hold") {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "invalid coordination mark request")
		}
	}
	body, err := protocol.CanonicalJSON(req)
	if err != nil || len(body) > protocol.MaxCoordArchiveRequestBytes {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "bounded coordination mark batch required")
	}
	raw, err := c.run(ctx, []string{"agentd", "ctl", "coord-archive", "mark"}, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out, err := decodeMarkResults(raw)
	if err != nil {
		return nil, err
	}
	if len(out) != len(req) {
		return nil, errors.New("coordination archive mark response is incomplete")
	}
	for i := range out {
		if out[i].CoordArchiveIdentity != req[i].CoordArchiveIdentity {
			return nil, errors.New("coordination archive mark response identity differs")
		}
		// A hold records that an owner was seen without clearing the old
		// grace. Present/reset clear it in the following durable step.
		if out[i].GraceStart != nil && (req[i].Event.State != "hold" || !utcTimePtr(out[i].GraceStart)) {
			return nil, errors.New("coordination archive mark response has an invalid grace state")
		}
	}
	return out, nil
}

func (c *CoordinationArchive) Lookup(ctx context.Context, ids []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error) {
	if len(ids) == 0 || len(ids) > protocol.MaxCoordArchiveLookupItems {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "bounded coordination lookup batch required")
	}
	for _, id := range ids {
		if _, err := id.RecordDir(); err != nil {
			return nil, err
		}
	}
	body, err := protocol.CanonicalJSON(ids)
	if err != nil || len(body) > protocol.MaxCoordArchiveRequestBytes {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "bounded coordination lookup batch required")
	}
	raw, err := c.run(ctx, []string{"agentd", "ctl", "coord-archive", "lookup"}, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out, err := decodeLookupResults(raw)
	if err != nil {
		return nil, err
	}
	if len(out) != len(ids) {
		return nil, errors.New("coordination archive lookup response is incomplete")
	}
	for i := range out {
		if out[i].CoordArchiveIdentity != ids[i] {
			return nil, errors.New("coordination archive lookup response identity differs")
		}
	}
	return out, nil
}

func (c *CoordinationArchive) Get(ctx context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	if err := q.Validate(); err != nil {
		return protocol.CoordArchiveGetResult{}, err
	}
	return c.get(ctx, q, "get")
}

// GetItem reads the selected archived item without allowing revision recovery pins.
func (c *CoordinationArchive) GetItem(ctx context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	if q.ResourceVersion != "" || q.SHA256 != "" {
		return protocol.CoordArchiveGetResult{}, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "item reads do not accept revision or SHA256 pins")
	}
	if err := q.Validate(); err != nil {
		return protocol.CoordArchiveGetResult{}, err
	}
	return c.get(ctx, q, "get-item")
}

func (c *CoordinationArchive) get(ctx context.Context, q protocol.CoordArchiveGetQuery, command string) (protocol.CoordArchiveGetResult, error) {
	var zero protocol.CoordArchiveGetResult
	cmd := []string{"agentd", "ctl", "coord-archive", command, "--resource", q.Resource, "--name", q.Name}
	if q.UID != "" {
		cmd = append(cmd, "--uid", q.UID)
	}
	if command == "get" && q.ResourceVersion != "" {
		cmd = append(cmd, "--resource-version", q.ResourceVersion, "--sha256", q.SHA256)
	}
	raw, err := c.run(ctx, cmd, nil)
	if err != nil {
		return zero, err
	}
	var out protocol.CoordArchiveGetResult
	if err := decodeObject(raw, &out, "manifest", "object"); err != nil {
		return zero, fmt.Errorf("coordination archive %s response is incomplete: %w", command, err)
	}
	m := out.Manifest.CoordArchiveManifest
	if !utcTime(out.Manifest.ArchivedAt) || m.Resource != q.Resource || m.Name != q.Name || (q.UID != "" && m.UID != q.UID) || (q.ResourceVersion != "" && (m.ResourceVersion != q.ResourceVersion || m.Object.SHA256 != q.SHA256)) {
		return zero, fmt.Errorf("coordination archive %s response does not match its selector", command)
	}
	if _, err := protocol.CoordArchiveRevisionDir(m); err != nil {
		return zero, fmt.Errorf("coordination archive %s response has an invalid manifest", command)
	}
	canonical, err := protocol.VerifyCoordArchiveObject(m, bytes.NewReader(out.Object))
	if err != nil {
		return zero, fmt.Errorf("coordination archive %s response refused: %w", command, err)
	}
	if !bytes.Equal(canonical, out.Object) {
		return zero, fmt.Errorf("coordination archive %s response object is not canonical", command)
	}
	out.Object = canonical
	return out, nil
}

func (c *CoordinationArchive) List(ctx context.Context, q protocol.CoordArchiveListQuery) (protocol.CoordArchiveListResult, error) {
	var zero protocol.CoordArchiveListResult
	if err := q.Validate(); err != nil {
		return zero, err
	}
	cmd := []string{"agentd", "ctl", "coord-archive", "list", "--resource", q.Resource, "--project", q.Project, "--limit", fmt.Sprint(q.Limit)}
	if q.RecipientUID != "" {
		cmd = append(cmd, "--recipient-uid", q.RecipientUID)
	}
	if q.After != "" {
		cmd = append(cmd, "--after", q.After)
	}
	raw, err := c.run(ctx, cmd, nil)
	if err != nil {
		return zero, err
	}
	out, err := decodeListResult(raw)
	if err != nil {
		return zero, err
	}
	if len(out.Items) > q.Limit {
		return zero, errors.New("coordination archive list exceeds its requested limit")
	}
	for _, item := range out.Items {
		if err := validateListItem(item, q); err != nil {
			return zero, errors.New("coordination archive list response is incomplete")
		}
	}
	if out.Next != "" {
		next := q
		next.After = out.Next
		if err := next.Validate(); err != nil {
			return zero, errors.New("coordination archive list response has an invalid cursor")
		}
	}
	return out, nil
}

func (c *CoordinationArchive) run(ctx context.Context, cmd []string, stdin io.Reader) ([]byte, error) {
	s := c.Shelf
	if s == nil || s.Exec == nil || s.Reader == nil {
		return nil, errors.New("this operator cannot exec into the shelf")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pod, err := s.Pod(ctx)
	if err != nil {
		return nil, err
	}
	timeout := s.Timeout
	if timeout <= 0 || timeout > defaultTimeout {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr := &capped{max: maxOutput}, &capped{max: 4 << 10}
	err = s.Exec.Run(ctx, s.Namespace, pod, Container, cmd, stdin, stdout, stderr)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if stdout.over || stderr.over {
		return nil, fmt.Errorf("coordination archive in shelf pod %s exceeded its output limit", pod)
	}
	if err != nil {
		var exit utilexec.ExitError
		var refusal protocol.HistoryError
		if errors.As(err, &exit) && exit.ExitStatus() == 1 && decodeObject(stdout.Bytes(), &refusal, "error", "message") == nil && knownRefusal(refusal.Code) && refusal.Message != "" {
			return nil, &refusal
		}
		if errors.As(err, &exit) && exit.ExitStatus() == 2 && strings.Contains(stderr.String(), "usage: ctl status | rescue") {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "coordination archive is unavailable")
		}
		return nil, fmt.Errorf("coordination archive in shelf pod %s: %w", pod, err)
	}
	return stdout.Bytes(), nil
}

func decodeObject(raw []byte, out any, required ...string) error {
	var fields map[string]json.RawMessage
	if err := decodeOne(raw, &fields); err != nil || fields == nil {
		if err != nil {
			return err
		}
		return errors.New("object required")
	}
	for _, key := range required {
		if value, ok := fields[key]; !ok || bytes.Equal(value, []byte("null")) {
			return fmt.Errorf("%s required", key)
		}
	}
	return decodeOne(raw, out)
}

func decodeOne(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}
func utcTime(t time.Time) bool     { return !t.IsZero() && t.Location() == time.UTC }
func utcTimePtr(t *time.Time) bool { return t != nil && utcTime(*t) }
func sameManifest(a, b protocol.CoordArchiveManifest) bool {
	// The shelf preserves the first publisher across idempotent retries.
	a.ArchivedBy, b.ArchivedBy = "", ""
	return a == b
}

func knownRefusal(code string) bool {
	switch code {
	case protocol.HistoryErrNotFound, protocol.HistoryErrAmbiguous, protocol.HistoryErrConflict, protocol.HistoryErrCorrupt, protocol.HistoryErrInvalid, protocol.HistoryErrNoArchive:
		return true
	}
	return false
}

func decodeMarkResults(raw []byte) ([]protocol.CoordArchiveMarkResult, error) {
	var items []json.RawMessage
	if err := decodeOne(raw, &items); err != nil || items == nil {
		return nil, errors.New("coordination archive mark response is incomplete")
	}
	out := make([]protocol.CoordArchiveMarkResult, len(items))
	for i := range items {
		if err := decodeObject(items[i], &out[i], "resource", "project", "name", "uid", "appended"); err != nil || !hasField(items[i], "graceStart") {
			return nil, errors.New("coordination archive mark response is incomplete")
		}
	}
	return out, nil
}

func decodeLookupResults(raw []byte) ([]protocol.CoordArchiveLookupResult, error) {
	var items []json.RawMessage
	if err := decodeOne(raw, &items); err != nil || items == nil {
		return nil, errors.New("coordination archive lookup response is incomplete")
	}
	out := make([]protocol.CoordArchiveLookupResult, len(items))
	for i := range items {
		if err := decodeObject(items[i], &out[i], "resource", "project", "name", "uid", "holds", "revisions"); err != nil || !hasField(items[i], "graceStart") || out[i].Revisions == nil {
			return nil, errors.New("coordination archive lookup response is incomplete")
		}
		for _, revision := range out[i].Revisions {
			if err := (protocol.CoordArchiveExactQuery{Resource: out[i].Resource, Name: out[i].Name, UID: out[i].UID, ResourceVersion: revision.ResourceVersion, SHA256: revision.SHA256}).Validate(); err != nil {
				return nil, errors.New("coordination archive lookup response has an invalid revision")
			}
		}
		if out[i].Holds != (len(out[i].Revisions) > 0) || (out[i].GraceStart != nil && !utcTimePtr(out[i].GraceStart)) {
			return nil, errors.New("coordination archive lookup response has an invalid hold state")
		}
	}
	return out, nil
}

func decodeListResult(raw []byte) (protocol.CoordArchiveListResult, error) {
	var fields map[string]json.RawMessage
	if err := decodeOne(raw, &fields); err != nil || fields == nil {
		return protocol.CoordArchiveListResult{}, errors.New("coordination archive list response is incomplete")
	}
	items, ok := fields["items"]
	if !ok || bytes.Equal(items, []byte("null")) {
		return protocol.CoordArchiveListResult{}, errors.New("coordination archive list response is incomplete")
	}
	var out protocol.CoordArchiveListResult
	if err := decodeOne(raw, &out); err != nil || out.Items == nil {
		return protocol.CoordArchiveListResult{}, errors.New("coordination archive list response is incomplete")
	}
	return out, nil
}

func hasField(raw []byte, key string) bool {
	var fields map[string]json.RawMessage
	return decodeOne(raw, &fields) == nil && fields != nil && fields[key] != nil
}

func validateListItem(item protocol.CoordArchiveListItem, q protocol.CoordArchiveListQuery) error {
	if !protocol.ValidKubeUID(item.UID) {
		return errors.New("invalid uid")
	}
	if item.Error != "" {
		if item.Error != "corrupt" || item.Manifest != nil || item.Object != nil {
			return errors.New("invalid corrupt item")
		}
		return nil
	}
	if item.Manifest == nil || len(item.Object) == 0 || !utcTime(item.Manifest.ArchivedAt) || item.Manifest.UID != item.UID || item.Manifest.Resource != q.Resource || item.Manifest.Project != q.Project {
		return errors.New("incomplete item")
	}
	if _, err := protocol.CoordArchiveRevisionDir(item.Manifest.CoordArchiveManifest); err != nil {
		return err
	}
	if q.RecipientUID != "" && item.Manifest.RecipientUID != q.RecipientUID {
		return errors.New("recipient differs")
	}
	var object map[string]any
	if err := decodeOne(item.Object, &object); err != nil || object == nil {
		return errors.New("projection is not an object")
	}
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return errors.New("projection has no spec")
	}
	if _, hasText := spec["text"]; hasText {
		return errors.New("projection has text")
	}
	metadata, ok := object["metadata"].(map[string]any)
	if !ok || object["apiVersion"] != item.Manifest.APIVersion || object["kind"] != item.Manifest.Kind || metadata["namespace"] != item.Manifest.Namespace || metadata["name"] != item.Manifest.Name || metadata["uid"] != item.Manifest.UID || metadata["resourceVersion"] != item.Manifest.ResourceVersion {
		return errors.New("projection identity differs")
	}
	if q.Resource == "taskclaims" {
		identity, ok := spec["identity"].(map[string]any)
		if !ok || identity["project"] != q.Project {
			return errors.New("projection project differs")
		}
	} else if spec["project"] != q.Project {
		return errors.New("projection project differs")
	}
	if q.Resource == "inboxmessages" || q.Resource == "inboxreadacks" {
		recipient, ok := spec["recipient"].(map[string]any)
		if !ok || recipient["uid"] != item.Manifest.RecipientUID {
			return errors.New("projection recipient differs")
		}
	}
	if q.Resource == "inboxreadacks" && spec["messageUID"] != item.Manifest.MessageUID {
		return errors.New("projection message differs")
	}
	return nil
}
