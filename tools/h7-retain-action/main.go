// A separate, fixed, one-attempt action. Source preparation is not execution
// permission. The owner must approve the frozen/reviewed executable first.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	target         = "/api/v1/persistentvolumes/pvc-ec80a337-740f-49d3-8b27-171bb6eed5af"
	pvName         = "pvc-ec80a337-740f-49d3-8b27-171bb6eed5af"
	pvUID          = "92143b2b-e065-4511-9310-2a5d8c069192"
	expectedRV     = "828181553"
	claimNamespace = "dev-agents"
	claimName      = "dev-env-history-archive"
	claimUID       = "ec80a337-740f-49d3-8b27-171bb6eed5af"
	patchSHA       = "70d829eb6049d81a3c52be451c3b4b9502d8e34bbb9e43239f47772f19e9fd47"
	maxResponse    = 512 * 1024
	maxTotal       = 2 * 1024 * 1024
	maxHeader      = 32 * 1024
	maxBinary      = 32 * 1024 * 1024
	requestTimeout = 5 * time.Second
	runTimeout     = 30 * time.Second
)

// This exact canonical payload is immutable and has no user-controlled fields.
const patch = `[{"op":"test","path":"/metadata/uid","value":"92143b2b-e065-4511-9310-2a5d8c069192"},{"op":"test","path":"/metadata/resourceVersion","value":"828181553"},{"op":"test","path":"/status/phase","value":"Bound"},{"op":"test","path":"/spec/claimRef/namespace","value":"dev-agents"},{"op":"test","path":"/spec/claimRef/name","value":"dev-env-history-archive"},{"op":"test","path":"/spec/claimRef/uid","value":"ec80a337-740f-49d3-8b27-171bb6eed5af"},{"op":"test","path":"/spec/persistentVolumeReclaimPolicy","value":"Delete"},{"op":"replace","path":"/spec/persistentVolumeReclaimPolicy","value":"Retain"}]`

var sourceCommit string // Set only by the hosted frozen-source build.

type provenance struct {
	Source string `json:"sourceCommit,omitempty"`
	Binary string `json:"binarySha256,omitempty"`
}

type safePV struct {
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	Phase           string `json:"phase"`
	ClaimNamespace  string `json:"claimNamespace"`
	ClaimName       string `json:"claimName"`
	ClaimUID        string `json:"claimUID"`
	Policy          string `json:"reclaimPolicy"`
}

type requestReceipt struct {
	Method        string `json:"method"`
	Started       string `json:"startedAt"`
	Finished      string `json:"finishedAt"`
	Status        int    `json:"httpStatus,omitempty"`
	ResponseBytes int    `json:"responseBytes"`
	Attempt       int    `json:"attempt"`
	Error         string `json:"error,omitempty"`
}

type report struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Outcome         string `json:"outcome"`
	Code            string `json:"code,omitempty"`
	Target          string `json:"target"`
	PatchSHA        string `json:"patchSha256"`
	OwnerReceiptSHA string `json:"ownerReceiptSha256,omitempty"`
	Authorization   string `json:"authorization"`
	provenance
	Started            string           `json:"startedAt"`
	Finished           string           `json:"finishedAt"`
	Requests           []requestReceipt `json:"requests"`
	TotalResponseBytes int              `json:"totalResponseBytes"`
	PatchAttempts      int              `json:"patchAttempts"`
	Preflight          *safePV          `json:"preflight,omitempty"`
	Postflight         *safePV          `json:"postflight,omitempty"`
}

type action struct {
	ctx                      context.Context
	base                     *url.URL
	token                    string
	tlsConfig                *tls.Config // Only offline tests substitute their own fixture CA.
	attempts, total, patches int
	used                     bool
	receipts                 []requestReceipt
}

type response struct {
	status int
	code   string
	object map[string]any
}

func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func ownProvenance() (provenance, string) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return provenance{}, "PlatformRefused"
	}
	if !lowerHex(sourceCommit, 40) {
		return provenance{}, "BuildProvenanceRefused"
	}
	name, err := os.Executable()
	if err != nil {
		return provenance{}, "BuildProvenanceRefused"
	}
	f, err := os.Open(name)
	if err != nil {
		return provenance{}, "BuildProvenanceRefused"
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxBinary+1))
	if err != nil || n > maxBinary {
		return provenance{}, "BuildProvenanceRefused"
	}
	return provenance{Source: sourceCommit, Binary: hex.EncodeToString(h.Sum(nil))}, ""
}

