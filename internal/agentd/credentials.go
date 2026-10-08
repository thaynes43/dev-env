package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	credentialFile     = "credential.json"
	maxCredentialBytes = 24 << 10
)

var ErrNoCredential = errors.New("no live credential installed")
var credentialTempName = regexp.MustCompile(`^\.credential-[a-f0-9]{12}$`)

// CredentialSpec is the public metadata checked before reading private stdin.
type CredentialSpec struct {
	Name, GrantUID, PodUID string
	Expires                time.Time
}

func validCredentialUID(uid string) bool {
	return len(uid) <= 63 && dnsLabel.MatchString(uid)
}

func (s CredentialSpec) Check(now time.Time) error {
	if protocol.ValidGrantName(s.Name) != nil || !validCredentialUID(s.GrantUID) || !validCredentialUID(s.PodUID) {
		return errors.New("invalid credential grant or pod identity")
	}
	if s.Expires.IsZero() || !now.Before(s.Expires) {
		return errors.New("credential grant expiry is missing or has passed")
	}
	return nil
}

// Credentials shares the mounted grants root and lock with kube grants. Each
// credential has one private atomic file containing metadata and material.
// A read opens directories and files without following symlinks and checks
// their owner, type, permissions, version, identities and expiry.
type Credentials struct {
	Grants Grants
	PodUID string
}

func CredentialsFromEnv(getenv func(string) string) Credentials {
	return Credentials{Grants: GrantsFromEnv(getenv), PodUID: getenv(protocol.PodUIDEnv)}
}

type installedCredential struct {
	protocol.InstalledCredential
	Payload protocol.CredentialPayload `json:"payload"`
}

func (installedCredential) String() string   { return "installedCredential{redacted}" }
func (installedCredential) GoString() string { return "installedCredential{redacted}" }
func (installedCredential) LogValue() slog.Value {
	return slog.StringValue("installedCredential{redacted}")
}

// decodeCredential checks a complete bounded JSON object. JSON parse errors
// can quote input, so all errors returned here are fixed strings.
func decodeCredential(r io.Reader, out any) error {
	b, err := io.ReadAll(io.LimitReader(r, maxCredentialBytes+1))
	if err != nil || len(b) == 0 || len(b) > maxCredentialBytes {
		return errors.New("credential document is unreadable, empty, or too large")
	}
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(b))); err != nil {
		return errors.New("invalid credential document")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid credential document")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("invalid credential document")
	}
	return nil
}

// Duplicate keys are ambiguous, even when encoding/json would accept the
// final value. Reject them in metadata and private payloads alike.
func uniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("invalid JSON")
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("invalid JSON")
			}
			seen[name] = true
		}
		if err := uniqueJSON(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func checkCredentialPayload(p protocol.CredentialPayload, grantUID string) error {
	if p.Version != 1 || p.Credential != "proxmox" || p.TokenID != "dev-env@pve!grant-"+grantUID {
		return errors.New("credential version, type, or provider identity is invalid")
	}
	if _, err := ReadToken(strings.NewReader(p.TokenSecret)); err != nil || strings.ContainsAny(p.TokenSecret, "\r\n") {
		return errors.New("invalid provider credential material")
	}
	return nil
}

func ReadCredentialPayload(r io.Reader, grantUID string) (protocol.CredentialPayload, error) {
	var p protocol.CredentialPayload
	if err := decodeCredential(r, &p); err != nil {
		return p, err
	}
	if err := checkCredentialPayload(p, grantUID); err != nil {
		return protocol.CredentialPayload{}, err
	}
	return p, nil
}

func checkPrivate(fi fs.FileInfo, dir bool) error {
	mode := fs.FileMode(0o600)
	if dir {
		mode = 0o700
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || fi.IsDir() != dir || (!dir && !fi.Mode().IsRegular()) || fi.Mode().Perm() != mode || fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		return errors.New("credential path has an invalid type, owner, or permissions")
	}
	return nil
}

func (c Credentials) openGrant(name string) (*os.File, error) {
	fd, err := syscall.Open(filepath.Join(c.Grants.Dir, name), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("credential directory is missing or unsafe")
	}
	f := os.NewFile(uintptr(fd), "credential directory")
	fi, err := f.Stat()
	if err == nil {
		err = checkPrivate(fi, true)
	}
	if err != nil {
		_ = f.Close()
		return nil, errors.New("credential directory is unsafe")
	}
	return f, nil
}

func readPrivateAt(dir *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("credential file is missing or unsafe")
	}
	f := os.NewFile(uintptr(fd), "private credential")
	fi, err := f.Stat()
	if err == nil {
		err = checkPrivate(fi, false)
	}
	if err != nil {
		_ = f.Close()
		return nil, errors.New("credential file is unsafe")
	}
	return f, nil
}

