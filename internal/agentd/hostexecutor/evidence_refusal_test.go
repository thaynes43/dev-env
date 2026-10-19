//go:build linux

package hostexecutor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeRefusalCodesNeverGrantEvidence(t *testing.T) {
	for code := 0; code <= 255; code++ {
		if ValidEvidenceExit(code) != (code >= 40 && code <= 44) {
			t.Fatalf("acceptance changed for%d", code)
		}
	}
	for code := ProbeRefusedArguments; code <= ProbeRefusedPredicate; code++ {
		if ValidEvidenceExit(code) || ProbeRefusalStage(code) == "unknown" {
			t.Fatalf("refusal%d", code)
		}
		err := probeRefusal{code, errors.New("untrusted error contents")}
		if ProbeRefusalExit(err) != code {
			t.Fatal("stage lost")
		}
	}
	if ProbeRefusalExit(errors.New("untrusted generic error")) != 1 || ProbeRefusalStage(40) != "unknown" {
		t.Fatal("unknown became diagnostic proof")
	}
}
func TestProbeEvidenceRetainsSpecificRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b := Binding{TaskUID: "fixture", Epoch: 1, HostID: "fixture", PodUID: "fixture", Deadline: time.Now().Add(time.Minute)}
	for _, tc := range []struct {
		name      string
		anchor    int
		challenge string
		want      int
	}{
		{"challenge", os.Getpid(), "invalid", ProbeRefusedChallenge},
		{"guard-identity", 1 << 30, strings.Repeat("a", 64), ProbeRefusedGuardIdentity},
		{"socket-connect", os.Getpid(), strings.Repeat("a", 64), ProbeRefusedConnect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProbeEvidence(ctx, filepath.Join(t.TempDir(), "absent.sock"), tc.anchor, b, tc.challenge)
			if err == nil || ProbeRefusalExit(err) != tc.want {
				t.Fatalf("refusal=%d err=%v", ProbeRefusalExit(err), err)
			}
		})
	}
}
