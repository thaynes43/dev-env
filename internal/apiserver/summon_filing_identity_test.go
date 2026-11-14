package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// OP801 has no API implementation changes. Both create decoders already
// reject these operator-only fields; exercise each authenticated create
// class, including a nested spec/summon payload, through the HTTP handler.
func TestNoAPICreateCanSetFilingIdentity(t *testing.T) {
	id := "filing:00000000-0000-0000-0000-000000000801"
	for _, class := range []string{"human", "client", "session", "summoning-caller"} {
		for _, field := range []string{"filedFor", "dispatchID", "summon", "spec"} {
			t.Run(class+"/"+field, func(t *testing.T) {
				f := summonFixture(t)
				token := tokHuman
				raw, err := json.Marshal(task())
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				switch class {
				case "client":
					token = tokClient
				case "session":
					token = f.sessionPod("filing-parent", "full", 0)
				case "summoning-caller":
					token = tokStranger
					body = remediationBody("rem-responder-filing", nil)
				}
				filedFor := map[string]any{"name": "rem-responder-original", "uid": "00000000-0000-0000-0000-000000000801"}
				summon := map[string]any{"filedFor": filedFor, "dispatchID": id}
				switch field {
				case "filedFor":
					body[field] = filedFor
				case "dispatchID":
					body[field] = id
				case "summon":
					body[field] = summon
				case "spec":
					body[field] = map[string]any{"summon": summon}
				}
				var before v1alpha1.AgentSessionList
				if err := f.c.List(context.Background(), &before); err != nil {
					t.Fatal(err)
				}
				w := f.do(http.MethodPost, apiv1.SessionsPath, token, body)
				if w.Code < 400 || w.Code >= 500 {
					t.Fatalf("identity injection returned %d", w.Code)
				}
				var after v1alpha1.AgentSessionList
				if err := f.c.List(context.Background(), &after); err != nil {
					t.Fatal(err)
				}
				if len(after.Items) != len(before.Items) || len(f.ledger().Charges) != 0 {
					t.Fatal("refused identity injection created or charged")
				}
			})
		}
	}
	t.Run("valid-create-id-is-server-chosen", func(t *testing.T) {
		f := summonFixture(t)
		if w := summonPost(t, f, remediationBody("rem-responder-valid", nil)); w.Code != http.StatusCreated {
			t.Fatalf("valid create: %d", w.Code)
		}
		s := f.session("rem-responder-valid")
		if s.Spec.Summon.FiledFor != nil || strings.HasPrefix(s.Spec.Summon.DispatchID, "filing:") || len(s.Spec.Summon.DispatchID) != 32 {
			t.Fatal("caller acquired platform filing identity")
		}
	})
}
