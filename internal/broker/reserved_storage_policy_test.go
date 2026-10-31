package broker

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Evaluate the actual proposed policy, including its selection variables and
// DELETE oldObject. Lookups and time are injected; this does not prove Kyverno
// runtime/compiler/RBAC, admission installation or a real deletion.
func reservedStoragePolicyAllowed(t *testing.T, grant, subject, target map[string]any, actor string, uid string, now time.Time) bool {
	t.Helper()
	raw, err := os.ReadFile("testdata/proposed-haynes-ops/grant-storage-targets.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Spec struct {
			Variables []struct {
				Name       string `json:"name"`
				Expression string `json:"expression"`
			} `json:"variables"`
			Validations []struct {
				Expression string `json:"expression"`
			} `json:"validations"`
		} `json:"spec"`
	}
	if err := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096).Decode(&policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Spec.Validations) != 3 {
		t.Fatal("missing storage policy validations")
	}
	lookup := func(args ...ref.Val) ref.Val {
		if len(args) != 4 || string(args[2].(celtypes.String)) != "dev-agents" || string(args[3].(celtypes.String)) != "grant-storage" {
			return celtypes.NewErr("lookup absent")
		}
		switch string(args[0].(celtypes.String)) + "/" + string(args[1].(celtypes.String)) {
		case "dev-env.haynesops.com/v1alpha1/accessgrants":
			if grant == nil {
				return celtypes.NewErr("own grant missing")
			}
			return celtypes.DefaultTypeAdapter.NativeToValue(grant)
		case "v1/serviceaccounts":
			if subject == nil {
				return celtypes.NewErr("own subject missing")
			}
			return celtypes.DefaultTypeAdapter.NativeToValue(subject)
		}
		return celtypes.NewErr("unexpected lookup")
	}
	env, err := cel.NewEnv(cel.OptionalTypes(), ext.Strings(), cel.Variable("request", cel.DynType), cel.Variable("oldObject", cel.DynType), cel.Variable("variables", cel.DynType),
		cel.Function("json.unmarshal", cel.Overload("storage_json_unmarshal", []*cel.Type{cel.StringType}, cel.DynType, cel.UnaryBinding(func(value ref.Val) ref.Val {
			var doc any
			if err := json.Unmarshal([]byte(value.(celtypes.String)), &doc); err != nil {
				return celtypes.NewErr("invalid JSON")
			}
			return celtypes.DefaultTypeAdapter.NativeToValue(doc)
		}))),
		cel.Function("resource.Get", cel.Overload("resource_get_four_strings", []*cel.Type{cel.StringType, cel.StringType, cel.StringType, cel.StringType}, cel.DynType, cel.FunctionBinding(lookup))),
		cel.Function("time.now", cel.Overload("storage_now", nil, cel.TimestampType, cel.FunctionBinding(func(...ref.Val) ref.Val { return celtypes.Timestamp{Time: now} }))))
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]any{}
	request := map[string]any{"operation": "DELETE", "name": "data", "namespace": "database", "userInfo": map[string]any{"username": actor, "uid": uid}}
	args := map[string]any{"request": request, "oldObject": target, "variables": vars}
	evaluate := func(expression string) (ref.Val, error) {
		ast, issues := env.Compile(expression)
		if issues.Err() != nil {
			t.Fatalf("policy compile: %v", issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		value, _, err := program.Eval(args)
		return value, err
	}
	for _, v := range policy.Spec.Variables {
		value, err := evaluate(v.Expression)
		if err != nil || celtypes.IsError(value) {
			return false
		}
		vars[v.Name] = value.Value()
	}
	for _, v := range policy.Spec.Validations {
		value, err := evaluate(v.Expression)
		if err != nil || value != celtypes.True {
			return false
		}
	}
	return true
}

func storagePolicyObjects(t *testing.T) (map[string]any, map[string]any, map[string]any, time.Time) {
	t.Helper()
	var objects []map[string]any
	if err := json.Unmarshal([]byte(`[
 {"metadata":{"name":"grant-storage","namespace":"dev-agents","uid":"grant-uid"},
  "spec":{"type":"kube","requester":{"sessionUID":"requester-uid"},"kube":{"role":"dev-env-grant-storage","namespaces":["database"],"storageTargets":[{"kind":"PersistentVolumeClaim","name":"data","uid":"object-uid"}]}},
  "status":{"phase":"Active","serviceAccount":"grant-storage","approvedBy":"delegate/dev-env-system/actor","expiresAt":"2026-10-31T20:00:00Z",
   "reserved":{"case":"rd4-storage","boundsRevision":"1","grantUID":"grant-uid","requesterUID":"requester-uid","requesterPodUID":"requester-pod","actorUID":"actor-pod","tomRef":"msg-tom-1","scopeDigest":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","effectiveDigest":"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB","ttl":"30m","decidedAt":"2026-10-31T19:00:00Z"}}},
 {"metadata":{"name":"grant-storage","namespace":"dev-agents","uid":"sa-uid","labels":{"dev-env.haynesops.com/grant":"grant-storage","dev-env.haynesops.com/grant-uid":"grant-uid"}}},
 {"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"name":"data","namespace":"database","uid":"object-uid"}}
]`), &objects); err != nil {
		t.Fatal(err)
	}
	decision, _ := json.Marshal(map[string]any{"approve": true, "by": "delegate/dev-env-system/actor", "grantUID": "grant-uid", "reserved": objects[0]["status"].(map[string]any)["reserved"]})
	objects[0]["metadata"].(map[string]any)["annotations"] = map[string]any{MaintenanceDecisionAnnotation: string(decision)}
	return objects[0], objects[1], objects[2], time.Date(2026, 10, 31, 19, 30, 0, 0, time.UTC)
}

func TestReservedStoragePolicyOwnActiveGrantExactUID(t *testing.T) {
	g, sa, target, now := storagePolicyObjects(t)
	if !reservedStoragePolicyAllowed(t, g, sa, target, "system:serviceaccount:dev-agents:grant-storage", "sa-uid", now) {
		t.Fatal("own Active exact UID denied")
	}
	for _, kind := range []string{"StatefulSet", "VolumeSnapshot"} {
		g, sa, target, now = storagePolicyObjects(t)
		target["kind"] = kind
		g["spec"].(map[string]any)["kube"].(map[string]any)["storageTargets"].([]any)[0].(map[string]any)["kind"] = kind
		if !reservedStoragePolicyAllowed(t, g, sa, target, "system:serviceaccount:dev-agents:grant-storage", "sa-uid", now) {
			t.Fatal("exact kind denied", kind)
		}
	}
}

func TestReservedStoragePolicyRefusals(t *testing.T) {
	for _, mode := range []string{"missing-own-grant", "missing-subject", "other-grant-only", "same-name-replaced-object", "other-namespace", "other-kind", "wrong-sa-uid", "sa-other-grant-uid", "stale-decision-grant-uid", "wrong-requester-uid", "no-ref", "wrong-ref", "other-valid-ref", "no-decision", "denial", "no-reserved", "wrong-class", "wrong-revision", "ended", "released", "expired", "no-expiry", "grant-deleting", "subject-deleting", "wrong-serviceaccount", "attested", "missing-object-uid", "null-old-object", "self-actor", "long-ttl"} {
		t.Run(mode, func(t *testing.T) {
			g, sa, target, now := storagePolicyObjects(t)
			actor, uid := "system:serviceaccount:dev-agents:grant-storage", "sa-uid"
			status := g["status"].(map[string]any)
			bounds := status["reserved"].(map[string]any)
			switch mode {
			case "missing-own-grant":
				g = nil
			case "missing-subject":
				sa = nil
			case "other-grant-only":
				actor = "system:serviceaccount:dev-agents:grant-other"
			case "same-name-replaced-object":
				target["metadata"].(map[string]any)["uid"] = "replacement"
			case "other-namespace":
				target["metadata"].(map[string]any)["namespace"] = "other"
			case "other-kind":
				target["kind"] = "StatefulSet"
			case "wrong-sa-uid":
				uid = "replacement"
			case "sa-other-grant-uid":
				sa["metadata"].(map[string]any)["labels"].(map[string]any)["dev-env.haynesops.com/grant-uid"] = "other"
			case "stale-decision-grant-uid":
				bounds["grantUID"] = "old"
			case "wrong-requester-uid":
				bounds["requesterUID"] = "other"
			case "other-valid-ref":
				bounds["tomRef"] = "other-message"
			case "no-decision":
				delete(g["metadata"].(map[string]any), "annotations")
			case "denial":
				decision, _ := json.Marshal(map[string]any{"approve": false, "by": "delegate/dev-env-system/actor", "grantUID": "grant-uid", "reserved": bounds})
				g["metadata"].(map[string]any)["annotations"] = map[string]any{MaintenanceDecisionAnnotation: string(decision)}
			case "no-ref":
				delete(bounds, "tomRef")
			case "wrong-ref":
				bounds["tomRef"] = "bad ref"
			case "no-reserved":
				delete(status, "reserved")
			case "wrong-class":
				bounds["case"] = "rd3a"
			case "wrong-revision":
				bounds["boundsRevision"] = "next"
			case "ended":
				status["phase"] = "Released"
			case "released":
				g["spec"].(map[string]any)["release"] = true
			case "expired":
				now = now.Add(time.Hour)
			case "no-expiry":
				delete(status, "expiresAt")
			case "grant-deleting":
				g["metadata"].(map[string]any)["deletionTimestamp"] = now.Format(time.RFC3339)
			case "subject-deleting":
				sa["metadata"].(map[string]any)["deletionTimestamp"] = now.Format(time.RFC3339)
			case "wrong-serviceaccount":
				status["serviceAccount"] = "grant-other"
			case "attested":
				status["approvedBy"] = "policy/standing"
			case "missing-object-uid":
				delete(target["metadata"].(map[string]any), "uid")
			case "null-old-object":
				target = nil
			case "self-actor":
				bounds["actorUID"] = "requester-pod"
			case "long-ttl":
				bounds["ttl"] = "2h"
			}
			if reservedStoragePolicyAllowed(t, g, sa, target, actor, uid, now) {
				t.Fatal("unsafe storage DELETE admitted", mode)
			}
		})
	}
	g, sa, target, now := storagePolicyObjects(t)
	for _, phase := range strings.Fields("Pending Denied Expired Released Failed") {
		g["status"].(map[string]any)["phase"] = phase
		if reservedStoragePolicyAllowed(t, g, sa, target, "system:serviceaccount:dev-agents:grant-storage", "sa-uid", now) {
			t.Fatal("ended/nonactive grant admitted", phase)
		}
	}
}
