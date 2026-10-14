package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func coordArchiveFixture(t *testing.T, resource string) (CoordArchiveManifest, []byte) {
	t.Helper()
	kinds := map[string][2]string{
		"taskclaims": {"task-claim", "TaskClaim"}, "publishedcontexts": {"published-context", "PublishedContext"},
		"inboxmessages": {"message", "InboxMessage"}, "inboxreadacks": {"read-ack", "InboxReadAck"},
	}
	kind := kinds[resource]
	m := CoordArchiveManifest{Format: CoordArchiveFormat, Resource: resource, Kind: kind[1], APIVersion: CoordArchiveAPIVersion,
		Namespace: "dev-env-system", Name: kind[0] + "." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32),
		UID: "11111111-1111-1111-1111-111111111111", ResourceVersion: "1234", Project: "dev-env", ArchivedBy: "dev-env-operator test"}
	spec := map[string]any{"project": m.Project, "futureSpecField": map[string]any{"text": "<>&"}}
	if resource == "taskclaims" {
		delete(spec, "project")
		spec["identity"] = map[string]any{"project": m.Project}
	}
	if resource == "inboxmessages" || resource == "inboxreadacks" {
		m.RecipientUID = "22222222-2222-2222-2222-222222222222"
		spec["recipient"] = map[string]any{"uid": m.RecipientUID}
	}
	if resource == "inboxreadacks" {
		m.MessageUID = "33333333-3333-3333-3333-333333333333"
		spec["messageUID"] = m.MessageUID
	}
	object := map[string]any{"apiVersion": m.APIVersion, "kind": m.Kind,
		"metadata": map[string]any{"namespace": m.Namespace, "name": m.Name, "uid": m.UID, "resourceVersion": m.ResourceVersion,
			"futureMetadata": json.Number("90071992547409931234567890")},
		"spec": spec, "futureObjectField": map[string]any{"decimal": json.Number("1.2300e+04")}}
	raw, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	bindCoordArchiveFixture(t, &m, raw)
	return m, raw
}

func bindCoordArchiveFixture(t *testing.T, m *CoordArchiveManifest, raw []byte) {
	t.Helper()
	canonical, err := CanonicalJSON(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	m.Object = CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(canonical))}
}

func requireCoordArchiveInvalid(t *testing.T, m CoordArchiveManifest, r io.Reader) {
	t.Helper()
	data, err := VerifyCoordArchiveObject(m, r)
	var refusal *HistoryError
	if data != nil || !errors.As(err, &refusal) || refusal.Code != HistoryErrInvalid {
		t.Fatalf("want invalid without object bytes, got %d bytes and %v", len(data), err)
	}
}

func TestCoordArchivePreservesFullObjectsAcrossAllFourKinds(t *testing.T) {
	for _, resource := range []string{"taskclaims", "publishedcontexts", "inboxmessages", "inboxreadacks"} {
		t.Run(resource, func(t *testing.T) {
			m, raw := coordArchiveFixture(t, resource)
			got, err := VerifyCoordArchiveObject(m, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`"futureMetadata":90071992547409931234567890`, `"decimal":1.2300e+04`, `"text":"<>&"`, `"futureObjectField":`} {
				if !bytes.Contains(got, []byte(want)) {
					t.Fatalf("lost future field, numeric precision or encoding %s", want)
				}
			}
			expected, err := CanonicalJSON(json.RawMessage(raw))
			if err != nil || !bytes.Equal(got, expected) {
				t.Fatalf("full canonical bytes differ: %v", err)
			}
		})
	}
}

func TestCoordArchiveRefusesIdentityAndDigestSubstitution(t *testing.T) {
	m, raw := coordArchiveFixture(t, "inboxreadacks")
	for _, field := range []string{"apiVersion", "kind", "namespace", "name", "uid", "resourceVersion", "project", "recipientUID", "messageUID", "sha256", "bytes"} {
		t.Run(field, func(t *testing.T) {
			changed := m
			switch field {
			case "apiVersion":
				changed.APIVersion = "other/v1"
			case "kind":
				changed.Kind = "InboxMessage"
			case "namespace":
				changed.Namespace = "other"
			case "name":
				changed.Name = strings.Replace(m.Name, "aaaaaaaa", "cccccccc", 1)
			case "uid":
				changed.UID = "44444444-4444-4444-4444-444444444444"
			case "resourceVersion":
				changed.ResourceVersion = "5678"
			case "project":
				changed.Project = "other"
			case "recipientUID":
				changed.RecipientUID = "44444444-4444-4444-4444-444444444444"
			case "messageUID":
				changed.MessageUID = "44444444-4444-4444-4444-444444444444"
			case "sha256":
				changed.Object.SHA256 = strings.Repeat("0", 64)
			case "bytes":
				changed.Object.Bytes++
			}
			requireCoordArchiveInvalid(t, changed, bytes.NewReader(raw))
		})
	}
}

