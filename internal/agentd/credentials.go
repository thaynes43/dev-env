package agentd

import (
	"bytes"
	"crypto/sha256"
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
	credentialFile      = "credential.json"
	credentialOwnerFile = "credential-owner.json"
	maxCredentialBytes  = 24 << 10
)

var ErrNoCredential = errors.New("no live credential installed")
var credentialTempName = regexp.MustCompile(`^\.credential-[a-f0-9]{12}$`)
var credentialOwnerTempName = regexp.MustCompile(`^\.credential-owner-[a-f0-9]{12}$`)
var credentialHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

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

// The owner is committed before the first token-bearing temporary file. Its
// private fingerprint binds retries to the keeper's original provider value,
// even if that file is empty or only partly written when agentd is killed.
type credentialOwner struct {
	Version int `json:"version"`
	protocol.InstalledCredential
	PayloadHash string `json:"payloadHash"`
}

func ownerOf(r installedCredential) credentialOwner {
	b, _ := json.Marshal(r.Payload)
	return credentialOwner{Version: 1, InstalledCredential: r.InstalledCredential, PayloadHash: fmt.Sprintf("%x", sha256.Sum256(b))}
}

func validCredentialMetadata(m protocol.InstalledCredential, name string) bool {
	return protocol.ValidGrantName(m.Name) == nil && m.Name == name && validCredentialUID(m.GrantUID) && validCredentialUID(m.PodUID) && !m.Expires.IsZero() && !m.InstalledAt.IsZero() && !m.InstalledAt.After(m.Expires) && m.Credential == "proxmox"
}

func sameCredentialOwner(a, b credentialOwner) bool {
	return a.Name == b.Name && a.GrantUID == b.GrantUID && a.PodUID == b.PodUID && a.Credential == b.Credential && a.Expires.Equal(b.Expires) && a.PayloadHash == b.PayloadHash
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

type credentialState struct {
	record    installedCredential
	owner     credentialOwner
	committed bool
	// Only an interrupted marker write remains: no token bytes have been
	// written yet, so this metadata-only staging can be cleared safely.
	unowned bool
}

func readCredentialAt(dir *os.File, file, name string) (installedCredential, error) {
	var r installedCredential
	f, err := readPrivateAt(dir, file)
	if err != nil {
		return r, err
	}
	defer func() { _ = f.Close() }()
	if err := decodeCredential(f, &r); err != nil {
		return installedCredential{}, err
	}
	if !validCredentialMetadata(r.InstalledCredential, name) || checkCredentialPayload(r.Payload, r.GrantUID) != nil {
		return installedCredential{}, errors.New("invalid installed credential metadata")
	}
	return r, nil
}

func readCredentialOwner(dir *os.File, name string) (credentialOwner, error) {
	var owner credentialOwner
	f, err := readPrivateAt(dir, credentialOwnerFile)
	if err != nil {
		return owner, err
	}
	defer func() { _ = f.Close() }()
	if err := decodeCredential(f, &owner); err != nil {
		return owner, err
	}
	if owner.Version != 1 || !validCredentialMetadata(owner.InstalledCredential, name) || !credentialHash.MatchString(owner.PayloadHash) {
		return credentialOwner{}, errors.New("invalid credential ownership marker")
	}
	return owner, nil
}

// A complete token temp must agree with the durable marker. Empty/partial
// writes are bound by that marker instead of trusting unfinished content.
func checkPendingTemp(dir *os.File, file, name string, owner credentialOwner) error {
	f, err := readPrivateAt(dir, file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if err != nil || len(b) > maxCredentialBytes {
		return errors.New("invalid temporary credential file")
	}
	if !json.Valid(b) {
		return nil
	}
	var r installedCredential
	if err := decodeCredential(bytes.NewReader(b), &r); err != nil {
		return err
	}
	if !validCredentialMetadata(r.InstalledCredential, name) || checkCredentialPayload(r.Payload, r.GrantUID) != nil || !sameCredentialOwner(owner, ownerOf(r)) {
		return errors.New("temporary credential conflicts with its ownership marker")
	}
	return nil
}

func (c Credentials) readState(name string) (credentialState, error) {
	var state credentialState
	dir, err := c.openGrant(name)
	if err != nil {
		return state, err
	}
	defer func() { _ = dir.Close() }()
	layout, err := credentialEntries(dir)
	if err != nil {
		return state, err
	}
	if layout.final {
		r, err := readCredentialAt(dir, credentialFile, name)
		if err != nil {
			return state, err
		}
		state.record, state.owner, state.committed = r, ownerOf(r), true
		if layout.owner {
			owner, err := readCredentialOwner(dir, name)
			if err != nil {
				return state, err
			}
			if !sameCredentialOwner(owner, state.owner) {
				return state, errors.New("credential ownership marker conflicts with installed credential")
			}
		}
		return state, nil
	}
	if layout.owner {
		owner, err := readCredentialOwner(dir, name)
		if err != nil {
			return state, err
		}
		for _, file := range layout.tokenTemps {
			if err := checkPendingTemp(dir, file, name, owner); err != nil {
				return state, err
			}
		}
		state.owner = owner
		state.record.InstalledCredential = owner.InstalledCredential
		return state, nil
	}
	// Before ownership markers existed, a complete first-write temp could
	// be left behind. Recover only fully validated, consistent records.
	for i, file := range layout.tokenTemps {
		r, err := readCredentialAt(dir, file, name)
		if err != nil {
			return state, err
		}
		owner := ownerOf(r)
		if i > 0 && !sameCredentialOwner(owner, state.owner) {
			return state, errors.New("temporary credential ownership conflicts")
		}
		state.owner, state.record = owner, r
	}
	if len(layout.tokenTemps) > 0 {
		return state, nil
	}
	if len(layout.ownerTemps) > 0 {
		state.unowned = true
		return state, nil
	}
	return state, errors.New("credential directory has no ownership record")
}

type credentialLayout struct {
	final, owner           bool
	tokenTemps, ownerTemps []string
}

// Recognize only agentd's exact staging names and validate private paths
// before ignoring or removing them. Kube and unrelated entries are refused.
func credentialEntries(dir *os.File) (credentialLayout, error) {
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return credentialLayout{}, errors.New("could not inspect credential directory")
	}
	var layout credentialLayout
	for _, e := range entries {
		if e.Name() == credentialFile {
			layout.final = true
			continue
		}
		if e.Name() == credentialOwnerFile {
			layout.owner = true
			continue
		}
		if !credentialTempName.MatchString(e.Name()) && !credentialOwnerTempName.MatchString(e.Name()) {
			return layout, errors.New("credential directory contains unrelated files")
		}
		f, err := readPrivateAt(dir, e.Name())
		if err != nil {
			return layout, err
		}
		_ = f.Close()
		if credentialTempName.MatchString(e.Name()) {
			layout.tokenTemps = append(layout.tokenTemps, e.Name())
		} else {
			layout.ownerTemps = append(layout.ownerTemps, e.Name())
		}
	}
	return layout, nil
}

func (c Credentials) removeStaging(name string, removeOwner bool) error {
	dir, err := c.openGrant(name)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	layout, err := credentialEntries(dir)
	if err != nil {
		return err
	}
	files := append(layout.tokenTemps, layout.ownerTemps...)
	if removeOwner && layout.owner {
		files = append(files, credentialOwnerFile)
	}
	for _, tmp := range files {
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
			state, err := c.readState(s.Name)
			if err != nil {
				return err
			}
			if !state.unowned && state.record.PodUID != c.PodUID {
				return errors.New("installed credential belongs to another pod")
			}
			if state.committed && state.record.GrantUID == s.GrantUID && (!state.record.Expires.Equal(s.Expires) || state.record.Payload != p) {
				return errors.New("an existing provider credential cannot change its value or expiry")
			}
			if !state.committed && !state.unowned && !sameCredentialOwner(state.owner, ownerOf(r)) {
				return errors.New("pending credential ownership conflicts with the install request")
			}
			if err := c.removeStaging(s.Name, state.committed); err != nil {
				return err
			}
			if state.committed {
				// The valid final record continues to own an interrupted
				// replacement. Its value stays usable until atomic rename.
				return writeCredentialAt(dir, append(b, '\n'))
			}
		}
		owner, err := json.Marshal(ownerOf(r))
		if err != nil {
			return errors.New("could not encode credential ownership marker")
		}
		// Commit ownership before opening any token-bearing temporary file.
		if err := writePrivateCredentialAt(dir, credentialOwnerFile, ".credential-owner-", append(owner, '\n')); err != nil {
			return err
		}
		if err := writeCredentialAt(dir, append(b, '\n')); err != nil {
			if created {
				_ = os.Remove(path)
			}
			return err
		}
		return c.removeStaging(s.Name, true)
	})
}

