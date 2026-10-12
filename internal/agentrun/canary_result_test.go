package agentrun

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func originalCanaryResult(id string) apiv1.CanaryResult {
	s := originalCanary(id)
	invocation := apiv1.ManagedInvocation(s.UID, "original-pod-uid")
	key := apiv1.ManagedOutputKey(invocation, 0)
	text := "SYNTHETIC_ASSISTANT_OUTPUT\n"
	return apiv1.CanaryResult{CanaryID: id, SessionUID: s.UID, Context: apiv1.PublishedContext{
		Metadata: apiv1.ContextMetadata{
			ManagedOutput: &apiv1.ManagedOutputAssertion{Invocation: invocation, Ordinal: 0, ConversationID: "11111111-1111-4111-8111-111111111111"},
			Name:          apiv1.ContextName(s.UID, "transcript-excerpt", key), UID: "publication-original", Kind: "transcript-excerpt", Key: key,
			Source: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: s.Name, UID: s.UID}, SourceGeneration: 1, SourceParent: s.Parent,
			Project: "dev-env", Repository: "dev-env", Actor: apiv1.TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: s.Name, PodUID: "original-pod-uid"},
			Provenance:    apiv1.TaskClaimProvenance{CatalogRevision: strings.Repeat("a", 64), RulesRevision: strings.Repeat("b", 64), GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"},
			Attribution:   apiv1.ContextAttribution{Provider: s.Agent, RequestedModel: s.Model, ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"},
			ContentSHA256: apiv1.ContextHash(text), ByteCount: int32(len(text)), PublishedAt: testNow, Origin: "ManagedOutputAssertion", Completeness: "PartialManagedOutput", StopState: "Unknown",
		}, Text: text,
	}}
}

func canaryResultArgs(p apiv1.CanaryResult) []string {
	return []string{"canary", "result", "--id", p.CanaryID, "--session-uid", p.SessionUID, "--context", p.Context.Metadata.Name, "--json", "--once"}
}

func TestCanaryResultOneOriginalGETAndRawNull(t *testing.T) {
	for _, id := range []string{claudeCanaryID, codexCanaryID} {
		t.Run(id, func(t *testing.T) {
			h := newHarness(t)
			p := originalCanaryResult(id)
			raw, _ := json.Marshal(p)
			raw = bytes.Replace(raw, []byte(`"initialPinState":"Unknown"`), []byte(`"initialPinState":"Unknown","reportedAt":null`), 1)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(raw) }
			h.mustRun(ExitOK, canaryResultArgs(p)...)
			reqs := h.api.requests()
			if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != apiv1.CanaryResultPath(id) || reqs[0].auth != "Bearer "+testToken || len(reqs[0].body) != 0 || len(reqs[0].query) != 2 || len(reqs[0].query["sessionUID"]) != 1 || reqs[0].query["sessionUID"][0] != p.SessionUID || len(reqs[0].query["context"]) != 1 || reqs[0].query["context"][0] != p.Context.Metadata.Name || len(h.ran) != 0 || len(h.sleeps) != 0 {
				t.Fatal("result crossed its single named read boundary", reqs, h.ran, h.sleeps)
			}
			if !bytes.Contains(h.stdout.Bytes(), []byte(`"reportedAt": null`)) && !bytes.Contains(h.stdout.Bytes(), []byte(`"reportedAt":null`)) {
				t.Fatal("optional explicit null was lost", h.stdout.String())
			}
			var got apiv1.CanaryResult
			if json.Unmarshal(h.stdout.Bytes(), &got) != nil || got.Context.Text != p.Context.Text || got.SessionUID != p.SessionUID {
				t.Fatal("raw stored publication was not preserved")
			}
		})
	}
}

func TestCanaryResultExplicitSelectionBeforeAuthentication(t *testing.T) {
	p := originalCanaryResult(claudeCanaryID)
	valid := canaryResultArgs(p)
	cases := [][]string{{"canary", "result"}, {"canary", "result", "--id", p.CanaryID, "--session-uid", p.SessionUID, "--json", "--once", "--kubeconfig", "/synthetic/common-kubeconfig"}}
	for _, flag := range []string{"--id", "--session-uid", "--context"} {
		for i, arg := range valid {
			if arg == flag {
				cases = append(cases, append(append([]string{}, valid[:i]...), valid[i+2:]...))
			}
		}
		cases = append(cases, append(append([]string{}, valid...), flag, "unexpected"))
	}
	for _, extra := range [][]string{{"--once=false"}, {"--json=false"}, {"--project", "dev-env"}, {"--prompt", "task"}, {"--poll"}, {"unexpected"}} {
		cases = append(cases, append(append([]string{}, valid...), extra...))
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			envReads := 0
			h.vars["KUBECONFIG"] = "/synthetic/common-kubeconfig"
			h.vars["DEV_ENV_CONTEXT"] = p.Context.Metadata.Name
			h.env.Getenv = func(k string) string { envReads++; return h.vars[k] }
			h.mustRun(ExitUsage, args...)
			if envReads != 0 || len(h.api.requests()) != 0 || len(h.ran) != 0 || h.stdout.Len() != 0 {
				t.Fatal("implicit selection reached connection/auth", envReads, h.api.requests(), h.ran)
			}
		})
	}
}

