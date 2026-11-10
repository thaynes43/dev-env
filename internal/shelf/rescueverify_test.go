package shelf

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const verifyID = "dev-env-1110-021204/20261110-0213"

func verifier(t *testing.T, e *fakeExec, slots *ExecSlots) *RescueVerifier {
	t.Helper()
	c := newClient(t, shelfPod("shelf-1", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), corev1.PodRunning, true))
	return &RescueVerifier{Shelf: &Shelf{Reader: c, Exec: e, Namespace: ns}, Slots: slots}
}

// Verify runs rescue-verify in the shelf with exactly the id given, under one
// exec slot it gives back, and a refusal on exit 1 is a result, not an error.
func TestRescueVerifierRunsUnderASlot(t *testing.T) {
	ctx := context.Background()
	sum := strings.Repeat("a", 64)
	slots := NewExecSlots(1)
	e := &fakeExec{out: `{"version":1,"id":"` + verifyID + `","manifest":"rescue/` + verifyID + `/manifest.json","manifestSHA256":"` + sum +
		`","bundles":2,"bytes":41984,"ok":true,"sharedFreeBytes":19327352832,"sharedTotalBytes":21474836480}` + "\n"}
	res, err := verifier(t, e, slots).Verify(ctx, verifyID)
	if err != nil || !res.OK || res.ManifestSHA256 != sum || res.Bundles != 2 || res.Bytes != 41984 || res.SharedFreeBytes != 19327352832 {
		t.Fatalf("verify: %+v %v", res, err)
	}
	if e.cmds[0] != "agentd ctl rescue-verify --id "+verifyID+" --timeout 50s" || e.pods[0] != ns+"/shelf-1/"+Container {
		t.Errorf("ran %q in %q", e.cmds[0], e.pods[0])
	}
	if release, ok := slots.TryTake(); !ok {
		t.Error("the slot was not given back")
	} else {
		release()
	}

	e = &fakeExec{out: `{"version":1,"ok":true,"bundles":0,"bytes":0,"sharedFreeBytes":1,"sharedTotalBytes":2}`}
	v := verifier(t, e, slots)
	v.Shelf.Timeout = 2 * time.Minute
	if res, err := v.Verify(ctx, ""); err != nil || !res.OK || e.cmds[0] != "agentd ctl rescue-verify --timeout 1m40s" {
		t.Fatalf("space only: %+v %v, ran %q", res, err, e.cmds)
	}
	if got := agentdDeadline(time.Second); got != time.Second {
		t.Errorf("a one-second exec gives agentd %s", got)
	}

	exit1 := utilexec.CodeExitError{Err: errors.New("command terminated"), Code: 1}
	for _, code := range []string{protocol.RescueVerifyBundleMissing, protocol.RescueVerifyBundleMismatch} {
		e = &fakeExec{out: `{"version":1,"id":"` + verifyID + `","ok":false,"refusal":"` + code + `","detail":"x","bundles":1,"bytes":3,"sharedFreeBytes":1,"sharedTotalBytes":2}`, err: exit1}
		res, err := verifier(t, e, slots).Verify(ctx, verifyID)
		if err != nil || res.OK || res.Refusal != code {
			t.Errorf("refusal %s: %+v %v", code, res, err)
		}
	}
}

// With every slot taken it does not exec; anything that is not the answer
// asked for is an error, never ok and never a refusal.
func TestRescueVerifierErrors(t *testing.T) {
	ctx := context.Background()
	sum := strings.Repeat("a", 64)
	slots := NewExecSlots(1)
	release, _ := slots.TryTake()
	e := &fakeExec{}
	if _, err := verifier(t, e, slots).Verify(ctx, verifyID); !errors.Is(err, ErrSlotsBusy) || len(e.cmds) != 0 {
		t.Fatalf("no slot: %v, ran %q", err, e.cmds)
	}
	release()

	exit := func(code int) error { return utilexec.CodeExitError{Err: errors.New("command terminated"), Code: code} }
	ok := func(edit string) string {
		doc := `{"version":1,"id":"` + verifyID + `","manifestSHA256":"` + sum + `","bundles":1,"bytes":3,"ok":true,"sharedFreeBytes":1,"sharedTotalBytes":2}`
		if edit != "" {
			doc = strings.Replace(doc, `"version":1`, edit, 1)
		}
		return doc
	}
	for _, tc := range []struct {
		name string
		e    *fakeExec
	}{
		{"usage", &fakeExec{errOut: "usage", err: exit(2)}},
		{"exit 1 with nothing on stdout", &fakeExec{errOut: "not mounted", err: exit(1)}},
		{"exit 1 that is not JSON", &fakeExec{out: "garbage", err: exit(1)}},
		{"exit 1 that says ok", &fakeExec{out: ok(""), err: exit(1)}},
		{"exit 1 with an unknown refusal", &fakeExec{out: `{"version":1,"id":"` + verifyID + `","ok":false,"refusal":"maybe","sharedFreeBytes":1,"sharedTotalBytes":2}`, err: exit(1)}},
		{"exit 0 that is not ok", &fakeExec{out: `{"version":1,"id":"` + verifyID + `","ok":false,"refusal":"bundle_missing","sharedFreeBytes":1,"sharedTotalBytes":2}`}},
		{"ok and refused", &fakeExec{out: ok(`"version":1,"refusal":"bundle_missing"`)}},
		{"ok without a digest", &fakeExec{out: strings.Replace(ok(""), sum, "", 1)}},
		{"another rescue", &fakeExec{out: strings.Replace(ok(""), verifyID, "other-1110-021204/20261110-0213", 1)}},
		{"another version", &fakeExec{out: ok(`"version":2`)}},
		{"no space figures", &fakeExec{out: strings.Replace(ok(""), `"sharedTotalBytes":2`, `"sharedTotalBytes":0`, 1)}},
		{"more free than total", &fakeExec{out: strings.Replace(ok(""), `"sharedFreeBytes":1`, `"sharedFreeBytes":3`, 1)}},
		{"exec failed", &fakeExec{err: errors.New("connection refused")}},
		{"not JSON", &fakeExec{out: "garbage"}},
		{"too long", &fakeExec{out: strings.Repeat("x", 64<<10+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if res, err := verifier(t, tc.e, slots).Verify(ctx, verifyID); err == nil {
				t.Errorf("%+v, want an error", res)
			}
		})
	}
	if _, err := verifier(t, &fakeExec{}, slots).Verify(ctx, "../x"); err == nil {
		t.Error("a bad id was sent")
	}
	if _, err := (&RescueVerifier{Shelf: verifier(t, &fakeExec{}, nil).Shelf}).Verify(ctx, verifyID); err == nil {
		t.Error("ran without slots")
	}
	if _, err := (&RescueVerifier{Shelf: &Shelf{Reader: newClient(t), Exec: &fakeExec{}, Namespace: ns}, Slots: slots}).Verify(ctx, verifyID); !errors.Is(err, ErrNoShelf) {
		t.Errorf("no shelf: %v", err)
	}
	if _, err := (&RescueVerifier{}).Verify(ctx, verifyID); err == nil {
		t.Error("a verifier without a shelf answered")
	}
}
