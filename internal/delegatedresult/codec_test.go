package delegatedresult

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func wire(s string) []byte { b, _ := json.Marshal(Request{1, s}); return b }
func decode(b []byte) (Request, error) {
	return DecodeRequest(bytes.NewReader(b), "application/json", "")
}

func TestRequestHappyAndEscapes(t *testing.T) {
	for _, raw := range []string{`{"schemaVersion":1,"answer":"done"}`, `{"answer":"\u0064one","schemaVersion":1}`, `{"schemaVersion":1,"answer":"\ud83d\ude00"}`, `{"schemaVersion":1,"answer":"\ufffd"}`, `{"schemaVersion":1,"answer":"\t\r\n"}`, `{"schemaVersion":1,"answer":"\\ud800"}`} {
		if _, err := decode([]byte(raw)); err != nil {
			t.Fatal("valid fixture rejected")
		}
	}
}

func TestRequestExactDecodedBoundary(t *testing.T) {
	for _, s := range []string{strings.Repeat("a", MaxAnswerBytes), strings.Repeat("é", MaxAnswerBytes/2), strings.Repeat("😀", MaxAnswerBytes/4)} {
		got, err := decode(wire(s))
		if err != nil || got.Answer != s {
			t.Fatal("exact boundary rejected or changed")
		}
		if _, err := decode(wire(s + "a")); !errors.Is(err, ErrInvalid) {
			t.Fatal("overflow accepted")
		}
	}
}

func TestRequestRawBoundary(t *testing.T) {
	raw := wire("a")
	raw = append(raw, bytes.Repeat([]byte(" "), MaxRawBytes-len(raw))...)
	if _, err := decode(raw); err != nil {
		t.Fatal("exact raw bound rejected")
	}
	if _, err := decode(append(raw, ' ')); !errors.Is(err, ErrInvalid) {
		t.Fatal("raw overflow accepted")
	}
}

func TestWorstCaseEscaping(t *testing.T) {
	raw := []byte(`{"schemaVersion":1,"answer":"` + strings.Repeat(`\u0061`, MaxAnswerBytes) + `"}`)
	got, err := decode(raw)
	if err != nil || len(got.Answer) != MaxAnswerBytes {
		t.Fatal("escaped boundary failed")
	}
}

func TestRequestInvalidSchema(t *testing.T) {
	cases := []string{`{}`, `[]`, `null`, `{"schemaVersion":true,"answer":"a"}`, `{"schemaVersion":1.0,"answer":"a"}`, `{"schemaVersion":1e0,"answer":"a"}`, `{"schemaVersion":null,"answer":"a"}`, `{"schemaVersion":2,"answer":"a"}`, `{"schemaVersion":1,"answer":null}`, `{"schemaVersion":1,"answer":3}`, `{"schemaVersion":1,"answer":[]}`, `{"schemaVersion":1,"answer":""}`, `{"schemaVersion":1,"answer":"a","answer":"b"}`, `{"schemaVersion":1,"answer":"a","\u0061nswer":"b"}`, `{"schemaVersion":1,"schemaVersion":1,"answer":"a"}`, `{"schemaVersion":1,"answer":"a","servedModel":"grok"}`, `{"schemaVersion":1,"answer":"a","provider":"cursor"}`, `{"schemaVersion":1,"answer":"a"} {}`, `{"SchemaVersion":1,"answer":"a"}`}
	for i, raw := range cases {
		if got, err := decode([]byte(raw)); !errors.Is(err, ErrInvalid) || got != (Request{}) {
			t.Errorf("fixture %d accepted or leaked partial data", i)
		}
	}
}

func TestRequestMalformedUnicode(t *testing.T) {
	cases := [][]byte{[]byte(`{"schemaVersion":1,"answer":"\ud800"}`), []byte(`{"schemaVersion":1,"answer":"\udfff"}`), []byte(`{"schemaVersion":1,"answer":"\ud800\u0041"}`), []byte(`{"schemaVersion":1,"answer":"\ud800\ud800"}`), []byte(`{"schemaVersion":1,"answer":"\ud800x"}`), []byte(`{"schemaVersion":1,"answer":"\uZZZZ"}`), append([]byte(`{"schemaVersion":1,"answer":"`), 0xff, '"', '}'), append([]byte(`{"schemaVersion":1,"answer":"`), 0xed, 0xa0, 0x80, '"', '}')}
	for i, b := range cases {
		if _, err := decode(b); !errors.Is(err, ErrInvalid) {
			t.Errorf("unicode fixture %d accepted", i)
		}
	}
}

func TestRequestControls(t *testing.T) {
	for r := rune(0); r <= 127; r++ {
		if r >= 32 && r != 127 {
			continue
		}
		_, err := decode(wire("a" + string(r)))
		allowed := r == '\t' || r == '\r' || r == '\n'
		if (err == nil) != allowed {
			t.Errorf("control U+%04X wrong outcome", r)
		}
	}
}

