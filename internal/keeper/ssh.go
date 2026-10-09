package keeper

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	MinterPrincipal          = "dev-env-keeper-proxmox-minter"
	DefaultSSHCAPath         = "/etc/dev-env-keeper/ssh-ca"
	DefaultSSHTargetsFile    = "/etc/dev-env-keeper/ssh/targets.json"
	DefaultSSHKnownHostsFile = "/etc/dev-env-keeper/ssh/known-hosts"
)

// NativeSSH signs an ephemeral certificate per operation. It invokes neither an
// ssh CLI nor a shell locally, and never exposes the CA or command output.
type NativeSSH struct {
	dial                               func(context.Context, string, string) (net.Conn, error)
	CADir, TargetsFile, KnownHostsFile string
	BeforeDispatch                     func(context.Context) error
	Now                                func() time.Time
}

// A per-request guard is carried in the operation context, rather than by
// mutating the shared SSH runner's Lease fence. Cleanup ignores this guard.
type sshCreateGuardKey struct{}

func withSSHCreateGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, sshCreateGuardKey{}, guard)
}

func checkSSHCreateGuard(ctx context.Context, command string) error {
	if strings.Fields(command)[3] != "create" {
		return nil
	}
	guard, _ := ctx.Value(sshCreateGuardKey{}).(func(context.Context) error)
	if guard == nil {
		return errors.New("SSH create request has no identity guard")
	}
	return guard(ctx)
}

type sshTrust struct {
	ca      ssh.Signer
	targets []string
	hostKey ssh.HostKeyCallback
}

func (s *NativeSSH) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("required SSH configuration is unavailable")
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return nil, errors.New("required SSH configuration is invalid")
	}
	return raw, nil
}
func (s *NativeSSH) trust() (*sshTrust, error) {
	ca, _, err := loadSSHCA(s.CADir)
	if err != nil {
		return nil, err
	}
	targetraw, err := readBoundedFile(s.TargetsFile, 8<<10)
	if err != nil {
		return nil, err
	}
	var targets []string
	dec := json.NewDecoder(bytes.NewReader(targetraw))
	if dec.Decode(&targets) != nil || dec.Decode(new(any)) != io.EOF || len(targets) == 0 || len(targets) > 8 {
		return nil, errors.New("SSH targets configuration is invalid")
	}
	seen := map[string]bool{}
	for _, target := range targets {
		host, port, e := net.SplitHostPort(target)
		if e != nil || port != "22" || host == "" || strings.ContainsAny(host, "*? \t\r\n/\\@") || seen[target] {
			return nil, errors.New("SSH targets must be distinct explicit host:22 addresses")
		}
		seen[target] = true
	}
	if _, err = readBoundedFile(s.KnownHostsFile, 64<<10); err != nil {
		return nil, err
	}
	callback, err := knownhosts.New(s.KnownHostsFile)
	if err != nil {
		return nil, errors.New("SSH host trust configuration is invalid")
	}
	return &sshTrust{ca: ca, targets: targets, hostKey: callback}, nil
}
func (s *NativeSSH) Ready() error { _, err := s.trust(); return err }

func (s *NativeSSH) signer(ca ssh.Signer, command string) (ssh.Signer, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, errors.New("could not create an ephemeral SSH key")
	}
	ephemeral, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, errors.New("could not create an ephemeral SSH signer")
	}
	now := s.now()
	serialBytes := make([]byte, 8)
	if _, err := rand.Read(serialBytes); err != nil {
		return nil, errors.New("could not create an SSH certificate serial")
	}
	serial := binary.BigEndian.Uint64(serialBytes)
	if serial == 0 {
		serial = 1
	}
	cert := &ssh.Certificate{
		Key: ephemeral.PublicKey(), Serial: serial, CertType: ssh.UserCert, KeyId: MinterPrincipal,
		ValidPrincipals: []string{MinterPrincipal}, ValidAfter: uint64(now.Add(-30 * time.Second).Unix()), ValidBefore: uint64(now.Add(2 * time.Minute).Unix()),
		Permissions: ssh.Permissions{CriticalOptions: map[string]string{"force-command": command}},
	}
	if err = cert.SignCert(rand.Reader, ca); err != nil {
		return nil, errors.New("could not sign the ephemeral SSH certificate")
	}
	return ssh.NewCertSigner(cert, ephemeral)
}