func TestCoordArchiveRefusesUnsafeOrInconsistentManifest(t *testing.T) {
	m, raw := coordArchiveFixture(t, "taskclaims")
	for _, mutate := range []func(*CoordArchiveManifest){
		func(m *CoordArchiveManifest) { m.Format = "other" },
		func(m *CoordArchiveManifest) { m.Resource = "agentsessions" },
		func(m *CoordArchiveManifest) { m.Name = "../outside" },
		func(m *CoordArchiveManifest) { m.Name = strings.Replace(m.Name, "task-claim", "message", 1) },
		func(m *CoordArchiveManifest) { m.Namespace = "../outside" },
		func(m *CoordArchiveManifest) { m.UID = "../outside" },
		func(m *CoordArchiveManifest) { m.ResourceVersion = "../../outside" },
		func(m *CoordArchiveManifest) { m.Project = "" },
		func(m *CoordArchiveManifest) { m.RecipientUID = "unexpected" },
		func(m *CoordArchiveManifest) { m.MessageUID = "unexpected" },
		func(m *CoordArchiveManifest) { m.Object.SHA256 = "short" },
		func(m *CoordArchiveManifest) { m.Object.Bytes = 0 },
		func(m *CoordArchiveManifest) { m.Object.Bytes = MaxCoordArchiveObjectBytes + 1 },
		func(m *CoordArchiveManifest) { m.ArchivedBy = "" },
	} {
		changed := m
		mutate(&changed)
		requireCoordArchiveInvalid(t, changed, bytes.NewReader(raw))
	}
}

func TestCoordArchiveRefusesMalformedOrMultipleObjects(t *testing.T) {
	m, raw := coordArchiveFixture(t, "taskclaims")
	for _, input := range [][]byte{nil, []byte("null"), []byte("[]"), []byte("{"), []byte(`{"number":NaN}`),
		{0xff}, append(append([]byte(nil), raw...), []byte(" {}")...), append(append([]byte(nil), raw...), 'x')} {
		requireCoordArchiveInvalid(t, m, bytes.NewReader(input))
	}
	requireCoordArchiveInvalid(t, m, nil)
}

type countedCoordArchiveReader struct {
	io.Reader
	bytes int
}

func (r *countedCoordArchiveReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += n
	return n, err
}

func TestCoordArchiveLimitsReaderBeforeDecoding(t *testing.T) {
	m, raw := coordArchiveFixture(t, "taskclaims")
	reader := &countedCoordArchiveReader{Reader: strings.NewReader(strings.Repeat(" ", MaxCoordArchiveRequestBytes+100))}
	requireCoordArchiveInvalid(t, m, reader)
	if reader.bytes != MaxCoordArchiveRequestBytes+1 {
		t.Fatalf("read %d bytes, want exactly finite cap plus overflow byte", reader.bytes)
	}
	invalid := m
	invalid.Object.Bytes = MaxCoordArchiveObjectBytes + 1
	reader = &countedCoordArchiveReader{Reader: bytes.NewReader(raw)}
	requireCoordArchiveInvalid(t, invalid, reader)
	if reader.bytes != 0 {
		t.Fatalf("read %d bytes after manifest refusal", reader.bytes)
	}
}

func TestCoordArchiveCanonicalSizeBoundary(t *testing.T) {
	m, raw := coordArchiveFixture(t, "publishedcontexts")
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["padding"] = json.RawMessage(`""`)
	base, err := CanonicalJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []int{0, 1} {
		object["padding"], err = json.Marshal(strings.Repeat("a", MaxCoordArchiveObjectBytes-len(base)+extra))
		if err != nil {
			t.Fatal(err)
		}
		input, err := CanonicalJSON(object)
		if err != nil {
			t.Fatal(err)
		}
		bindCoordArchiveFixture(t, &m, input)
		if extra == 0 {
			got, err := VerifyCoordArchiveObject(m, bytes.NewReader(input))
			if err != nil || len(got) != MaxCoordArchiveObjectBytes {
				t.Fatalf("exact boundary: got %d bytes, %v", len(got), err)
			}
		} else {
			// A lying in-bound declaration still cannot return an oversized body.
			m.Object.Bytes = MaxCoordArchiveObjectBytes
			requireCoordArchiveInvalid(t, m, bytes.NewReader(input))
		}
	}
}
