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
	var out protocol.CoordArchivePutResult
	cmd := []string{"agentd", "ctl", "coord-archive", "put"}
	if mark {
		cmd = append(cmd, "--mark")
	}
	if err := c.run(ctx, cmd, bytes.NewReader(body), &out); err != nil {
		return zero, err
	}
	if !sameManifest(out.Manifest.CoordArchiveManifest, req.Manifest) || !utcTime(out.Manifest.ArchivedAt) {
		return zero, errors.New("coordination archive put returned an invalid manifest")
	}
	return out, nil
}

func (c *CoordinationArchive) Mark(ctx context.Context, req []protocol.CoordArchiveMarkRequest) ([]protocol.CoordArchiveMarkResult, error) {
	if len(req) == 0 || len(req) > protocol.MaxCoordArchiveLookupItems {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "bounded coordination mark batch required")
	}
	for _, item := range req {
		if _, err := item.CoordArchiveIdentity.RecordDir(); err != nil || item.Event.Validate(item.Resource) != nil || (item.Event.State != "present" && item.Event.State != "reset") {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "invalid coordination mark request")
		}
	}
	body, err := protocol.CanonicalJSON(req)
	if err != nil || len(body) > protocol.MaxCoordArchiveRequestBytes {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "bounded coordination mark batch required")
	}
	var out []protocol.CoordArchiveMarkResult
	if err := c.run(ctx, []string{"agentd", "ctl", "coord-archive", "mark"}, bytes.NewReader(body), &out); err != nil {
		return nil, err
	}
	if len(out) != len(req) {
		return nil, errors.New("coordination archive mark response is incomplete")
	}
	for i := range out {
		if out[i].CoordArchiveIdentity != req[i].CoordArchiveIdentity {
			return nil, errors.New("coordination archive mark response identity differs")
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
	var out []protocol.CoordArchiveLookupResult
	if err := c.run(ctx, []string{"agentd", "ctl", "coord-archive", "lookup"}, bytes.NewReader(body), &out); err != nil {
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
	var zero protocol.CoordArchiveGetResult
	if err := q.Validate(); err != nil {
		return zero, err
	}
	cmd := []string{"agentd", "ctl", "coord-archive", "get", "--resource", q.Resource, "--name", q.Name}
	if q.UID != "" {
		cmd = append(cmd, "--uid", q.UID)
	}
	if q.ResourceVersion != "" {
		cmd = append(cmd, "--resource-version", q.ResourceVersion, "--sha256", q.SHA256)
	}
	var out protocol.CoordArchiveGetResult
	if err := c.run(ctx, cmd, nil, &out); err != nil {
		return zero, err
	}
	m := out.Manifest.CoordArchiveManifest
	if !utcTime(out.Manifest.ArchivedAt) || m.Resource != q.Resource || m.Name != q.Name || (q.UID != "" && m.UID != q.UID) || (q.ResourceVersion != "" && (m.ResourceVersion != q.ResourceVersion || m.Object.SHA256 != q.SHA256)) {
		return zero, errors.New("coordination archive get response does not match its selector")
	}
	if _, err := protocol.VerifyCoordArchiveObject(m, bytes.NewReader(out.Object)); err != nil {
		return zero, fmt.Errorf("coordination archive get response refused: %w", err)
	}
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
	var out protocol.CoordArchiveListResult
	if err := c.run(ctx, cmd, nil, &out); err != nil {
		return zero, err
	}
	if len(out.Items) > q.Limit {
		return zero, errors.New("coordination archive list exceeds its requested limit")
	}
	for _, item := range out.Items {
		if item.UID == "" || (item.Error == "" && (item.Manifest == nil || !utcTime(item.Manifest.ArchivedAt) || item.Manifest.UID != item.UID || item.Manifest.Resource != q.Resource || item.Manifest.Project != q.Project)) {
			return zero, errors.New("coordination archive list response is incomplete")
		}
	}
	return out, nil
}

func (c *CoordinationArchive) run(ctx context.Context, cmd []string, stdin io.Reader, out any) error {
	s := c.Shelf
	if s == nil || s.Exec == nil || s.Reader == nil {
		return errors.New("this operator cannot exec into the shelf")
	}
	pod, err := s.Pod(ctx)
	if err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr := &capped{max: maxOutput}, &capped{max: 4 << 10}
	err = s.Exec.Run(ctx, s.Namespace, pod, Container, cmd, stdin, stdout, stderr)
	if stdout.over {
		return fmt.Errorf("coordination archive in shelf pod %s printed more than %d bytes", pod, maxOutput)
	}
	if err != nil {
		var exit utilexec.ExitError
		var refusal protocol.HistoryError
		if errors.As(err, &exit) && exit.ExitStatus() == 1 && decodeOne(stdout.Bytes(), &refusal) == nil && refusal.Code != "" {
			return &refusal
		}
		if strings.Contains(strings.ToLower(stderr.String()), "unknown command") {
			return protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "coordination archive is unavailable")
		}
		return fmt.Errorf("coordination archive in shelf pod %s: %w", pod, err)
	}
	if err := decodeOne(stdout.Bytes(), out); err != nil {
		return fmt.Errorf("coordination archive in shelf pod %s: answer is not one complete JSON value: %w", pod, err)
	}
	return nil
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
func utcTime(t time.Time) bool                             { return !t.IsZero() && t.Location() == time.UTC }
func sameManifest(a, b protocol.CoordArchiveManifest) bool { return a == b }
