package protocol

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func cursorFixtureTime() time.Time { return time.Date(2026, 10, 15, 10, 1, 0, 0, time.UTC) }

func cursorAPIFixture() (CursorBinding, []byte) {
	b := CursorBinding{Surface: "api", AgentID: "bc-synthetic-agent", RunID: "run-synthetic-run", CWD: "/workspace/project", WorkerName: "synthetic-worker", Repository: "example/project", ToolManifestSHA256: strings.Repeat("a", 64)}
	raw := []byte(`{"version":1,"provenance":{"source":"api-agent-run-projection","receiptID":"synthetic-api","observedAt":"2026-10-15T10:00:00Z"},"agentPath":"/v1/agents/bc-synthetic-agent","runPath":"/v1/agents/bc-synthetic-agent/runs/run-synthetic-run","agentHTTPStatus":200,"runHTTPStatus":200,"agent":{"id":"bc-synthetic-agent","status":"IDLE","latestRunId":"run-synthetic-run","env":{"type":"machine","name":"synthetic-worker"}},"run":{"id":"run-synthetic-run","agentId":"bc-synthetic-agent","status":"FINISHED"}}`)
	return b, raw
}

func cursorACPFixture() (CursorBinding, []byte) {
	b := CursorBinding{Surface: "acp", CWD: "/workspace/project", Repository: "example/project", ToolManifestSHA256: strings.Repeat("b", 64)}
	raw := []byte(`{"version":1,"provenance":{"source":"acp-initialize-list-projection","receiptID":"synthetic-acp","observedAt":"2026-10-15T10:00:00Z"},"initializeRequest":{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}},"initializeResponse":{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"sessionCapabilities":{"list":{}},"mcpCapabilities":{"http":true,"sse":true}}}},"listRequest":{"jsonrpc":"2.0","id":2,"method":"session/list","params":{"cwd":"/workspace/project"}},"listResponse":{"jsonrpc":"2.0","id":2,"result":{"sessions":[]}}}`)
	return b, raw
}

