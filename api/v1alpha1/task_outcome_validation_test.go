package v1alpha1_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
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
	for _, kind := range []string{"URL", "org", "SHA", "summary", "control", "PR count", "SHA count", "dot segment", "dot-dot segment"} {
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
			case "dot segment":
				r.PRURLs[0] = "https://github.com/thaynes43/./pull/1"
			case "dot-dot segment":
				r.PRURLs[0] = "https://github.com/thaynes43/../pull/1"
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

// Every summary rune the operator's validator accepts must also pass this API
// server's schema, whose pattern uses the API server's own Go Unicode tables
// (envtest follows the cluster's Kubernetes minor). Otherwise the operator's
// status write is refused as Invalid. Dry-run writes persist nothing.
func TestTaskOutcomeSchemaAcceptsEveryOperatorRune(t *testing.T) {
	c := ctx(t)
	s := testutil.New(t, "claude", "dev/client").Session
	s.Name, s.Namespace, s.UID, s.ResourceVersion = name(), ns, "", ""
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	var accepted []rune
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if utf8.ValidRune(r) && (protocol.TaskOutcomeRecord{Summary: string(r)}).Validate() == nil {
			accepted = append(accepted, r)
		}
	}
	if len(accepted) < 1000 {
		t.Fatalf("only %d summary runes accepted", len(accepted))
	}
	write := func(summary string, prURLs ...string) error {
		candidate := s.DeepCopy()
		candidate.Status.TaskOutcome = &v1alpha1.TaskOutcomeStatus{SessionUID: string(s.UID), PodUID: "original-pod", Record: v1alpha1.TaskOutcomeRecordStatus{Summary: summary, PRURLs: prURLs}}
		return k8s.Status().Update(c, candidate, client.DryRunAll)
	}
	for start := 0; start < len(accepted); start += 200 {
		chunk := accepted[start:min(start+200, len(accepted))]
		if err := write(string(chunk)); err != nil {
			t.Fatalf("schema refused runes U+%04X..U+%04X the operator accepts: %v", chunk[0], chunk[len(chunk)-1], err)
		}
	}
	urls := []string{"https://github.com/thaynes43/.github/pull/1", "https://github.com/thaynes43/a.b_c-d/pull/2"}
	for _, u := range urls {
		if err := (protocol.TaskOutcomeRecord{Summary: "ok", PRURLs: []string{u}}).Validate(); err != nil {
			t.Fatal(u, err)
		}
	}
	if err := write("ok", urls...); err != nil {
		t.Fatal("schema refused PR URLs the operator accepts", err)
	}
	if got := k8s.Get(c, client.ObjectKeyFromObject(s), s); got != nil || s.Status.TaskOutcome != nil {
		t.Fatal("dry run persisted an outcome", got)
	}
}
