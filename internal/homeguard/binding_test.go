package homeguard

import (
	"encoding/json"
	"testing"
)

func fixture() (Binding, []map[string]any) {
	b := Binding{"ns", "session", "session-uid", "pvc-uid", "pv", "pv-uid"}
	raw := []string{
		`{"metadata":{"namespace":"ns","name":"session","uid":"session-uid"},"spec":{"agent":"codex","mode":"remote","operatingMode":"Running"}}`,
		`{"metadata":{"namespace":"ns","name":"home-session","uid":"pvc-uid","finalizers":["dev-env.haynesops.com/rescue"],"ownerReferences":[{"apiVersion":"dev-env.haynesops.com/v1alpha1","kind":"AgentSession","name":"session","uid":"session-uid","controller":true}]},"spec":{"volumeName":"pv"},"status":{"phase":"Bound"}}`,
		`{"metadata":{"name":"pv","uid":"pv-uid","finalizers":["kubernetes.io/pv-protection"]},"spec":{"claimRef":{"apiVersion":"v1","kind":"PersistentVolumeClaim","namespace":"ns","name":"home-session","uid":"pvc-uid"}},"status":{"phase":"Bound"}}`}
	objs := make([]map[string]any, 3)
	for i, r := range raw {
		_ = json.Unmarshal([]byte(r), &objs[i])
	}
	return b, objs
}
func TestOriginalHomeBinding(t *testing.T) {
	tests := []struct {
		name   string
		change func([]map[string]any)
	}{
		{"unchanged", nil},
		{"missing PVC", func(o []map[string]any) { o[1] = nil }},
		{"replaced PVC", func(o []map[string]any) { o[1]["metadata"].(map[string]any)["uid"] = "new" }},
		{"foreign PVC", func(o []map[string]any) {
			o[1]["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)["uid"] = "other"
		}},
		{"deleting PVC", func(o []map[string]any) {
			o[1]["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-30T00:00:00Z"
		}},
		{"unprotected PVC", func(o []map[string]any) { delete(o[1]["metadata"].(map[string]any), "finalizers") }},
		{"changed volumeName", func(o []map[string]any) { o[1]["spec"].(map[string]any)["volumeName"] = "other" }},
		{"replaced PV", func(o []map[string]any) { o[2]["metadata"].(map[string]any)["uid"] = "new" }},
		{"deleting PV", func(o []map[string]any) {
			o[2]["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-30T00:00:00Z"
		}},
		{"changed claimRef", func(o []map[string]any) { o[2]["spec"].(map[string]any)["claimRef"].(map[string]any)["uid"] = "other" }},
		{"unprotected PV", func(o []map[string]any) { delete(o[2]["metadata"].(map[string]any), "finalizers") }},
		{"suspended start", func(o []map[string]any) { o[0]["spec"].(map[string]any)["operatingMode"] = "Suspended" }},
		{"new session", func(o []map[string]any) { o[0]["metadata"].(map[string]any)["uid"] = "new" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, o := fixture()
			if tt.change != nil {
				tt.change(o)
			}
			var raw [3][]byte
			for i, v := range o {
				raw[i], _ = json.Marshal(v)
			}
			err := b.Verify(raw[0], raw[1], raw[2], true)
			if (err == nil) != (tt.change == nil) {
				t.Fatalf("Verify=%v", err)
			}
		})
	}
}
func TestOriginalHomeSelect(t *testing.T) {
	b, _ := fixture()
	if got, err := Select(nil, "ns", "session", "session-uid", nil); got != nil || err != nil {
		t.Fatal("default off")
	}
	a := map[string]string{Annotation: b.String()}
	if got, err := Select(nil, "ns", "session", "session-uid", a); err != nil || *got != b {
		t.Fatal("binding disappeared with config")
	}
	if _, err := Select(&b, "ns", "session", "replacement", nil); err == nil {
		t.Fatal("replacement session admitted")
	}
	other := b
	other.PVCUID = "other"
	if _, err := Select(&other, "ns", "session", "session-uid", a); err == nil {
		t.Fatal("conflicting binding accepted")
	}
	for _, raw := range []string{"{}", b.String() + " {}", `{"namespace":"ns","unknown":1}`} {
		if _, err := Parse(raw); err == nil {
			t.Fatal("malformed binding admitted")
		}
	}
}
