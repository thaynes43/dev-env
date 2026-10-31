package v1alpha1_test

import (
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestTaskOutcomeSchema(t *testing.T) {
	c := ctx(t)
	s := testutil.New(t, "claude", "dev/client").Session
	s.Name, s.Namespace, s.UID, s.ResourceVersion = name(), ns, "", ""
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	valid := &v1alpha1.TaskOutcomeStatus{SessionUID: string(s.UID), PodUID: "original-pod", Record: v1alpha1.TaskOutcomeRecordStatus{Summary: strings.Repeat("界", 200), PRURLs: []string{"https://github.com/thaynes43/dev-env/pull/1"}, HeadSHAs: []string{strings.Repeat("a", 40)}}}
	for _, kind := range []string{"URL", "org", "SHA", "summary", "control", "PR count", "SHA count"} {
		t.Run(kind, func(t *testing.T) {
			bad := s.DeepCopy()
			bad.Status.TaskOutcome = valid.DeepCopy()
			r := &bad.Status.TaskOutcome.Record
			switch kind {
			case "URL":
				r.PRURLs[0] += "?q=1"
			case "org":
				r.PRURLs[0] = "https://github.com/other/dev-env/pull/1"
			case "SHA":
				r.HeadSHAs[0] = "bad"
			case "summary":
				r.Summary = strings.Repeat("x", 201)
			case "control":
				r.Summary = "one\ntwo"
			case "PR count":
				r.PRURLs = append(r.PRURLs, r.PRURLs[0], r.PRURLs[0], r.PRURLs[0])
			case "SHA count":
				r.HeadSHAs = append(r.HeadSHAs, r.HeadSHAs[0], r.HeadSHAs[0], r.HeadSHAs[0])
			}
			if err := k8s.Status().Update(c, bad); !apierrors.IsInvalid(err) {
				t.Fatal("invalid outcome accepted", err)
			}
		})
	}
	s.Status.TaskOutcome = valid
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("valid outcome refused", err)
	}
	if s.Status.TaskOutcome == nil {
		t.Fatal("outcome pruned")
	}
	for _, kind := range []string{"second write", "remove field", "remove status", "null status"} {
		t.Run(kind, func(t *testing.T) {
			bad := s.DeepCopy()
			var err error
			switch kind {
			case "second write":
				bad.Status.TaskOutcome.Record.Summary = "replacement"
				err = k8s.Status().Update(c, bad)
			case "remove field":
				bad.Status.TaskOutcome = nil
				err = k8s.Status().Update(c, bad)
			case "remove status":
				err = k8s.Status().Patch(c, bad, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"remove","path":"/status"}]`)))
			case "null status":
				err = k8s.Status().Patch(c, bad, client.RawPatch(types.MergePatchType, []byte(`{"status":null}`)))
			}
			if !apierrors.IsInvalid(err) {
				t.Fatal("immutable outcome changed", err)
			}
		})
	}
}
