//go:build linux

package hostexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestProbeRefusalCodesNeverGrantEvidence(t *testing.T) {
	for code := 0; code <= 255; code++ {
		if ValidEvidenceExit(code) != (code >= 40 && code <= 44) {
			t.Fatalf("acceptance changed for%d", code)
		}
	}
	for code := ProbeRefusedArguments; code <= ProbeRefusedResponseOversize; code++ {
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

func TestProbeEvidenceAuthenticatedResponseFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		consume      bool
		reply        string
		waitDeadline bool
		want         int
	}{
		{"unread-request-reset", false, "", false, ProbeRefusedResponseReset},
		{"partial-response-reset", false, "{\"version\":1}", false, ProbeRefusedResponseReset},
		{"empty-eof", true, "", false, ProbeRefusedIdentity},
		{"truncated-json", true, "{\"version\":", false, ProbeRefusedIdentity},
		{"oversize", true, strings.Repeat(" ", 8193), false, ProbeRefusedResponseOversize},
		{"finite-read-deadline", true, "", true, ProbeRefusedResponseTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "evidence-response-")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(dir) }()
			path := filepath.Join(dir, "guard.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			if err = listener.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			releasePeer := make(chan struct{})
			go func() {
				conn, err := listener.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = conn.Close() }()
				if err = conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
					done <- err
					return
				}
				if tc.consume {
					var req json.RawMessage
					if err = json.NewDecoder(conn).Decode(&req); err != nil {
						done <- err
						return
					}
				} else {
					// Wait for real pending bytes, then close without consuming them.
					// Linux resets this authenticated socket; a partial reply is no proof.
					raw, e := conn.SyscallConn()
					if e != nil {
						done <- e
						return
					}
					if e = raw.Control(func(fd uintptr) {
						fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
						n, pollErr := unix.Poll(fds, 200)
						if pollErr != nil || n != 1 {
							err = fmt.Errorf("request not pending: %d %w", n, pollErr)
						}
					}); e != nil {
						done <- e
						return
					}
					if err != nil {
						done <- err
						return
					}
				}
				if tc.waitDeadline {
					// Keep the peer open until the read itself reports its deadline.
					// A close racing that deadline would instead produce empty EOF.
					<-releasePeer
				}
				if tc.reply != "" {
					_, err = conn.Write([]byte(tc.reply))
				}
				done <- err
			}()
			b := Binding{TaskUID: "owned-luna-1019-0004", Epoch: 1, HostID: "owned-luna-1019-4", PodUID: "original-pod", Deadline: time.Now().UTC().Add(time.Minute)}
			proof, err := ProbeEvidence(ctx, path, os.Getpid(), b, strings.Repeat("ab", 32))
			close(releasePeer)
			if err == nil || ProbeRefusalExit(err) != tc.want || proof.Version != 0 || ValidEvidenceExit(ProbeRefusalExit(err)) {
				t.Fatalf("refusal=%d want%d proof=%+v err=%v", ProbeRefusalExit(err), tc.want, proof, err)
			}
			if serverErr := <-done; serverErr != nil {
				t.Fatal(serverErr)
			}
		})
	}
}

func TestEvidenceServerReportsFixedRefusalWithoutPeerContents(t *testing.T) {
	c := config(t)
	baseline, err := NativeHomeBaseline(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	reports := make(chan string, 1)
	e := &EvidenceConfig{HomeUID: "home", Baseline: baseline, ReportRefusal: func(stage string) { reports <- stage }}
	if err = e.start(c); err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	b := c.Binding
	b.TaskUID = "untrusted-peer-data-not-for-logs"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	proof, err := ProbeEvidence(ctx, filepath.Join(filepath.Dir(c.ReceiptPath), EvidenceSocketName), os.Getpid(), b, strings.Repeat("ab", 32))
	if err == nil || proof.Version != 0 {
		t.Fatal("wrong binding accepted")
	}
	select {
	case stage := <-reports:
		if stage != "request-binding" {
			t.Fatalf("unexpected diagnostic %q", stage)
		}
	case <-ctx.Done():
		t.Fatal("guard-side refusal not reported")
	}
}

func TestEvidenceServerBlockedDiagnosticDoesNotHoldProofOrClose(t *testing.T) {
	c := config(t)
	baseline, err := NativeHomeBaseline(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	e := &EvidenceConfig{HomeUID: "home", Baseline: baseline, ReportRefusal: func(string) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}}
	if err = e.start(c); err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	path := filepath.Join(filepath.Dir(c.ReceiptPath), EvidenceSocketName)
	bad := c.Binding
	bad.TaskUID = "wrong-task"
	if _, err = ProbeEvidence(ctx, path, os.Getpid(), bad, strings.Repeat("ab", 32)); err == nil {
		t.Fatal("wrong task accepted")
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("diagnostic not entered")
	}
	// Fill and then overflow the bounded report queue while its sink is blocked.
	for i := 0; i < 2; i++ {
		if _, err = ProbeEvidence(ctx, path, os.Getpid(), bad, strings.Repeat("ab", 32)); err == nil {
			t.Fatal("wrong task accepted")
		}
	}
	proof, err := ProbeEvidence(ctx, path, os.Getpid(), c.Binding, strings.Repeat("ab", 32))
	if err != nil || proof.Stage != "Prepared" {
		t.Fatal("blocked diagnostic held valid proof", err)
	}
	closed := make(chan struct{})
	go func() { e.Close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("blocked diagnostic held server shutdown")
	}
}

func TestEvidenceServerConsumesOneCompleteRequestBeforeReply(t *testing.T) {
	for _, tc := range []struct {
		name       string
		suffix     string
		fragmented bool
		want       bool
	}{
		{"encoder-newline", "\n", false, true},
		{"fragmented-newline", "\n", true, true},
		{"second-json-value", "\n{}\n", false, false},
		{"trailing-junk", "\njunk", false, false},
		{"request-byte-limit", strings.Repeat(" ", 4097), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config(t)
			baseline, err := NativeHomeBaseline(c.Home)
			if err != nil {
				t.Fatal(err)
			}
			e := &EvidenceConfig{HomeUID: "home", Baseline: baseline}
			if err = e.start(c); err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Net: "unix", Name: filepath.Join(filepath.Dir(c.ReceiptPath), EvidenceSocketName)})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			anchor, err := identity(os.Getpid())
			if err != nil || !ownedSocketPeer(conn, anchor) {
				t.Fatal("real guard peer unavailable", err)
			}
			if err = conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			challenge := strings.Repeat("ab", 32)
			raw, err := json.Marshal(struct {
				Challenge string  `json:"challenge"`
				Binding   Binding `json:"binding"`
			}{challenge, c.Binding})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = conn.Write(raw); err != nil {
				t.Fatal(err)
			}
			if tc.fragmented {
				if err = conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				one := make([]byte, 1)
				_, err = conn.Read(one)
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatal("reply preceded complete request EOF", err)
				}
				if err = conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = conn.Write([]byte(tc.suffix)); err != nil {
				t.Fatal(err)
			}
			if err = conn.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			reply, readErr := io.ReadAll(io.LimitReader(conn, 8193))
			var proof Observation
			decodeErr := json.Unmarshal(reply, &proof)
			accepted := readErr == nil && decodeErr == nil && proof.Challenge == challenge && EvidenceExit(proof, c.Binding, "home", baseline) == EvidencePrepared
			if accepted != tc.want {
				t.Fatalf("accepted=%v want=%v readErr=%v decodeErr=%v", accepted, tc.want, readErr, decodeErr)
			}
		})
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