// Flags carry procedural approval provenance, not cryptographic authorization.
// Root must observe the actual owner ruling after executable review and before
// this invocation. There is no stdin, configurable identity, target or payload.
func run(ctx context.Context, args []string, load func(context.Context) (*action, string), build func() (provenance, string)) report {
	r := report{SchemaVersion: 1, Outcome: "NotAuthorized", Code: "ExplicitOwnerReceiptRequired", Target: target, PatchSHA: patchSHA,
		Authorization: "ProceduralReceiptOnly", Started: time.Now().UTC().Format(time.RFC3339Nano), Requests: []requestReceipt{}}
	finish := func() report { r.Finished = time.Now().UTC().Format(time.RFC3339Nano); return r }
	if len(args) != 3 || args[0] != "--owner-approved" || args[1] != "--owner-receipt-sha256" || !lowerHex(args[2], 64) {
		return finish()
	}
	r.OwnerReceiptSHA = args[2]
	var code string
	r.provenance, code = build()
	if code != "" {
		r.Outcome, r.Code = "PreparationRefused", code
		return finish()
	}
	hash := sha256.Sum256([]byte(patch))
	if hex.EncodeToString(hash[:]) != patchSHA {
		r.Outcome, r.Code = "PreparationRefused", "PayloadDigestRefused"
		return finish()
	}
	a, code := load(ctx)
	if code != "" {
		r.Outcome, r.Code = "PreparationRefused", code
		return finish()
	}
	a.perform(&r)
	r.Requests, r.TotalResponseBytes, r.PatchAttempts = a.receipts, a.total, a.patches
	return finish()
}

// A process executes this state machine once. No result authorizes restarting it.
// A refused/ambiguous PATCH consumes the one write and is never refreshed/retried.
func (a *action) perform(r *report) {
	if a.used {
		r.Outcome, r.Code = "PreparationRefused", "AlreadyAttempted"
		return
	}
	a.used = true
	pre := a.exchange(http.MethodGet)
	if pre.code != "" {
		r.Outcome, r.Code = "PreflightRefused", pre.code
		return
	}
	pv, ok := validatedPV(pre.object)
	if !ok {
		r.Outcome, r.Code = "PreflightRefused", "TargetPremiseRefused"
		return
	}
	r.Preflight = pv
	if pv.ResourceVersion != expectedRV || pv.Policy != "Delete" {
		r.Outcome, r.Code = "PreflightRefused", "TargetPremiseChanged"
		return
	}
	written := a.exchange(http.MethodPatch)
	// Known explicit rejection statuses are definitive only with a bounded,
	// syntactically valid JSON error response. Never emit its contents.
	if written.code == "HTTPStatusRefused" && definitiveRejection(written.status) {
		r.Outcome, r.Code = "DefinitiveWriteError", "HTTPStatusRefused"
		return
	}
	patchPV, validPatch := validatedPV(written.object)
	acknowledged := written.code == "" && validPatch && patchPV.Policy == "Retain"
	post := a.exchange(http.MethodGet)
	if post.code != "" {
		r.Outcome, r.Code = "UncertainAfterWrite", post.code
		return
	}
	current, ok := validatedPV(post.object)
	if !ok {
		r.Outcome, r.Code = "UncertainAfterWrite", "PostconditionRefused"
		return
	}
	r.Postflight = current
	if current.Policy != "Retain" {
		// Even Delete does not prove that a dispatched ambiguous PATCH cannot
		// still commit later. No cancellation, rollback or retry is inferred.
		r.Outcome, r.Code = "UncertainAfterWrite", "RetainNotObserved"
		return
	}
	if !acknowledged {
		r.Outcome, r.Code = "RetainObservedAfterAmbiguousWrite", "WriteCausationUnproved"
		return
	}
	r.Outcome, r.Code = "AppliedVerified", ""
}

func definitiveRejection(status int) bool {
	switch status {
	case 400, 401, 403, 404, 405, 409, 410, 411, 413, 415, 422:
		return true
	}
	return false
}