func (c Credentials) readOne(name string) (installedCredential, error) {
	var r installedCredential
	dir, err := c.openGrant(name)
	if err != nil {
		return r, err
	}
	defer func() { _ = dir.Close() }()
	if _, err := credentialEntries(dir); err != nil {
		return r, err
	}
	f, err := readPrivateAt(dir, credentialFile)
	if err != nil {
		return r, err
	}
	defer func() { _ = f.Close() }()
	if err := decodeCredential(f, &r); err != nil {
		return installedCredential{}, err
	}
	s := CredentialSpec{Name: r.Name, GrantUID: r.GrantUID, PodUID: r.PodUID, Expires: r.Expires}
	// Expiry is checked by callers, so expired credentials can be removed.
	if protocol.ValidGrantName(s.Name) != nil || s.Name != name || !validCredentialUID(s.GrantUID) || !validCredentialUID(s.PodUID) || s.Expires.IsZero() || r.InstalledAt.IsZero() || r.InstalledAt.After(r.Expires) || r.Credential != "proxmox" || checkCredentialPayload(r.Payload, r.GrantUID) != nil {
		return installedCredential{}, errors.New("invalid installed credential metadata")
	}
	return r, nil
}

// Interrupted replacements can leave a private temporary file beside the
// previous valid document. Recognize only agentd's exact temporary names,
// and validate their type, owner and mode before ignoring or removing them.
func credentialEntries(dir *os.File) ([]string, error) {
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, errors.New("could not inspect credential directory")
	}
	var temps []string
	for _, e := range entries {
		if e.Name() == credentialFile {
			continue
		}
		if !credentialTempName.MatchString(e.Name()) {
			return nil, errors.New("credential directory contains unrelated files")
		}
		f, err := readPrivateAt(dir, e.Name())
		if err != nil {
			return nil, err
		}
		_ = f.Close()
		temps = append(temps, e.Name())
	}
	return temps, nil
}

func (c Credentials) removeTemps(name string) error {
	dir, err := c.openGrant(name)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	temps, err := credentialEntries(dir)
	if err != nil {
		return err
	}
	for _, tmp := range temps {
		if err := syscall.Unlinkat(int(dir.Fd()), tmp); err != nil {
			return errors.New("could not remove temporary private credential")
		}
	}
	return nil
}

// Install atomically replaces the metadata and credential together. A
// malformed existing entry is refused, and a kube entry is never replaced.
func (c Credentials) Install(s CredentialSpec, p protocol.CredentialPayload) error {
	now := c.Grants.now()
	if err := s.Check(now); err != nil {
		return err
	}
	if c.PodUID == "" || s.PodUID != c.PodUID {
		return errors.New("credential target pod changed")
	}
	if err := checkCredentialPayload(p, s.GrantUID); err != nil {
		return err
	}
	r := installedCredential{InstalledCredential: protocol.InstalledCredential{
		Name: s.Name, GrantUID: s.GrantUID, PodUID: s.PodUID, Credential: "proxmox",
		Expires: s.Expires.UTC(), InstalledAt: now.UTC(),
	}, Payload: p}
	b, err := json.Marshal(r)
	if err != nil || len(b)+1 > maxCredentialBytes {
		return errors.New("private credential document cannot be encoded within its size limit")
	}
	return c.Grants.locked(func() error {
		if err := s.Check(c.Grants.now()); err != nil {
			return err
		}
		path := filepath.Join(c.Grants.Dir, s.Name)
		created := false
		if err := os.Mkdir(path, 0o700); err == nil {
			created = true
			// Memory volumes and test roots may inherit setgid. Clear it
			// only on the new directory, without repairing unsafe entries.
			if err := os.Chmod(path, 0o700); err != nil {
				return errors.New("could not make credential directory private")
			}
		} else if !errors.Is(err, fs.ErrExist) {
			return errors.New("could not create credential directory")
		}
		dir, err := c.openGrant(s.Name)
		if err != nil {
			return err
		}
		defer func() { _ = dir.Close() }()
		entries, err := dir.ReadDir(-1)
		if err != nil {
			return errors.New("could not inspect credential directory")
		}
		if len(entries) != 0 {
			old, err := c.readOne(s.Name)
			if err != nil {
				return err
			}
			if old.PodUID != c.PodUID {
				return errors.New("installed credential belongs to another pod")
			}
			if old.GrantUID == s.GrantUID && (!old.Expires.Equal(s.Expires) || old.Payload != p) {
				return errors.New("an existing provider credential cannot change its value or expiry")
			}
			if err := c.removeTemps(s.Name); err != nil {
				return err
			}
		}
		if err := writeCredentialAt(dir, append(b, '\n')); err != nil {
			if created {
				_ = os.Remove(path)
			}
			return err
		}
		return nil
	})
}

// writeCredentialAt uses an opened private directory, so replacing a path
// with a symlink cannot redirect the token to another directory.
func writeCredentialAt(dir *os.File, b []byte) error {
	name := ".credential-" + newBootID()
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return errors.New("could not create private credential file")
	}
	f := os.NewFile(uintptr(fd), "private credential")
	defer func() { _ = syscall.Unlinkat(int(dir.Fd()), name) }()
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("could not write private credential file")
	}
	if err := syscall.Renameat(int(dir.Fd()), name, int(dir.Fd()), credentialFile); err != nil {
		return errors.New("could not replace private credential file")
	}
	return nil
}

