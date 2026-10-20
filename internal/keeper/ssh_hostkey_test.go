package keeper

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // known_hosts hashing, test fixture only.
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func testECDSAKey(t *testing.T) ssh.Signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// multiKeyNode is a fake PVE node: it holds several host keys, as a real node
// does (ECDSA, RSA and ED25519), and answers every exec with the reply func.
func multiKeyNode(t *testing.T, ca ssh.Signer, reply func(string) string, hostKeys ...ssh.Signer) (addr string, commands *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	config := &ssh.ServerConfig{PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		cert, ok := key.(*ssh.Certificate)
		if !ok || conn.User() != "dev-env" || !bytes.Equal(cert.SignatureKey.Marshal(), ca.PublicKey().Marshal()) {
			return nil, errors.New("wrong minter identity")
		}
		if err := (&ssh.CertChecker{SupportedCriticalOptions: []string{"force-command"}}).CheckCert(MinterPrincipal, cert); err != nil {
			return nil, err
		}
		return &cert.Permissions, nil
	}}
	for _, k := range hostKeys {
		config.AddHostKey(k)
	}
	commands = &atomic.Int32{}
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				server, channels, requests, e := ssh.NewServerConn(conn, config)
				if e != nil {
					_ = conn.Close()
					return
				}
				defer func() { _ = server.Close() }()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, e := incoming.Accept()
					if e != nil {
						return
					}
					for request := range requests {
						if request.Type != "exec" {
							_ = request.Reply(false, nil)
							continue
						}
						var payload struct{ Command string }
						if ssh.Unmarshal(request.Payload, &payload) != nil {
							_ = request.Reply(false, nil)
							break
						}
						commands.Add(1)
						_ = request.Reply(true, nil)
						_, _ = io.WriteString(channel, reply(payload.Command))
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						_ = channel.Close()
						break
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), commands
}