func (a *action) exchange(method string) (out response) {
	rec := requestReceipt{Method: method, Started: time.Now().UTC().Format(time.RFC3339Nano), Attempt: a.attempts}
	defer func() {
		rec.Finished = time.Now().UTC().Format(time.RFC3339Nano)
		rec.Error = out.code
		a.receipts = append(a.receipts, rec)
	}()
	fail := func(code string) response { return response{status: rec.Status, code: code} }
	if a.ctx.Err() != nil || a.attempts >= 3 || a.total >= maxTotal {
		return fail("RequestBudgetExhausted")
	}
	want := [3]string{http.MethodGet, http.MethodPatch, http.MethodGet}
	if method != want[a.attempts] || method == http.MethodPatch && a.patches != 0 {
		return fail("RequestSequenceRefused")
	}
	ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
	defer cancel()
	u := *a.base
	u.Path, u.RawPath, u.RawQuery, u.Fragment = target, "", "", ""
	var body io.Reader
	if method == http.MethodPatch {
		body = io.LimitReader(strings.NewReader(patch), int64(len(patch)))
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return fail("RequestRefused")
	}
	req.GetBody = nil // No rewind/replay even for the immutable payload.
	req.Close = true
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json-patch+json")
		req.ContentLength = int64(len(patch))
	}
	// Same pinned-Go strategy as the reviewed GET transport: each RoundTrip
	// owns a fresh HTTP/1 connection; no proxy, reuse, redirect or replay path.
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: requestTimeout}).DialContext,
		TLSClientConfig: a.tlsConfig, TLSHandshakeTimeout: requestTimeout, ResponseHeaderTimeout: requestTimeout,
		DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false,
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}, MaxResponseHeaderBytes: maxHeader}
	defer tr.CloseIdleConnections()
	a.attempts++
	rec.Attempt = a.attempts
	if method == http.MethodPatch {
		a.patches++
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		if ctx.Err() != nil {
			return fail("RequestTimeout")
		}
		return fail("TransportRefused")
	}
	defer resp.Body.Close()
	rec.Status = resp.StatusCode
	if resp.Header.Get("Content-Encoding") != "" {
		return fail("EncodingRefused")
	}
	limit := maxResponse
	if maxTotal-a.total < limit {
		limit = maxTotal - a.total
	}
	if resp.ContentLength > int64(limit) {
		return fail("DeclaredResponseBudgetExhausted")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	rec.ResponseBytes = len(b)
	a.total += len(b)
	if len(b) > limit {
		return fail("ResponseBudgetExhausted")
	}
	if err != nil {
		if ctx.Err() != nil {
			return fail("RequestTimeout")
		}
		return fail("ResponseReadRefused")
	}
	object, ok := strictObject(b)
	if !ok {
		return fail("MalformedJSONResponse")
	}
	if resp.StatusCode != http.StatusOK {
		return fail("HTTPStatusRefused")
	}
	return response{status: rec.Status, object: object}
}

func validatedPV(obj map[string]any) (*safePV, bool) {
	meta, _ := obj["metadata"].(map[string]any)
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)
	claim, _ := spec["claimRef"].(map[string]any)
	if obj["apiVersion"] != "v1" || obj["kind"] != "PersistentVolume" || meta["name"] != pvName || meta["uid"] != pvUID ||
		status["phase"] != "Bound" || claim["namespace"] != claimNamespace || claim["name"] != claimName || claim["uid"] != claimUID {
		return nil, false
	}
	if value, found := meta["deletionTimestamp"]; found && value != nil {
		return nil, false
	}
	rv, ok := meta["resourceVersion"].(string)
	if !ok || len(rv) == 0 || len(rv) > 64 {
		return nil, false
	}
	for _, c := range rv {
		if c < '0' || c > '9' {
			return nil, false
		}
	}
	policy, ok := spec["persistentVolumeReclaimPolicy"].(string)
	if !ok || policy != "Delete" && policy != "Retain" {
		return nil, false
	}
	// Emit approved identifiers, never arbitrary live strings or storage data.
	return &safePV{Name: pvName, UID: pvUID, ResourceVersion: rv, Phase: "Bound", ClaimNamespace: claimNamespace, ClaimName: claimName, ClaimUID: claimUID, Policy: policy}, true
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	// Hard wall also bounds blocked config-file/output I/O. Termination cannot
	// establish cancellation of an already dispatched PATCH. Never restart it.
	wall := time.AfterFunc(runTimeout, func() { os.Exit(2) })
	defer wall.Stop()
	r := run(ctx, os.Args[1:], configured, ownProvenance)
	if json.NewEncoder(os.Stdout).Encode(r) != nil || r.Outcome != "AppliedVerified" {
		os.Exit(2)
	}
}