func (c Credentials) names() ([]string, error) {
	entries, err := os.ReadDir(c.Grants.Dir)
	if err != nil {
		return nil, errors.New("could not inspect credential store")
	}
	var names []string
	for _, e := range entries {
		if protocol.ValidGrantName(e.Name()) != nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(c.Grants.Dir, e.Name(), credentialFile)); err == nil || !errors.Is(err, fs.ErrNotExist) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// List never returns provider material. A wrong-pod or malformed entry fails
// closed rather than being silently treated as an available credential.
func (c Credentials) List() ([]protocol.InstalledCredential, error) {
	var out []protocol.InstalledCredential
	err := c.Grants.locked(func() error {
		names, err := c.names()
		if err != nil {
			return err
		}
		for _, name := range names {
			r, err := c.readOne(name)
			if err != nil {
				return err
			}
			if r.PodUID != c.PodUID {
				return errors.New("installed credential belongs to another pod")
			}
			if c.Grants.now().Before(r.Expires) {
				out = append(out, r.InstalledCredential)
			}
		}
		return nil
	})
	return out, err
}

// Use selects a live provider credential at call time. Selection is stable
// by grant name; callers never cache the returned material across calls.
func (c Credentials) Use(credential string) (protocol.CredentialPayload, error) {
	var p protocol.CredentialPayload
	if credential != "proxmox" {
		return p, errors.New("unsupported credential type")
	}
	err := c.Grants.locked(func() error {
		names, err := c.names()
		if err != nil {
			return err
		}
		for _, name := range names {
			r, err := c.readOne(name)
			if err != nil {
				return err
			}
			if r.PodUID != c.PodUID {
				return errors.New("installed credential belongs to another pod")
			}
			if c.Grants.now().Before(r.Expires) {
				p = r.Payload
				return nil
			}
		}
		return ErrNoCredential
	})
	return p, err
}

// Remove is idempotent and fenced by both UIDs. Stale cleanup leaves a new
// same-named grant alone. It does not delete a kube grant or untyped entry.
func (c Credentials) Remove(name, grantUID, podUID string) error {
	if protocol.ValidGrantName(name) != nil || !validCredentialUID(grantUID) || !validCredentialUID(podUID) || podUID != c.PodUID {
		return errors.New("invalid credential cleanup identity")
	}
	return c.Grants.locked(func() error {
		path := filepath.Join(c.Grants.Dir, name)
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		r, err := c.readOne(name)
		if err != nil {
			return err
		}
		if r.GrantUID != grantUID || r.PodUID != podUID {
			return nil
		}
		return c.removeOne(name)
	})
}

func (c Credentials) removeOne(name string) error {
	// Only the known file is removed. Unexpected files prevent directory
	// removal instead of being recursively deleted with private material.
	dir, err := c.openGrant(name)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	temps, err := credentialEntries(dir)
	if err != nil {
		return err
	}
	if err := syscall.Unlinkat(int(dir.Fd()), credentialFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("could not remove private credential")
	}
	for _, tmp := range temps {
		if err := syscall.Unlinkat(int(dir.Fd()), tmp); err != nil {
			return errors.New("could not remove temporary private credential")
		}
	}
	if err := os.Remove(filepath.Join(c.Grants.Dir, name)); err != nil {
		return errors.New("could not remove credential directory")
	}
	return nil
}

// Expire removes provider files at their own expiry, even if the keeper is
// unavailable. The provider's fixed expiry is the authoritative backstop.
func (c Credentials) Expire() error {
	return c.Grants.locked(func() error {
		names, err := c.names()
		if err != nil {
			return err
		}
		var failed error
		for _, name := range names {
			r, err := c.readOne(name)
			if err != nil {
				failed = err
				continue
			}
			if r.PodUID != c.PodUID {
				failed = errors.New("installed credential belongs to another pod")
				continue
			}
			if !c.Grants.now().Before(r.Expires) {
				if err := c.removeOne(name); err != nil {
					failed = err
				}
			}
		}
		return failed
	})
}

// CredentialEnvironment is passed only to a child process, never printed.
func CredentialEnvironment(p protocol.CredentialPayload) []string {
	return []string{"PVE_OPERATOR_TOKEN_ID=" + p.TokenID, "PVE_OPERATOR_TOKEN_SECRET=" + p.TokenSecret, "PVE_GRANT_ACTIVE=1"}
}

// RedactCredentialOutput covers an API or child echo of the chosen secret.
func RedactCredentialOutput(b []byte, p protocol.CredentialPayload) []byte {
	return bytes.ReplaceAll(b, []byte(p.TokenSecret), []byte("[redacted]"))
}

// Keep formatting of secret-bearing state from becoming a logging path.
var _ fmt.Stringer = installedCredential{}