func nativeFor(t *testing.T, dir, addr, hostsContent string) *NativeSSH {
	t.Helper()
	targets := filepath.Join(dir, "targets.json")
	if err := os.WriteFile(targets, []byte(`["minter.example:22"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts := filepath.Join(dir, "known-hosts")
	if err := os.WriteFile(hosts, []byte(hostsContent), 0o600); err != nil {
		t.Fatal(err)
	}
	return &NativeSSH{CADir: dir, TargetsFile: targets, KnownHostsFile: hosts, dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}
}

const listTokensCommand = "sudo -n /usr/bin/pvesh get /access/users/dev-env@pve/token --output-format json"

// The production failure: the node serves ECDSA, RSA and ED25519 host keys, the
// trust file pins only ED25519, and the library's default order prefers ECDSA.
func TestNativeSSHConnectsWhenNodeServesMoreKeysThanArePinned(t *testing.T) {
	dir, ca := writeTestSSHCA(t)
	ed := testSSHKey(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	// ECDSA and RSA are added first so a server that prefers them would pick them.
	addr, commands := multiKeyNode(t, ca, func(string) string { return "[]" }, testECDSAKey(t), rsaSigner, ed)
	native := nativeFor(t, dir, addr, knownhosts.Line([]string{"minter.example"}, ed.PublicKey())+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, dispatched, err := native.Run(ctx, listTokensCommand)
	if err != nil || !dispatched || string(out) != "[]" || commands.Load() != 1 {
		t.Fatalf("multi-key node was not reachable with an ED25519-only pin: %v", err)
	}
}

// Narrowing the offered algorithms must not weaken the check: a node whose
// ED25519 key is not the pinned one is refused, even though it offers that type.
func TestNativeSSHRefusesWhenPinnedKeyDiffers(t *testing.T) {
	dir, ca := writeTestSSHCA(t)
	pinned := testSSHKey(t)
	served := testSSHKey(t)
	addr, commands := multiKeyNode(t, ca, func(string) string { return "[]" }, testECDSAKey(t), served)
	native := nativeFor(t, dir, addr, knownhosts.Line([]string{"minter.example"}, pinned.PublicKey())+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, dispatched, err := native.Run(ctx, listTokensCommand)
	if err == nil || dispatched || commands.Load() != 0 {
		t.Fatal("a node presenting a different key than the pin was trusted")
	}
}

// A node that only has keys of a type that is not pinned is refused, and an
// unpinned target never falls back to accepting any key.
func TestNativeSSHRefusesUnpinnedKeyType(t *testing.T) {
	dir, ca := writeTestSSHCA(t)
	addr, commands := multiKeyNode(t, ca, func(string) string { return "[]" }, testECDSAKey(t))
	native := nativeFor(t, dir, addr, knownhosts.Line([]string{"minter.example"}, testSSHKey(t).PublicKey())+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, dispatched, err := native.Run(ctx, listTokensCommand); err == nil || dispatched || commands.Load() != 0 {
		t.Fatal("an unpinned host key type was trusted")
	}
	// Same for a target with no entry at all.
	native = nativeFor(t, dir, addr, knownhosts.Line([]string{"other.example"}, testSSHKey(t).PublicKey())+"\n")
	if _, dispatched, err := native.Run(ctx, listTokensCommand); err == nil || dispatched || commands.Load() != 0 {
		t.Fatal("a target with no pin was trusted")
	}
}

func TestHostKeyAlgorithmsFollowThePinsForOneTarget(t *testing.T) {
	ed, ec := testSSHKey(t), testECDSAKey(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSigner, _ := ssh.NewSignerFromKey(rsaKey)
	salt := []byte("0123456789abcdefghij")
	mac := hmac.New(sha1.New, salt) //nolint:gosec // fixture.
	_, _ = mac.Write([]byte("hashed.example"))
	hashed := "|1|" + base64.StdEncoding.EncodeToString(salt) + "|" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	file := strings.Join([]string{
		"# comment",
		knownhosts.Line([]string{"a.example"}, ed.PublicKey()),
		knownhosts.Line([]string{"b.example", "10.0.0.2"}, ec.PublicKey()),
		knownhosts.Line([]string{"b.example"}, rsaSigner.PublicKey()),
		"@cert-authority b.example " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(ed.PublicKey()))),
		knownhosts.Line([]string{"[c.example]:2222"}, ec.PublicKey()),
		knownhosts.Line([]string{hashed}, ed.PublicKey()),
		knownhosts.Line([]string{"*.glob.example", "!bad.glob.example"}, ec.PublicKey()),
		"not a valid line",
	}, "\n")
	for _, tc := range []struct {
		target string
		want   []string
	}{
		{"a.example:22", []string{ssh.KeyAlgoED25519}},
		{"b.example:22", []string{ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}},
		{"10.0.0.2:22", []string{ssh.KeyAlgoECDSA256}},
		{"hashed.example:22", []string{ssh.KeyAlgoED25519}},
		{"x.glob.example:22", []string{ssh.KeyAlgoECDSA256}},
		{"bad.glob.example:22", nil},
		{"c.example:22", nil},
		{"unknown.example:22", nil},
	} {
		if got := hostKeyAlgorithms([]byte(file), tc.target); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.target, got, tc.want)
		}
	}
}

// Cleanup of a token that was never minted (the two stuck jobs of 2026-10-19):
// once SSH works, the list proves absence, no delete is sent, and cleanup is done.
func TestProxmoxCleanupOfNeverMintedTokenIsIdempotent(t *testing.T) {
	f := newCredentialFixture(t)
	dir, ca := writeTestSSHCA(t)
	ed := testSSHKey(t)
	var deletes atomic.Int32
	other, _ := json.Marshal([]map[string]any{{"tokenid": "grant-someoneelse", "expire": 1, "privsep": 0, "comment": "x"}})
	for name, listing := range map[string]string{"empty list": "[]", "only other tokens": string(other)} {
		t.Run(name, func(t *testing.T) {
			addr, commands := multiKeyNode(t, ca, func(command string) string {
				if strings.Contains(command, " delete ") {
					deletes.Add(1)
				}
				return listing
			}, testECDSAKey(t), ed)
			native := nativeFor(t, dir, addr, knownhosts.Line([]string{"minter.example"}, ed.PublicKey())+"\n")
			p := &proxmoxProvider{SSH: native}
			e := credentialEntry{JobUID: testJobUID, Spec: f.job.Spec, Stage: journalCleanup}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			gone, err := p.remove(ctx, e)
			if err != nil || !gone {
				t.Fatalf("cleanup of an absent token did not complete: gone=%v err=%v", gone, err)
			}
			if deletes.Load() != 0 || commands.Load() != 1 {
				t.Fatal("cleanup of an absent token ran a delete or more than one list")
			}
		})
	}
}