func TestCursorAPIReadReceipt(t *testing.T) {
	b, raw := cursorAPIFixture()
	got, err := NormalizeCursorAPIRead(raw, b, cursorFixtureTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.Binding != b || got.AgentState != "IDLE" || got.RunState != "FINISHED" || got.ModelState != UsageUnknown || got.WorkerIDState != UsageUnknown || got.Cost.State != UsageUnknown || !got.Cost.Valid() || got.ToolAccess != UsageUnknown || got.PhoneMirroring != UsageUnknown || got.ActualModelState != UsageUnknown {
		t.Fatalf("false evidence: %+v", got)
	}
	if got.BindingSource != "CallerPinned" || got.Provenance.ReceiptID != "synthetic-api" || len(got.ReceiptSHA256) != 64 {
		t.Fatal("lost provenance")
	}
	for _, change := range []struct{ old, new string }{
		{`"id":"bc-synthetic-agent"`, `"id":"bc-other"`},
		{`"agentId":"bc-synthetic-agent"`, `"agentId":"bc-other"`},
		{`"latestRunId":"run-synthetic-run"`, `"latestRunId":"run-other"`},
		{`"name":"synthetic-worker"`, `"name":"wrong-worker"`},
		{`"runHTTPStatus":200`, `"runHTTPStatus":403`},
		{`"runPath":"/v1/agents/bc-synthetic-agent/runs/run-synthetic-run"`, `"runPath":"/v1/agents/bc-other/runs/run-synthetic-run"`},
	} {
		bad := bytes.Replace(raw, []byte(change.old), []byte(change.new), 1)
		if v, e := NormalizeCursorAPIRead(bad, b, cursorFixtureTime(), time.Hour); e == nil || !reflect.DeepEqual(v, CursorControlObservation{}) {
			t.Fatalf("accepted mismatch %s", change.old)
		}
	}
	b.WorkerID = "unproved-worker-id"
	if _, err = NormalizeCursorAPIRead(raw, b, cursorFixtureTime(), time.Hour); err == nil {
		t.Fatal("missing worker ID fabricated")
	}
}

func TestCursorRequestedModelIsNotServedModelEvidence(t *testing.T) {
	b, raw := cursorAPIFixture()
	raw = bytes.Replace(raw, []byte(`"agent":{"id"`), []byte(`"agent":{"model":{"id":"synthetic-requested-model"},"id"`), 1)
	got, err := NormalizeCursorAPIRead(raw, b, cursorFixtureTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "synthetic-requested-model" || got.ModelState != CursorModelProviderRequested || got.ActualModelState != UsageUnknown {
		t.Fatalf("requested metadata overclaims served model: %+v", got)
	}
	if _, err := PlanCursorControl("observe", got, 1, cursorFixtureTime(), time.Hour); err != nil {
		t.Fatalf("requested metadata rejected by planning: %v", err)
	}
	got.ModelState = UsageProviderReported
	// Reseal to test the semantic validator independently of digest integrity.
	got.ObservationSHA256, err = cursorControlObservationDigest(got)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PlanCursorControl("observe", got, 1, cursorFixtureTime(), time.Hour); err == nil {
		t.Fatal("accepted serving-model provenance for requested metadata")
	}
}

func TestCursorSurfaceFieldsCannotBeResealedAcrossSurfaces(t *testing.T) {
	b, raw := cursorAPIFixture()
	api, err := NormalizeCursorAPIRead(raw, b, cursorFixtureTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"load", "list", "http", "sse", "listComplete"} {
		t.Run(field, func(t *testing.T) {
			changed := api
			switch field {
			case "load":
				changed.Advertisements.Load = "Advertised"
			case "list":
				changed.Advertisements.List = "Advertised"
			case "http":
				changed.Advertisements.MCPHTTP = "Advertised"
			case "sse":
				changed.Advertisements.MCPSSE = "Advertised"
			case "listComplete":
				changed.ListComplete = true
			}
			changed.ObservationSHA256, err = cursorControlObservationDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := PlanCursorControl("observe", changed, 1, cursorFixtureTime(), time.Hour); err == nil {
				t.Fatal("accepted ACP-only observation field on API surface")
			}
		})
	}
	b, raw = cursorACPFixture()
	acp, err := NormalizeCursorACPRead(raw, b, cursorFixtureTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	acp.ModelState, acp.Model = CursorModelProviderRequested, "synthetic-requested-model"
	acp.ObservationSHA256, err = cursorControlObservationDigest(acp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PlanCursorControl("observe", acp, 1, cursorFixtureTime(), time.Hour); err == nil {
		t.Fatal("accepted API requested-model field on ACP surface")
	}
}

func TestCursorACPReadReceipt(t *testing.T) {
	b, raw := cursorACPFixture()
	got, err := NormalizeCursorACPRead(raw, b, cursorFixtureTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.Binding != b || len(got.LocalSessions) != 0 || !got.ListComplete || got.AgentState != UsageUnknown || got.RunState != UsageUnknown || got.ToolAccess != UsageUnknown || got.PhoneMirroring != UsageUnknown || got.RuntimeAuthority != UsageUnknown || got.ModelState != UsageUnknown || got.Cost.State != UsageUnknown {
		t.Fatalf("empty list overclaim %+v", got)
	}
	if got.Advertisements != (CursorCapabilityAdvertisements{"Advertised", "Advertised", "Advertised", "Advertised"}) {
		t.Fatal("advertisement lost")
	}
	for _, change := range []struct{ old, new string }{
		{`"initializeResponse":{"jsonrpc":"2.0","id":1`, `"initializeResponse":{"jsonrpc":"2.0","id":2`},
		{`"listResponse":{"jsonrpc":"2.0","id":2`, `"listResponse":{"jsonrpc":"2.0","id":1`},
		{`"protocolVersion":1`, `"protocolVersion":2`},
		{`"method":"session/list"`, `"method":"session/load"`},
		{`"params":{"cwd":"/workspace/project"}`, `"params":{"cwd":"/workspace/other"}`},
		{`"sessions":[]`, `"sessions":null`},
		{`"sessions":[]`, `"sessions":[{"sessionId":"local-fixture","cwd":"/workspace/project"},{"sessionId":"local-fixture","cwd":"/workspace/project"}]`},
		{`"sessions":[]`, `"sessions":[{"sessionId":"local-fixture","cwd":"/workspace/other"}]`},
	} {
		bad := bytes.Replace(raw, []byte(change.old), []byte(change.new), 1)
		if v, e := NormalizeCursorACPRead(bad, b, cursorFixtureTime(), time.Hour); e == nil || !reflect.DeepEqual(v, CursorControlObservation{}) {
			t.Fatalf("accepted bad exchange %s", change.old)
		}
	}
	b.AgentID = "bc-synthetic-agent"
	if _, err = NormalizeCursorACPRead(raw, b, cursorFixtureTime(), time.Hour); err == nil {
		t.Fatal("cross-surface ID merge")
	}
	b, _ = cursorACPFixture()
	b.LocalSessionID = "bc-synthetic-agent"
	if _, err = NormalizeCursorACPRead(raw, b, cursorFixtureTime(), time.Hour); err == nil {
		t.Fatal("empty ACP list mirrored cloud identity")
	}
}

func TestCursorControlPlansRefuseBeforeSend(t *testing.T) {
	for _, surface := range []string{"api", "acp"} {
		var observation CursorControlObservation
		var err error
		if surface == "api" {
			b, raw := cursorAPIFixture()
			observation, err = NormalizeCursorAPIRead(raw, b, cursorFixtureTime(), time.Hour)
		} else {
			b, raw := cursorACPFixture()
			observation, err = NormalizeCursorACPRead(raw, b, cursorFixtureTime(), time.Hour)
		}
		if err != nil {
			t.Fatal(err)
		}
		sends := 0
		for _, op := range []CursorControlOperation{CursorControlLaunch, CursorControlSteer, CursorControlCancel} {
			p, e := PlanCursorControl(op, observation, 3, cursorFixtureTime(), time.Hour)
			if e != nil || p.State != "NeedsDecision" || p.OfflineRequest != nil || !reflect.DeepEqual(p.Observation, observation) {
				t.Fatalf("held plan %+v %v", p, e)
			}
			if p.OfflineRequest != nil {
				sends++
			}
		}
		if sends != 0 {
			t.Fatal("denied plan sent data")
		}
		p, e := PlanCursorControl(CursorControlObserve, observation, 3, cursorFixtureTime(), time.Hour)
		if e != nil || p.State != "ReusedReceipt" || p.OfflineRequest != nil {
			t.Fatal("observe created request")
		}
		p, e = PlanCursorControl(CursorControlList, observation, 3, cursorFixtureTime(), time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		if surface == "api" {
			if p.State != "NeedsDecision" || p.OfflineRequest != nil {
				t.Fatal("unproved cloud list allowed")
			}
		} else {
			if p.State != "OfflineOnly" || p.OfflineRequest == nil || p.OfflineRequest.Method != "session/list" || p.OfflineRequest.Params.CWD != observation.Binding.CWD {
				t.Fatal("offline list binding lost")
			}
		}
	}
	b, _ := cursorACPFixture()
	for _, method := range []string{"authenticate", "session/new", "session/load", "session/prompt", "session/cancel", "tool", "send", "followup", "worker/start"} {
		if r, e := BuildCursorACPRequest(method, 4, b); e == nil || !reflect.DeepEqual(r, CursorACPRequest{}) {
			t.Fatalf("held method %s built request", method)
		}
	}
	for _, method := range []string{"initialize", "session/list"} {
		r, e := BuildCursorACPRequest(method, 5, b)
		if e != nil || r.ID != 5 || r.Method != method {
			t.Fatal("offline request refused")
		}
	}
}

func TestCursorControlBoundsAndFreshness(t *testing.T) {
	b, raw := cursorAPIFixture()
	bad := [][]byte{nil, []byte("{"), append(append([]byte{}, raw...), []byte("{}")...), []byte(strings.Repeat(" ", MaxCursorControlReceiptBytes+1)),
		bytes.Replace(raw, []byte(`"status":"IDLE"`), []byte(`"status":"IDLE","status":"RUNNING"`), 1),
		bytes.Replace(raw, []byte(`"status":"IDLE"`), []byte(`"status":"IDLE","STATUS":"RUNNING"`), 1),
		bytes.Replace(raw, []byte(`"status":"IDLE"`), []byte(`"status":"IDLE","ſtatus":"RUNNING"`), 1),
		bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"messages":["synthetic-prose"]`), 1)}
	for i, v := range bad {
		if _, e := NormalizeCursorAPIRead(v, b, cursorFixtureTime(), time.Hour); e == nil {
			t.Fatalf("accepted malformed %d", i)
		}
	}
	for _, v := range []struct {
		now time.Time
		age time.Duration
	}{{cursorFixtureTime().Add(2 * time.Hour), time.Hour}, {cursorFixtureTime().Add(-2 * time.Minute), time.Hour}, {cursorFixtureTime(), 0}, {cursorFixtureTime(), 25 * time.Hour}} {
		if _, e := NormalizeCursorAPIRead(raw, b, v.now, v.age); e == nil {
			t.Fatal("stale/future/unbounded receipt accepted")
		}
	}
	o, e := NormalizeCursorAPIRead(raw, b, cursorFixtureTime(), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = PlanCursorControl(CursorControlObserve, o, 6, cursorFixtureTime().Add(2*time.Hour), time.Hour); e == nil {
		t.Fatal("stale normalized observation planned")
	}
}

func TestCursorControlSerialization(t *testing.T) {
	b, raw := cursorACPFixture()
	o, e := NormalizeCursorACPRead(raw, b, cursorFixtureTime(), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	p, e := PlanCursorControl(CursorControlList, o, 7, cursorFixtureTime(), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	a, e := CanonicalJSON(p)
	if e != nil {
		t.Fatal(e)
	}
	var round CursorControlPlan
	if e = json.Unmarshal(a, &round); e != nil || !reflect.DeepEqual(p, round) {
		t.Fatalf("plan round trip %v", e)
	}
	again, e := PlanCursorControl(CursorControlList, round.Observation, 7, cursorFixtureTime(), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	c, _ := CanonicalJSON(again)
	if !bytes.Equal(a, c) || round.Observation.Binding.ToolManifestSHA256 != b.ToolManifestSHA256 || round.Observation.Provenance != o.Provenance || round.Observation.ReceiptSHA256 != o.ReceiptSHA256 {
		t.Fatal("identity/manifest/provenance nondeterministic")
	}
	if (Session{Agent: "cursor"}).Validate() == nil {
		t.Fatal("offline contract admitted production Cursor session")
	}
}

func TestCursorControlObservationIntegrity(t *testing.T) {
	b, raw := cursorAPIFixture()
	original, err := NormalizeCursorAPIRead(raw, b, cursorFixtureTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*CursorControlObservation){
		func(o *CursorControlObservation) {
			o.Binding.AgentID = "bc-other-agent"
			o.Binding.RunID = "run-other-run"
		},
		func(o *CursorControlObservation) { o.Binding.WorkerName = "other-worker" },
		func(o *CursorControlObservation) { o.Binding.CWD = "/workspace/other" },
		func(o *CursorControlObservation) { o.Binding.Repository = "other/project" },
		func(o *CursorControlObservation) { o.Binding.ToolManifestSHA256 = strings.Repeat("c", 64) },
		func(o *CursorControlObservation) { o.Provenance.ReceiptID = "other-receipt" },
		func(o *CursorControlObservation) { o.AgentState = "RUNNING" },
		func(o *CursorControlObservation) { o.ReceiptSHA256 = strings.Repeat("d", 64) },
	} {
		o := original
		mutate(&o)
		if p, e := PlanCursorControl(CursorControlObserve, o, 8, cursorFixtureTime(), time.Hour); e == nil || p.OfflineRequest != nil {
			t.Fatal("rebound observation reused")
		}
	}
	b, raw = cursorACPFixture()
	o, err := NormalizeCursorACPRead(raw, b, cursorFixtureTime(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	o.Binding.LocalSessionID = "absent-local-session"
	o, err = sealCursorControlObservation(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PlanCursorControl(CursorControlObserve, o, 9, cursorFixtureTime(), time.Hour); err == nil {
		t.Fatal("absent local session reused")
	}
}
