package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The server changes Kubernetes state while accepting the session channel,
// after authentication but before the client can call Start. No real SSH host
// or provider operation is involved.
func createGuardSSH(t *testing.T, f credentialFixture, change func() error) (*NativeSSH, *atomic.Int32, <-chan error) {
	t.Helper()
	dir, ca := writeTestSSHCA(t)
	host := testSSHKey(t)
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
	config.AddHostKey(host)
	var once sync.Once
	changed := make(chan error, 1)
	creates := &atomic.Int32{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					_ = conn.Close()
					return
				}
				defer func() { _ = server.Close() }()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					once.Do(func() { changed <- change() })
					channel, requests, err := incoming.Accept()
					if err != nil {
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
						output := []byte("[]")
						if strings.Contains(payload.Command, " create ") {
							creates.Add(1)
							output, _ = json.Marshal(mintedWire{Info: f.provider.info, Value: credentialCanary, ID: providerID(testGrantUID)})
						}
						_ = request.Reply(true, nil)
						_, _ = channel.Write(output)
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						_ = channel.Close()
						break
					}
				}
			}()
		}
	}()
	targets := filepath.Join(dir, "targets.json")
	if err := os.WriteFile(targets, []byte(`["minter.example:22"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts := filepath.Join(dir, "known-hosts")
	if err := os.WriteFile(hosts, []byte(knownhosts.Line([]string{"minter.example"}, host.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	native := &NativeSSH{CADir: dir, TargetsFile: targets, KnownHostsFile: hosts, BeforeDispatch: f.w.Fence, dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}}
	return native, creates, changed
}

func TestNativeSSHCreateRechecksLiveRequestAfterHandshake(t *testing.T) {
	changes := []struct {
		name   string
		change func(credentialFixture) error
	}{
		{"grant release", func(f credentialFixture) error {
			g := &v1alpha1.AccessGrant{}
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-agents", Name: "grant-example"}, g); err != nil {
				return err
			}
			g.Spec.Release = true
			return f.c.Update(context.Background(), g)
		}},
		{"job release", func(f credentialFixture) error {
			j := &v1alpha1.CredentialJob{}
			if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.job), j); err != nil {
				return err
			}
			j.Spec.Release = true
			return f.c.Update(context.Background(), j)
		}},
		{"session replacement", func(f credentialFixture) error {
			s := &v1alpha1.AgentSession{}
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-agents", Name: "session-example"}, s); err != nil {
				return err
			}
			if err := f.c.Delete(context.Background(), s); err != nil {
				return err
			}
			s.ResourceVersion = ""
			s.UID = "20000000-0000-4000-8000-000000000002"
			return f.c.Create(context.Background(), s)
		}},
		{"session deletion", func(f credentialFixture) error {
			s := &v1alpha1.AgentSession{}
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-agents", Name: "session-example"}, s); err != nil {
				return err
			}
			return f.c.Delete(context.Background(), s)
		}},
		{"fixed expiry", func(f credentialFixture) error { f.clock.Step(time.Hour); return nil }},
		{"job UID replacement", func(f credentialFixture) error {
			j := &v1alpha1.CredentialJob{}
			if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.job), j); err != nil {
				return err
			}
			j.Finalizers = nil
			if err := f.c.Update(context.Background(), j); err != nil {
				return err
			}
			if err := f.c.Delete(context.Background(), j); err != nil {
				return err
			}
			j.ResourceVersion = ""
			j.UID = "30000000-0000-4000-8000-000000000002"
			return f.c.Create(context.Background(), j)
		}},
		{"job pin replacement", func(f credentialFixture) error {
			g := &v1alpha1.AccessGrant{}
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-agents", Name: "grant-example"}, g); err != nil {
				return err
			}
			g.Annotations[v1alpha1.LabelPrefix+"credential-job-uid"] = "30000000-0000-4000-8000-000000000002"
			return f.c.Update(context.Background(), g)
		}},
		{"requester UID replacement", func(f credentialFixture) error {
			g := &v1alpha1.AccessGrant{}
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-agents", Name: "grant-example"}, g); err != nil {
				return err
			}
			g.Spec.Requester.SessionUID = types.UID("20000000-0000-4000-8000-000000000002")
			return f.c.Update(context.Background(), g)
		}},
		{"approval status loss", func(f credentialFixture) error {
			g := &v1alpha1.AccessGrant{}
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "dev-agents", Name: "grant-example"}, g); err != nil {
				return err
			}
			g.Status.Phase = v1alpha1.GrantReleased
			return f.c.Status().Update(context.Background(), g)
		}},
	}
	for _, test := range changes {
		t.Run(test.name, func(t *testing.T) {
			f := newCredentialFixture(t)
			native, creates, changed := createGuardSSH(t, f, func() error { return test.change(f) })
			f.w.Provider = &proxmoxProvider{SSH: native}
			if err := f.reconcile(t); err == nil {
				t.Fatal("changed live request was accepted")
			}
			select {
			case err := <-changed:
				if err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("state change did not occur during handshake")
			}
			if creates.Load() != 0 || f.installer.installs != 0 {
				t.Fatal("changed request produced credential side effects")
			}
		})
	}
}

func TestNativeSSHCreateGuardAllowsLiveRequestAndCleanup(t *testing.T) {
	f := newCredentialFixture(t)
	native, creates, changed := createGuardSSH(t, f, func() error { return nil })
	f.w.Provider = &proxmoxProvider{SSH: native}
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	if creates.Load() != 1 || f.installer.installs != 1 {
		t.Fatal("live request failed to create exactly once")
	}
	// A context carrying a failed create guard must still allow metadata reads
	// and cleanup operations after release/expiry/session loss.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = withSSHCreateGuard(ctx, func(context.Context) error { return errors.New("request no longer active") })
	e := credentialEntry{JobUID: testJobUID, Spec: f.job.Spec, Stage: journalCleanup}
	for _, command := range []string{"sudo -n /usr/bin/pvesh get /access/users/dev-env@pve/token --output-format json", pveCommand("delete", e)} {
		if _, dispatched, err := native.Run(ctx, command); err != nil || !dispatched {
			t.Fatalf("cleanup refused: %v", err)
		}
	}
	if creates.Load() != 1 {
		t.Fatal("cleanup dispatched another create")
	}
}