func TestTransportMetadata(t *testing.T) {
	for _, pair := range [][2]string{{"text/plain", ""}, {"application/json", "gzip"}, {"application/json", "identity"}, {"application/json; charset=utf-8", ""}, {"", ""}} {
		if _, err := DecodeRequest(bytes.NewReader(wire("a")), pair[0], pair[1]); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid transport accepted")
		}
	}
	if _, err := DecodeRequest(nil, "application/json", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil reader accepted")
	}
}

type countedReader struct{ count int }

func (r *countedReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	r.count += len(p)
	return len(p), nil
}
func TestBoundedRead(t *testing.T) {
	r := &countedReader{}
	if _, err := DecodeRequest(r, "application/json", ""); !errors.Is(err, ErrInvalid) || r.count != MaxRawBytes+1 {
		t.Fatal("reader bound violated")
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("sensitive transport diagnostic") }
func TestSafeErrors(t *testing.T) {
	if _, err := DecodeRequest(failedReader{}, "application/json", ""); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("transport error leaked")
	}
}

func fixture(t *testing.T) (Attempt, Receipt, time.Time) {
	t.Helper()
	k := Key{"scope", "host-admission-1", "session-1", "pod-1", "task-1", "invocation-1", "final", 1}
	p := Provenance{Tenant: "tenant", Project: "project", Repository: "repo", CatalogIdentity: "catalog", ReaderPolicyVersion: "reader-v1", RetentionPolicyVersion: "retention-v1", AdmissionEpoch: 1, Host: Attribution{Provider: "codex", RequestedModel: "host-requested", ServedModelStatus: "Unknown"}, Delegate: Attribution{Provider: "cursor", RequestedModel: "grok-requested", ServedModelStatus: "Unknown"}}
	a, err := NewAttempt(k, p, Request{1, "done"})
	if err != nil {
		t.Fatal("fixture construction failed")
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	r := Receipt{1, k, p, "record-1", "commit-1", a.digest, 4, now.Add(-time.Minute), now.Add(time.Hour), "Created"}
	return a, r, now
}

func receiptWire(r Receipt) []byte { b, _ := json.Marshal(r); return b }
func TestReceiptHappyUnknownAndReplay(t *testing.T) {
	a, r, now := fixture(t)
	got, err := DecodeReceipt(bytes.NewReader(receiptWire(r)), a, nil, now)
	if err != nil || got.Provenance.Delegate.ServedModel != nil || got.Provenance.Delegate.ServedModelStatus != "Unknown" {
		t.Fatal("unknown provenance lost")
	}
	r.Outcome = "Confirmed"
	if _, err := DecodeReceipt(bytes.NewReader(receiptWire(r)), a, &got, now); err != nil {
		t.Fatal("replay receipt rejected")
	}
}

func TestReceiptTampering(t *testing.T) {
	a, r, now := fixture(t)
	mutations := []func(*Receipt){func(r *Receipt) { r.Key.PodUID = "replacement" }, func(r *Receipt) { r.Key.SessionUID = "replacement" }, func(r *Receipt) { r.Key.TaskUID = "other" }, func(r *Receipt) { r.Key.InvocationUID = "other" }, func(r *Receipt) { r.Key.HostAdmissionUID = "other" }, func(r *Receipt) { r.Key.Scope = "other" }, func(r *Receipt) { r.Digest = strings.Repeat("0", 64) }, func(r *Receipt) { r.ByteCount++ }, func(r *Receipt) { r.Provenance.Delegate.Provider = "codex" }, func(r *Receipt) { r.Provenance.Host.Provider = "cursor" }, func(r *Receipt) { r.Provenance.AdmissionEpoch++ }, func(r *Receipt) { r.Provenance.CancellationEpoch++ }, func(r *Receipt) { r.Provenance.RetentionPolicyVersion = "v2" }, func(r *Receipt) { r.Provenance.ReaderPolicyVersion = "v2" }, func(r *Receipt) { r.Provenance.CatalogIdentity = "other" }, func(r *Receipt) { r.RecordUID = "" }, func(r *Receipt) { r.CommitID = "" }, func(r *Receipt) { r.Outcome = "Executed" }, func(r *Receipt) { r.ExpiresAt = now }, func(r *Receipt) { r.CommittedAt = now.Add(time.Minute) }, func(r *Receipt) { s := "grok"; r.Provenance.Delegate.ServedModel = &s }, func(r *Receipt) { r.Provenance.Delegate.ServedModelStatus = "Observed" }}
	for i, mutate := range mutations {
		changed := r
		mutate(&changed)
		if _, err := DecodeReceipt(bytes.NewReader(receiptWire(changed)), a, nil, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("tampering fixture %d accepted", i)
		}
	}
}

func TestReceiptStrictSchema(t *testing.T) {
	a, r, now := fixture(t)
	raw := string(receiptWire(r))
	cases := []string{raw + " {}", strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":true`, 1), strings.Replace(raw, `"cancellationEpoch":0`, `"cancellationEpoch":null`, 1), strings.Replace(raw, `"cancellationEpoch":0`, `"cancellationEpoch":false`, 1), strings.Replace(raw, `"cancellationEpoch":0,`, ``, 1), strings.Replace(raw, `"servedModel":null,`, ``, 1), strings.Replace(raw, `"provider":"cursor"`, `"provider":"cursor","provider":"cursor"`, 1), strings.Replace(raw, `"provider":"cursor"`, `"provider":"cursor","extra":1`, 1), strings.Replace(raw, `"recordUID":"record-1"`, `"recordUID":"\ud800"`, 1)}
	for i, b := range cases {
		if got, err := DecodeReceipt(strings.NewReader(b), a, nil, now); !errors.Is(err, ErrInvalid) || got.RecordUID != "" {
			t.Errorf("receipt schema fixture %d accepted or leaked", i)
		}
	}
}

func TestReceiptCommitMetadataImmutable(t *testing.T) {
	a, r, now := fixture(t)
	for _, mutate := range []func(*Receipt){func(r *Receipt) { r.RecordUID = "other" }, func(r *Receipt) { r.CommitID = "other" }, func(r *Receipt) { r.CommittedAt = r.CommittedAt.Add(time.Second) }, func(r *Receipt) { r.ExpiresAt = r.ExpiresAt.Add(time.Second) }, func(r *Receipt) { r.Outcome = "Created" }} {
		next := r
		next.Outcome = "Confirmed"
		mutate(&next)
		if !errors.Is(next.Validate(a, &r, now), ErrInvalid) {
			t.Fatal("commit metadata change accepted")
		}
	}
}

func TestImmutableRetryAndCopy(t *testing.T) {
	a, r, _ := fixture(t)
	req, err := decode([]byte(`{"answer":"\u0064one","schemaVersion":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewAttempt(r.Key, r.Provenance, req)
	if err != nil || !a.SameImmutable(b) {
		t.Fatal("semantic retry rejected")
	}
	changed, _ := NewAttempt(r.Key, r.Provenance, Request{1, "other"})
	if a.SameImmutable(changed) {
		t.Fatal("changed answer accepted")
	}
	r.Provenance.CatalogIdentity = "other"
	changed, _ = NewAttempt(r.Key, r.Provenance, req)
	if a.SameImmutable(changed) {
		t.Fatal("changed provenance accepted")
	}
	served := "observed-model"
	r.Provenance.Delegate.ServedModel = &served
	r.Provenance.Delegate.ServedModelStatus = "Observed"
	hostServed := "observed-host-model"
	r.Provenance.Host.ServedModel = &hostServed
	r.Provenance.Host.ServedModelStatus = "Observed"
	frozen, err := NewAttempt(r.Key, r.Provenance, req)
	if err != nil {
		t.Fatal(err)
	}
	copied, _ := NewAttempt(r.Key, r.Provenance, req)
	served = "mutated"
	hostServed = "mutated host"
	assertOriginal := func(p Provenance) {
		t.Helper()
		if p.Delegate.ServedModel == nil || *p.Delegate.ServedModel != "observed-model" {
			t.Fatal("frozen delegate model changed")
		}
		if p.Host.ServedModel == nil || *p.Host.ServedModel != "observed-host-model" {
			t.Fatal("frozen host model changed")
		}
	}
	_, p, _, _ := frozen.Values()
	assertOriginal(p)
	*p.Delegate.ServedModel = "mutated getter"
	*p.Host.ServedModel = "mutated host getter"
	_, after, _, _ := frozen.Values()
	assertOriginal(after)
	if !frozen.SameImmutable(copied) {
		t.Fatal("input or getter pointer mutated frozen attempt")
	}
	if (Attempt{}).SameImmutable(Attempt{}) {
		t.Fatal("empty attempt accepted")
	}
}

func TestDefaultsClosed(t *testing.T) {
	var admission Admission = Disabled{}
	var store Store = Disabled{}
	if _, _, err := admission.Resolve(context.Background(), Key{}); !errors.Is(err, ErrDisabled) {
		t.Fatal("admission default open")
	}
	if _, err := store.Commit(context.Background(), Attempt{}); !errors.Is(err, ErrDisabled) {
		t.Fatal("store default open")
	}
}

func TestInvalidAttemptAndReceiptExpectations(t *testing.T) {
	a, r, now := fixture(t)
	if _, err := NewAttempt(Key{}, r.Provenance, Request{1, "done"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero key accepted")
	}
	if _, err := NewAttempt(r.Key, Provenance{}, Request{1, "done"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero provenance accepted")
	}
	if _, err := DecodeReceipt(bytes.NewReader(receiptWire(r)), Attempt{}, nil, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero expectation accepted")
	}
	if _, err := DecodeReceipt(bytes.NewReader(receiptWire(r)), a, nil, time.Time{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero clock accepted")
	}
	if _, err := DecodeReceipt(io.LimitReader(failedReader{}, 1), a, nil, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("reader error accepted")
	}
}

func TestDomainSeparatedDigest(t *testing.T) {
	a, _, _ := fixture(t)
	if a.digest != "255d63efb0bdd457d758fc037be72bf83f334a4fc22fda35691e75ca50e0b9d1" {
		t.Fatal("digest does not match independently computed SHA-256 fixture")
	}
}

func TestExistingUIDShape(t *testing.T) {
	_, r, _ := fixture(t)
	for _, s := range []string{"", "a b", "a:b", "../other", strings.Repeat("a", 129)} {
		key := r.Key
		key.SessionUID = s
		if _, err := NewAttempt(key, r.Provenance, Request{1, "done"}); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid UID shape accepted")
		}
	}
}

func TestReceiptBoundedRead(t *testing.T) {
	a, _, now := fixture(t)
	r := &countedReader{}
	if _, err := DecodeReceipt(r, a, nil, now); !errors.Is(err, ErrInvalid) || r.count != MaxRawBytes+1 {
		t.Fatal("receipt reader bound violated")
	}
}

func TestReceiptObservedModelRequiresExactExpectation(t *testing.T) {
	a, r, now := fixture(t)
	observed := "authenticated-observation-fixture"
	r.Provenance.Delegate.ServedModel = &observed
	r.Provenance.Delegate.ServedModelStatus = "Observed"
	if !errors.Is(r.Validate(a, nil, now), ErrInvalid) {
		t.Fatal("receipt overrode unknown admitted provenance")
	}
	expected, err := NewAttempt(r.Key, r.Provenance, Request{1, "done"})
	if err != nil || r.Validate(expected, nil, now) != nil {
		t.Fatal("exact observed fixture rejected")
	}
	observed = "conflicting-observation"
	if !errors.Is(r.Validate(expected, nil, now), ErrInvalid) {
		t.Fatal("changed observation accepted")
	}
}

func TestReceiptExactKeysAndEscapedDuplicates(t *testing.T) {
	a, r, now := fixture(t)
	raw := string(receiptWire(r))
	cases := []string{
		strings.Replace(raw, `"schemaVersion":1`, `"SchemaVersion":1`, 1),
		strings.Replace(raw, `"sessionUID":"session-1"`, `"SessionUID":"session-1"`, 1),
		strings.Replace(raw, `"catalogIdentity":"catalog"`, `"CatalogIdentity":"catalog"`, 1),
		strings.Replace(raw, `"provider":"cursor"`, `"Provider":"cursor"`, 1),
		strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":1,"\u0073chemaVersion":1`, 1),
		strings.Replace(raw, `"sessionUID":"session-1"`, `"sessionUID":"session-1","\u0073essionUID":"session-1"`, 1),
		strings.Replace(raw, `"catalogIdentity":"catalog"`, `"catalogIdentity":"catalog","\u0063atalogIdentity":"catalog"`, 1),
		strings.Replace(raw, `"provider":"cursor"`, `"provider":"cursor","\u0070rovider":"cursor"`, 1),
	}
	for i, b := range cases {
		if got, err := DecodeReceipt(strings.NewReader(b), a, nil, now); !errors.Is(err, ErrInvalid) || got != (Receipt{}) {
			t.Errorf("exact-key fixture %d accepted or leaked", i)
		}
	}
}

// A valid document returned alongside a transport failure is not success.
// Return bytes progressively so the fixture obeys io.Reader for any buffer size.
type dataErrorReader struct{ data []byte }

func (r *dataErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, errors.New("sensitive partial transport diagnostic")
	}
	return n, nil
}

func TestDataWithNonEOFReadError(t *testing.T) {
	if got, err := DecodeRequest(&dataErrorReader{data: wire("done")}, "application/json", ""); !errors.Is(err, ErrInvalid) || got != (Request{}) {
		t.Fatal("request bytes plus error accepted or leaked")
	}
	a, r, now := fixture(t)
	if got, err := DecodeReceipt(&dataErrorReader{data: receiptWire(r)}, a, nil, now); !errors.Is(err, ErrInvalid) || got != (Receipt{}) {
		t.Fatal("receipt bytes plus error accepted or leaked")
	}
}
