package keeper

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func testSSHKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
func writeTestSSHCA(t *testing.T) (string, ssh.Signer) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "private-key"), pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "public-key"), ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, signer
}
func TestMinterCertificateHasDedicatedPrincipalAndOneForcedCommand(t *testing.T) {
	s := &NativeSSH{Now: func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }}
	command := "sudo -n /usr/bin/pvesh get /access/users/dev-env@pve/token --output-format json"
	signer, err := s.signer(testSSHKey(t), command)
	if err != nil {
		t.Fatal(err)
	}
	cert, ok := signer.PublicKey().(*ssh.Certificate)
	if !ok {
		t.Fatal("ephemeral certificate missing")
	}
	if cert.Serial == 0 || cert.CertType != ssh.UserCert || len(cert.ValidPrincipals) != 1 || cert.ValidPrincipals[0] != MinterPrincipal || len(cert.Extensions) != 0 || len(cert.CriticalOptions) != 1 || cert.CriticalOptions["force-command"] != command {
		t.Fatal("minter certificate broadens the ratified contract")
	}
	if time.Unix(int64(cert.ValidBefore), 0).Sub(s.now()) != 2*time.Minute {
		t.Fatal("certificate is not bounded to a short mint operation")
	}
	if err = (&ssh.CertChecker{SupportedCriticalOptions: []string{"force-command"}, Clock: s.now}).CheckCert(MinterPrincipal, cert); err != nil {
		t.Fatal(err)
	}
}

func TestNativeSSHPinsHostKeyAndFencesDispatch(t *testing.T) {
	dir, ca := writeTestSSHCA(t)
	host := testSSHKey(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	certs := make(chan *ssh.Certificate, 4)
	commands := make(chan string, 4)
	config := &ssh.ServerConfig{PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		cert, ok := key.(*ssh.Certificate)
		if !ok || conn.User() != "dev-env" || !bytes.Equal(cert.SignatureKey.Marshal(), ca.PublicKey().Marshal()) {
			return nil, errors.New("wrong minter identity")
		}
		checker := ssh.CertChecker{SupportedCriticalOptions: []string{"force-command"}}
		if err := checker.CheckCert(MinterPrincipal, cert); err != nil {
			return nil, err
		}
		certs <- cert
		return &cert.Permissions, nil
	}}
	config.AddHostKey(host)
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
						commands <- payload.Command
						_ = request.Reply(true, nil)
						_, _ = io.WriteString(channel, "[]")
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						_ = channel.Close()
						break
					}
				}
			}()
		}
	}()
	targets := filepath.Join(dir, "targets.json")
	raw, _ := json.Marshal([]string{"minter.example:22"})
	if err = os.WriteFile(targets, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	hosts := filepath.Join(dir, "known-hosts")
	if err = os.WriteFile(hosts, []byte(knownhosts.Line([]string{"minter.example"}, host.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	native := &NativeSSH{CADir: dir, TargetsFile: targets, KnownHostsFile: hosts, dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}}
	command := "sudo -n /usr/bin/pvesh get /access/users/dev-env@pve/token --output-format json"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, dispatched, err := native.Run(ctx, command)
	if err != nil || !dispatched || string(output) != "[]" {
		t.Fatalf("native SSH failed: %v", err)
	}
	cert := <-certs
	if cert.CriticalOptions["force-command"] != command || <-commands != command {
		t.Fatal("wrong remote command")
	}
	// Changed pinned host keys never reach exec and never count as dispatched.
	if err = os.WriteFile(hosts, []byte(knownhosts.Line([]string{"minter.example"}, testSSHKey(t).PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, dispatched, err = native.Run(ctx, command)
	if err == nil || dispatched {
		t.Fatal("changed host trust reached dispatch")
	}
	if err = os.WriteFile(hosts, []byte(knownhosts.Line([]string{"minter.example"}, host.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	native.BeforeDispatch = func(context.Context) error { return errors.New("lease lost " + credentialCanary) }
	_, dispatched, err = native.Run(ctx, command)
	if err == nil || dispatched || strings.Contains(err.Error(), credentialCanary) {
		t.Fatal("lease refusal leaked or dispatched")
	}
	select {
	case <-commands:
		t.Fatal("command ran after lease refusal")
	default:
	}
	cancelled, stop := context.WithCancel(context.Background())
	native.BeforeDispatch = func(context.Context) error { stop(); return nil }
	_, dispatched, err = native.Run(cancelled, command)
	if err == nil || dispatched {
		t.Fatal("leader cancellation before Start still dispatched")
	}
	select {
	case <-commands:
		t.Fatal("command ran after cancellation")
	default:
	}
}
func TestNativeSSHRejectsMissingAndMismatchedCA(t *testing.T) {
	dir, _ := writeTestSSHCA(t)
	native := &NativeSSH{CADir: dir, TargetsFile: filepath.Join(dir, "missing"), KnownHostsFile: filepath.Join(dir, "missing-hosts")}
	if native.Ready() == nil {
		t.Fatal("missing trust accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "public-key"), ssh.MarshalAuthorizedKey(testSSHKey(t).PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	if native.Ready() == nil {
		t.Fatal("mismatched CA accepted")
	}
	for _, command := range []string{"sudo -n pvesh create /access/users/root@pam/token/x", "sudo -n /usr/bin/pvesh get /access/users/dev-env@pve/token --output-format json; id", "sudo -n /usr/bin/pvesh delete /access/users/dev-env@pve/token/grant-x --output-format json"} {
		if validPVECommand(command) {
			t.Fatal("unscoped command accepted")
		}
	}
}
