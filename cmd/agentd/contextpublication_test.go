package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type contextNoExecution struct{}

func (contextNoExecution) Run(context.Context, agentd.Cmd) (agentd.Result, error) {
	panic("context command executed a process")
}
func (contextNoExecution) LookPath(string) (string, error) {
	panic("context command searched for a provider")
}

func TestExplicitContextCommandsUseOnlyStdinAndProjectedAPI(t *testing.T) {
	home := t.TempDir()
	if os.Mkdir(filepath.Join(home, ".agentd"), 0o700) != nil {
		t.Fatal("state fixture")
	}
	token := filepath.Join(t.TempDir(), "token")
	if os.WriteFile(token, []byte("PROJECTED_IDENTITY_CANARY"), 0o600) != nil {
		t.Fatal("token fixture")
	}
	private := filepath.Join(home, "auth-canary.json")
	if os.WriteFile(private, []byte("PRIVATE_AUTH_CANARY"), 0o600) != nil {
		t.Fatal("canary fixture")
	}
	req := apiv1.PublishContextRequest{Kind: "handoff", Key: "task-174", Text: "EXPLICIT_STDIN_TEXT"}
	m := apiv1.ContextMetadata{Name: apiv1.ContextName("session-uid", req.Kind, req.Key), UID: "publication-uid", Kind: req.Kind, Key: req.Key, Source: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "source", UID: "session-uid"}, SourceGeneration: 1, SourceParent: "human", Project: "sample", Repository: "repo", Actor: apiv1.TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "source", PodUID: "pod-uid"}, Provenance: apiv1.TaskClaimProvenance{CatalogRevision: strings.Repeat("a", 64), RulesRevision: strings.Repeat("b", 64), GitHub: "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown"}, Attribution: apiv1.ContextAttribution{Provider: "claude", RequestedModel: "claude-opus-5-5", ObservedModelState: "Unknown", UsageState: "Unknown", CostState: "Unknown"}, ContentSHA256: apiv1.ContextHash(req.Text), ByteCount: int32(len(req.Text)), PublishedAt: time.Now(), Origin: "ExplicitPublished", Completeness: "ExplicitExcerpt", StopState: "Unknown"}
	calls := 0
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer PROJECTED_IDENTITY_CANARY" {
			t.Error("missing projected identity")
		}
		if r.Method == http.MethodPost {
			var got apiv1.PublishContextRequest
			if json.NewDecoder(r.Body).Decode(&got) != nil || got != req {
				t.Error("request exported extra input")
			}
			_ = json.NewEncoder(w).Encode(apiv1.PublishContextResult{Outcome: "Created", Metadata: m})
		} else if r.URL.Path == apiv1.PublishedContextsPath {
			_ = json.NewEncoder(w).Encode(apiv1.PublishedContextList{Items: []apiv1.ContextMetadata{m}})
		} else {
			_ = json.NewEncoder(w).Encode(apiv1.PublishedContext{Metadata: m, Text: req.Text})
		}
	}))
	defer ts.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600) != nil {
		t.Fatal("CA fixture")
	}
	env := map[string]string{"HOME": home, "DEV_ENV_POD_UID": "pod-uid", "AGENTD_API_URL": ts.URL, "AGENTD_API_TOKEN_FILE": token, "AGENTD_API_CA_FILE": ca, "AGENTD_SESSION": `{"name":"source","sessionUID":"session-uid","repo":"repo","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"PRIVATE_PROMPT_CANARY"}`}
	invoke := func(args []string, text string) (int, string, string) {
		var out, errout bytes.Buffer
		code := run(context.Background(), args, strings.NewReader(text), &out, &errout, func(k string) string { return env[k] }, contextNoExecution{})
		return code, out.String(), errout.String()
	}
	code, out, errout := invoke([]string{"ctl", "context-publish", "--kind=handoff", "--key=task-174"}, req.Text)
	if code != exitOK || strings.Contains(out, req.Text) || strings.Contains(out+errout, "CANARY") {
		t.Fatal("publish command leaked or failed", code, out, errout)
	}
	code, out, errout = invoke([]string{"ctl", "context-list", "--project=sample"}, "")
	if code != exitOK || strings.Contains(out, req.Text) {
		t.Fatal("list leaked text", code, out, errout)
	}
	code, out, errout = invoke([]string{"ctl", "context-read", "--project=sample", "--name=" + m.Name}, "")
	if code != exitOK || !strings.Contains(out, req.Text) {
		t.Fatal("authorized read failed", code, out, errout)
	}
	for _, args := range [][]string{{"ctl", "context-publish", "--kind=handoff", "--key=task-174", "--file=" + private}, {"ctl", "context-publish", "--kind=handoff", "--key=oversized"}} {
		text := req.Text
		if len(args) == 4 {
			text = strings.Repeat("é", apiv1.MaxContextBytes/2+1)
		}
		code, _, errout = invoke(args, text)
		if code != exitUsage || strings.Contains(errout, "CANARY") {
			t.Fatal("path or oversized input accepted", code, errout)
		}
	}
	if calls != 3 {
		t.Fatal("refused command reached API", calls)
	}
	data, err := os.ReadFile(private)
	if err != nil || string(data) != "PRIVATE_AUTH_CANARY" {
		t.Fatal("private HOME modified")
	}
}