func (s *NativeSSH) Run(ctx context.Context, command string) ([]byte, bool, error) {
	if !validPVECommand(command) {
		return nil, false, errors.New("SSH command is outside the keeper minter contract")
	}
	if ctx.Err() != nil {
		return nil, false, errors.New("SSH operation was cancelled")
	}
	trust, err := s.trust()
	if err != nil {
		return nil, false, err
	}
	signer, err := s.signer(trust.ca, command)
	if err != nil {
		return nil, false, err
	}
	config := &ssh.ClientConfig{User: "dev-env", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: trust.hostKey, Timeout: 5 * time.Second}
	for _, target := range trust.targets {
		if ctx.Err() != nil {
			break
		}
		dial := s.dial
		if dial == nil {
			dial = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
		}
		conn, e := dial(ctx, "tcp", target)
		if e != nil {
			continue
		}
		// A deadline also bounds the SSH handshake, whose API has no context.
		deadline := time.Now().Add(credentialAttemptTimeout)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		if e = conn.SetDeadline(deadline); e != nil {
			_ = conn.Close()
			continue
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		clientConn, chans, reqs, e := ssh.NewClientConn(conn, target, config)
		if e != nil {
			stop()
			_ = conn.Close()
			continue
		}
		clientSSH := ssh.NewClient(clientConn, chans, reqs)
		session, e := clientSSH.NewSession()
		if e != nil {
			stop()
			_ = clientSSH.Close()
			continue
		}
		if ctx.Err() != nil || (s.BeforeDispatch != nil && s.BeforeDispatch(ctx) != nil) {
			stop()
			_ = session.Close()
			_ = clientSSH.Close()
			return nil, false, errors.New("SSH leadership fence refused dispatch")
		}
		if checkSSHCreateGuard(ctx, command) != nil {
			stop()
			_ = session.Close()
			_ = clientSSH.Close()
			return nil, false, errors.New("SSH create identity guard refused dispatch")
		}
		if ctx.Err() != nil {
			stop()
			_ = session.Close()
			_ = clientSSH.Close()
			return nil, false, errors.New("SSH operation was cancelled before dispatch")
		}
		output := &boundedOutput{limit: 64 << 10}
		session.Stdout = output
		session.Stderr = io.Discard
		// Start errors can lose an acknowledgement after the server accepted exec.
		e = session.Start(command)
		if e == nil {
			e = session.Wait()
		}
		stop()
		_ = session.Close()
		_ = clientSSH.Close()
		if e != nil || output.overflow || ctx.Err() != nil {
			return nil, true, errors.New("SSH command failed; output discarded")
		}
		return output.buf.Bytes(), true, nil
	}
	return nil, false, errors.New("no pinned SSH target accepted the connection")
}

type boundedOutput struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buf.Len() {
		b.overflow = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func validPVECommand(command string) bool {
	words := strings.Fields(command)
	if strings.Join(words, " ") != command || len(words) < 7 || words[0] != "sudo" || words[1] != "-n" || words[2] != "/usr/bin/pvesh" {
		return false
	}
	if command == "sudo -n /usr/bin/pvesh get /access/users/dev-env@pve/token --output-format json" {
		return true
	}
	if words[3] != "create" && words[3] != "delete" {
		return false
	}
	uid := strings.TrimPrefix(words[4], "/access/users/dev-env@pve/token/grant-")
	if !validUID(uid) || words[4] != "/access/users/dev-env@pve/token/grant-"+uid || words[5] != "--output-format" || words[6] != "json" {
		return false
	}
	if words[3] == "delete" {
		return len(words) == 7
	}
	if len(words) != 13 || words[7] != "--expire" || words[9] != "--privsep" || words[10] != "0" || words[11] != "--comment" {
		return false
	}
	expires, err := strconv.ParseInt(words[8], 10, 64)
	if err != nil || expires <= 0 {
		return false
	}
	pieces := strings.Split(words[12], ":")
	return len(pieces) == 3 && pieces[0] == "dev-env" && pieces[1] == uid && validUID(pieces[2])
}