func TestCanaryResultPublicationNameIsNotKubeconfigContext(t *testing.T) {
	h, l := laptopHarness(t, operatorDNS)
	p := originalCanaryResult(claudeCanaryID)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, p) }
	h.mustRun(ExitOK, append(canaryResultArgs(p), "--kubeconfig", "/synthetic/config")...)
	if l.contexts != 1 || len(h.api.requests()) != 1 {
		t.Fatal("default kubeconfig context was not resolved separately", l.contexts, h.api.requests())
	}
	for _, argv := range append(l.calls, l.starts...) {
		for _, arg := range argv {
			if arg == p.Context.Metadata.Name {
				t.Fatal("publication name used as kubeconfig context", argv)
			}
		}
	}
}

func TestCanaryResultRejectsAmbiguousRawJSON(t *testing.T) {
	p := originalCanaryResult(claudeCanaryID)
	raw, _ := json.Marshal(p)
	cases := map[string][]byte{
		"duplicate-id":      bytes.Replace(raw, []byte(`"canaryID":`), []byte(`"canaryID":"foreign","canaryID":`), 1),
		"duplicate-session": bytes.Replace(raw, []byte(`"sessionUID":`), []byte(`"sessionUID":"foreign","sessionUID":`), 1),
		"duplicate-source":  bytes.Replace(raw, []byte(`"uid":"original-session-uid"`), []byte(`"uid":"foreign","uid":"original-session-uid"`), 1),
		"duplicate-hash":    bytes.Replace(raw, []byte(`"contentSHA256":`), []byte(`"contentSHA256":"foreign","contentSHA256":`), 1),
		"case-alias":        bytes.Replace(raw, []byte(`"canaryID":`), []byte(`"CANARYID":"foreign","canaryID":`), 1),
		"nested-case-alias": bytes.Replace(raw, []byte(`"uid":"original-session-uid"`), []byte(`"UID":"foreign","uid":"original-session-uid"`), 1),
		"unknown":           bytes.Replace(raw, []byte(`"metadata":{`), []byte(`"metadata":{"extra":true,`), 1),
		"null-session":      bytes.Replace(raw, []byte(`"sessionUID":"original-session-uid"`), []byte(`"sessionUID":null`), 1),
		"null-ordinal":      bytes.Replace(raw, []byte(`"ordinal":0`), []byte(`"ordinal":null`), 1),
		"missing-ordinal":   bytes.Replace(raw, []byte(`"ordinal":0,`), nil, 1),
		"null-context":      []byte(`{"canaryID":"` + p.CanaryID + `","sessionUID":"` + p.SessionUID + `","context":null}`),
		"second-envelope":   append(append([]byte{}, raw...), []byte(` {}`)...),
		"invalid-utf8":      bytes.Replace(raw, []byte("SYNTHETIC"), []byte{0xff}, 1),
		"wrong-hash":        bytes.Replace(raw, []byte(p.Context.Metadata.ContentSHA256), []byte(strings.Repeat("0", 64)), 1),
		"foreign-source":    bytes.Replace(raw, []byte(p.Context.Metadata.Source.Name), []byte("foreign-original"), 1),
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(reply) }
			h.mustRun(ExitFailed, canaryResultArgs(p)...)
			if len(h.api.requests()) != 1 || h.stdout.Len() != 0 || len(h.sleeps) != 0 {
				t.Fatal("ambiguous reply printed or retried", h.api.requests(), h.stdout.String(), h.sleeps)
			}
		})
	}
}

func TestCanaryResultRedirectAndErrorsNeverRetry(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			p := originalCanaryResult(claudeCanaryID)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.Header().Set("Location", h.api.srv.URL+"/redirected")
				apiError(w, status, apiv1.CodeUnavailable, "unconfirmed result")
			}
			if h.run(canaryResultArgs(p)...) == ExitOK || len(h.api.requests()) != 1 || h.stdout.Len() != 0 || len(h.sleeps) != 0 {
				t.Fatal("uncertain result retried or accepted", h.api.requests(), h.stdout.String(), h.sleeps)
			}
		})
	}
}