// writeCredentialAt uses an opened private directory, so replacing a path
// with a symlink cannot redirect the token to another directory.
func writeCredentialAt(dir *os.File, b []byte) error {
	return writePrivateCredentialAt(dir, credentialFile, ".credential-", b)
}

func writePrivateCredentialAt(dir *os.File, target, prefix string, b []byte) error {
	name := prefix + newBootID()
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
	if err := syscall.Renameat(int(dir.Fd()), name, int(dir.Fd()), target); err != nil {
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
		if present, err := credentialDirectoryPresent(filepath.Join(c.Grants.Dir, e.Name())); present || err != nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// A marker or recognized temporary file also identifies a credential
// directory. Kube operations must preserve it before the first final rename.
func credentialDirectoryPresent(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("could not inspect grant directory")
	}
	for _, e := range entries {
		if e.Name() == credentialFile || e.Name() == credentialOwnerFile || credentialTempName.MatchString(e.Name()) || credentialOwnerTempName.MatchString(e.Name()) {
			return true, nil
		}
	}
	return false, nil
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
			state, err := c.readState(name)
			if err != nil {
				return err
			}
			if state.unowned {
				continue
			}
			r := state.record
			if r.PodUID != c.PodUID {
				return errors.New("installed credential belongs to another pod")
			}
			if state.committed && c.Grants.now().Before(r.Expires) {
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
			state, err := c.readState(name)
			if err != nil {
				return err
			}
			if state.unowned {
				continue
			}
			r := state.record
			if r.PodUID != c.PodUID {
				return errors.New("installed credential belongs to another pod")
			}
			if state.committed && c.Grants.now().Before(r.Expires) {
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
		state, err := c.readState(name)
		if err != nil {
			return err
		}
		if state.unowned {
			return c.removeOne(name)
		}
		r := state.record
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
	layout, err := credentialEntries(dir)
	if err != nil {
		return err
	}
	files := append(layout.tokenTemps, layout.ownerTemps...)
	if layout.final {
		files = append(files, credentialFile)
	}
	if layout.owner {
		files = append(files, credentialOwnerFile)
	}
	for _, tmp := range files {
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
			state, err := c.readState(name)
			if err != nil {
				failed = err
				continue
			}
			if state.unowned {
				if err := c.removeOne(name); err != nil {
					failed = err
				}
				continue
			}
			r := state.record
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
