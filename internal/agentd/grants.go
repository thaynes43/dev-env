package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Kube grants in the session pod (D-63): `agentd ctl grant-install`,
// `grant-remove`, `grant-use` and `grant-list`. The broker installs a grant's
// token by exec, on stdin. Each grant gets a directory in the pod's
// memory-backed grants volume:
//
//	<dir>/<grant>/token       the token, 0600
//	<dir>/<grant>/grant.json  name, role, namespaces, expires; no token
//	<dir>/kubeconfig          KUBECONFIG: a context per grant and "default"
//
// The kubeconfig exists only while a grant is installed. Without it kubectl and
// client-go fall back to the pod's in-cluster config, so a pod with no grant
// behaves as one built before D-63. Every write runs under a lock on
// <dir>/.lock, drops expired grants, and replaces the kubeconfig by rename.
// Nothing here prints or logs a token.

// The pod's own identity, as client-go's in-cluster config reads it.
const (
	ServiceAccountTokenFile     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	ServiceAccountCAFile        = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	ServiceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// Names inside the kubeconfig.
const (
	clusterName = "in-cluster"
	agentUser   = "agent"
)

const (
	grantTokenFile = "token"
	grantFile      = "grant.json"
	grantsLock     = ".lock"
	// maxTokenBytes caps what grant-install reads from stdin. A
	// ServiceAccount token is about a kilobyte.
	maxTokenBytes = 16 << 10
)

// ErrNoGrantsDir means the pod has no grants volume: it was built before D-63.
// `agentd ctl grant-*` exits with protocol.ExitNoGrantsDir on it.
var ErrNoGrantsDir = errors.New("no grants directory")

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Grants keeps the kube grants installed in the pod.
type Grants struct {
	// Dir is the grants volume: $DEV_ENV_GRANTS_DIR, else protocol.GrantsDir.
	// agentd never creates it, so a token is never written to a disk the
	// operator did not give the pod in memory.
	Dir string
	// Server is the API server's URL, from the same variables client-go's
	// in-cluster config reads (KUBERNETES_SERVICE_HOST and _PORT). Session
	// pods resolve no search-expanded names (DESIGN-001 3.6), so the
	// kubeconfig names the address, not kubernetes.default.svc. Empty when
	// those are unset: then a write that needs the kubeconfig fails.
	Server string
	// Namespace is the pod's namespace, used by the baseline and cluster-wide
	// contexts; loading a kubeconfig otherwise changes kubectl to default.
	Namespace string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// GrantsFromEnv returns the pod's grants as the environment describes them.
func GrantsFromEnv(getenv func(string) string) Grants {
	g := Grants{Dir: getenv(protocol.GrantsDirEnv), Namespace: getenv(protocol.PodNamespaceEnv)}
	if g.Namespace == "" {
		b, _ := os.ReadFile(ServiceAccountNamespaceFile)
		g.Namespace = strings.TrimSpace(string(b))
	}
	if g.Dir == "" {
		g.Dir = protocol.GrantsDir
	}
	host, port := getenv("KUBERNETES_SERVICE_HOST"), getenv("KUBERNETES_SERVICE_PORT")
	if host != "" && port != "" {
		g.Server = "https://" + net.JoinHostPort(host, port)
	}
	return g
}

func (g Grants) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// GrantSpec is what grant-install is told about a grant.
type GrantSpec struct {
	Name       string
	Role       string
	Namespaces []string
	Expires    time.Time
}

// Check refuses a spec before anything is read or written.
func (s GrantSpec) Check(now time.Time) error {
	if err := protocol.ValidGrantName(s.Name); err != nil {
		return err
	}
	if !dnsLabel.MatchString(s.Role) || len(s.Role) > 253 {
		return fmt.Errorf("--role %q is not a role name", s.Role)
	}
	seen := map[string]bool{}
	for _, ns := range s.Namespaces {
		if !dnsLabel.MatchString(ns) || len(ns) > 63 {
			return fmt.Errorf("--namespace %q is not a namespace", ns)
		}
		if seen[ns] {
			return fmt.Errorf("--namespace %s is given twice", ns)
		}
		seen[ns] = true
	}
	if s.Expires.IsZero() {
		return errors.New("--expires is required")
	}
	if !now.Before(s.Expires) {
		return fmt.Errorf("the grant expired at %s", s.Expires.UTC().Format(time.RFC3339))
	}
	return nil
}

// ReadToken reads a token from r: one line of printable characters with no
// spaces, at most 16 KiB, and one trailing newline allowed. Its errors never
// hold the token.
func ReadToken(r io.Reader) ([]byte, error) {
	// Two bytes allow CRLF after a maximum-sized token; one more detects
	// additional input rather than accepting a truncated stdin stream.
	b, err := io.ReadAll(io.LimitReader(r, maxTokenBytes+3))
	if err != nil {
		return nil, errors.New("could not read the token from stdin")
	}
	if bytes.HasSuffix(b, []byte("\n")) {
		b = bytes.TrimSuffix(b, []byte("\n"))
		b = bytes.TrimSuffix(b, []byte("\r"))
	}
	switch {
	case len(b) == 0:
		return nil, errors.New("no token on stdin")
	case len(b) > maxTokenBytes:
		return nil, fmt.Errorf("the token on stdin is longer than %d bytes", maxTokenBytes)
	}
	for _, c := range b {
		if c <= ' ' || c >= 0x7f {
			return nil, errors.New("the token on stdin is more than one line, or holds a space or a control character")
		}
	}
	return b, nil
}

// Install writes the grant's token and grant.json and rewrites the kubeconfig
// with a context for it. The current context stays as it was; a new kubeconfig
// starts at "default". Installing a grant again replaces its token.
func (g Grants) Install(spec GrantSpec, token []byte) error {
	now := g.now()
	if err := spec.Check(now); err != nil {
		return err
	}
	var err error
	token, err = ReadToken(bytes.NewReader(token))
	if err != nil {
		return err
	}
	if g.Server == "" {
		return errors.New("KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT are not set, so the kubeconfig cannot name the API server")
	}
	if !dnsLabel.MatchString(g.Namespace) || len(g.Namespace) > 63 {
		return errors.New("the pod namespace is not set or is invalid")
	}
	return g.locked(func() error {
		dir := filepath.Join(g.Dir, spec.Name)
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return errors.New("the grant path is not a directory, or is a symlink")
		}
		if _, err := os.Lstat(filepath.Join(dir, credentialFile)); err == nil {
			return errors.New("the grant path already holds a provider credential")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return errors.New("the grant path is unsafe")
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
		meta, err := json.MarshalIndent(protocol.InstalledGrant{
			Name: spec.Name, Role: spec.Role, Namespaces: spec.Namespaces,
			Expires: spec.Expires.UTC(), InstalledAt: now.UTC().Truncate(time.Second),
		}, "", "  ")
		if err != nil {
			return err
		}
		// The token first, grant.json last: a directory without grant.json
		// is not a grant, and the next write removes it.
		if err := writePrivate(filepath.Join(dir, grantTokenFile), token); err != nil {
			return fmt.Errorf("write the token: %w", err)
		}
		if err := writePrivate(filepath.Join(dir, grantFile), append(meta, '\n')); err != nil {
			return err
		}
		return g.rewrite(now, "")
	})
}

// Remove deletes the grant's directory and rewrites the kubeconfig. Removing a
// grant that is not installed is not an error. When the current context was
// the grant's, it becomes "default"; with no grant left the kubeconfig goes.
func (g Grants) Remove(name string) error {
	if err := protocol.ValidGrantName(name); err != nil {
		return err
	}
	return g.locked(func() error {
		if _, err := os.Lstat(filepath.Join(g.Dir, name, credentialFile)); err == nil {
			return errors.New("the grant path holds a provider credential")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return errors.New("the grant path is unsafe")
		}
		if err := os.RemoveAll(filepath.Join(g.Dir, name)); err != nil {
			return err
		}
		return g.rewrite(g.now(), "")
	})
}

// Use makes the grant's context, or "default", the kubeconfig's current
// context.
func (g Grants) Use(name string) error {
	if name != protocol.DefaultContext {
		if err := protocol.ValidGrantName(name); err != nil {
			return fmt.Errorf("%w, nor %q", err, protocol.DefaultContext)
		}
	}
	return g.locked(func() error {
		now := g.now()
		if name != protocol.DefaultContext {
			list, err := g.read(now)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(list, func(x protocol.InstalledGrant) bool { return x.Name == name && !x.Expired }) {
				return fmt.Errorf("grant %s is not installed in this pod, or it has expired", name)
			}
		}
		return g.rewrite(now, name)
	})
}

// List returns the installed grants by name, expired ones included until the
// next write removes them, and marks the current context's.
func (g Grants) List() ([]protocol.InstalledGrant, error) {
	var list []protocol.InstalledGrant
	err := g.locked(func() error {
		var err error
		list, err = g.read(g.now())
		if err != nil {
			return err
		}
		cur := g.currentContext()
		for i := range list {
			list[i].Current = list[i].Name == cur
		}
		return nil
	})
	return list, err
}

// KubeconfigPath is the kubeconfig's path, which KUBECONFIG names.
func (g Grants) KubeconfigPath() string { return filepath.Join(g.Dir, protocol.KubeconfigName) }

func (g Grants) checkDir() error {
	fi, err := os.Lstat(g.Dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %s is missing; this pod was built before kube grants (D-63), and a new pod of the session takes them", ErrNoGrantsDir, g.Dir)
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%w: %s is not a directory", ErrNoGrantsDir, g.Dir)
	}
	return nil
}

// locked runs f under an exclusive lock on the grants directory, so an
// install, a remove and a use never interleave.
func (g Grants) locked(f func() error) error {
	if err := g.checkDir(); err != nil {
		return err
	}
	fd, err := syscall.Open(filepath.Join(g.Dir, grantsLock), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return errors.New("the grants lock is missing or unsafe")
	}
	lf := os.NewFile(uintptr(fd), "grants lock")
	defer func() { _ = lf.Close() }()
	fi, err := lf.Stat()
	if err != nil || checkPrivate(fi, false) != nil {
		return errors.New("the grants lock has an invalid type, owner, or permissions")
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", g.Dir, err)
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()
	return f()
}

// read returns the grants in the directory, by name. A directory with a grant
// name but no readable grant.json or no token is skipped; anything else in the
// directory is not agentd's and is left alone.
func (g Grants) read(now time.Time) ([]protocol.InstalledGrant, error) {
	entries, err := os.ReadDir(g.Dir)
	if err != nil {
		return nil, err
	}
	var list []protocol.InstalledGrant
	for _, e := range entries {
		if !e.IsDir() || protocol.ValidGrantName(e.Name()) != nil {
			continue
		}
		// Provider credentials share this root but never belong in a
		// kubeconfig. Leave their typed entries to the credential store.
		if _, err := os.Lstat(filepath.Join(g.Dir, e.Name(), credentialFile)); err == nil {
			continue
		}
		ig, ok := g.readOne(e.Name())
		if !ok {
			continue
		}
		ig.Expired = !now.Before(ig.Expires)
		list = append(list, ig)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

func (g Grants) readOne(name string) (protocol.InstalledGrant, bool) {
	var ig protocol.InstalledGrant
	dir := filepath.Join(g.Dir, name)
	b, err := os.ReadFile(filepath.Join(dir, grantFile))
	if err != nil || json.Unmarshal(b, &ig) != nil || ig.Name != name || ig.Expires.IsZero() {
		return ig, false
	}
	if fi, err := os.Stat(filepath.Join(dir, grantTokenFile)); err != nil || !fi.Mode().IsRegular() {
		return ig, false
	}
	ig.Expired, ig.Current = false, false
	return ig, true
}

// rewrite drops expired and broken grants, then writes the kubeconfig: a
// context per grant, "default" for the pod's own identity, and current = use
// when it is set, else what it was if that context is still there, else
// "default". With no grant left it removes the kubeconfig.
func (g Grants) rewrite(now time.Time, use string) error {
	entries, err := os.ReadDir(g.Dir)
	if err != nil {
		return err
	}
	var live []protocol.InstalledGrant
	for _, e := range entries {
		if !e.IsDir() || protocol.ValidGrantName(e.Name()) != nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(g.Dir, e.Name(), credentialFile)); err == nil {
			continue
		}
		ig, ok := g.readOne(e.Name())
		if ok && now.Before(ig.Expires) {
			live = append(live, ig)
			continue
		}
		if err := os.RemoveAll(filepath.Join(g.Dir, e.Name())); err != nil {
			return err
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Name < live[j].Name })

	path := g.KubeconfigPath()
	if len(live) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if g.Server == "" {
		return errors.New("KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT are not set, so the kubeconfig cannot name the API server")
	}
	cur := use
	if cur == "" {
		cur = g.currentContext()
	}
	if cur != protocol.DefaultContext && !slices.ContainsFunc(live, func(x protocol.InstalledGrant) bool { return x.Name == cur }) {
		cur = protocol.DefaultContext
	}
	b, err := json.MarshalIndent(g.kubeconfig(live, cur), "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(b, '\n'))
}

// currentContext is the kubeconfig's current context, or "" when there is no
// kubeconfig or it cannot be read.
func (g Grants) currentContext() string {
	b, err := os.ReadFile(g.KubeconfigPath())
	if err != nil {
		return ""
	}
	var kc struct {
		CurrentContext string `json:"current-context"`
	}
	if json.Unmarshal(b, &kc) != nil {
		return ""
	}
	return kc.CurrentContext
}

// The kubeconfig, in JSON, which kubectl reads like YAML.
type kubeconfig struct {
	APIVersion     string         `json:"apiVersion"`
	Kind           string         `json:"kind"`
	Clusters       []namedCluster `json:"clusters"`
	Users          []namedUser    `json:"users"`
	Contexts       []namedContext `json:"contexts"`
	CurrentContext string         `json:"current-context"`
}

type namedCluster struct {
	Name    string      `json:"name"`
	Cluster kubeCluster `json:"cluster"`
}

type kubeCluster struct {
	Server               string `json:"server"`
	CertificateAuthority string `json:"certificate-authority"`
}

type namedUser struct {
	Name string   `json:"name"`
	User kubeUser `json:"user"`
}

type kubeUser struct {
	TokenFile string `json:"tokenFile"`
}

type namedContext struct {
	Name    string      `json:"name"`
	Context kubeContext `json:"context"`
}

type kubeContext struct {
	Cluster   string `json:"cluster"`
	User      string `json:"user"`
	Namespace string `json:"namespace,omitempty"`
}

// kubeconfig builds the file. The "default" context preserves the pod's
// namespace; a grant's context starts in its first namespace, and a
// cluster-wide grant's in the pod's.
func (g Grants) kubeconfig(live []protocol.InstalledGrant, current string) kubeconfig {
	kc := kubeconfig{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: []namedCluster{{Name: clusterName, Cluster: kubeCluster{
			Server: g.Server, CertificateAuthority: ServiceAccountCAFile,
		}}},
		Users:          []namedUser{{Name: agentUser, User: kubeUser{TokenFile: ServiceAccountTokenFile}}},
		Contexts:       []namedContext{{Name: protocol.DefaultContext, Context: kubeContext{Cluster: clusterName, User: agentUser, Namespace: g.Namespace}}},
		CurrentContext: current,
	}
	for _, ig := range live {
		kc.Users = append(kc.Users, namedUser{Name: ig.Name, User: kubeUser{TokenFile: filepath.Join(g.Dir, ig.Name, grantTokenFile)}})
		ctx := kubeContext{Cluster: clusterName, User: ig.Name, Namespace: g.Namespace}
		if len(ig.Namespaces) > 0 {
			ctx.Namespace = ig.Namespaces[0]
		}
		kc.Contexts = append(kc.Contexts, namedContext{Name: ig.Name, Context: ctx})
	}
	return kc
}

// writePrivate writes data to a new file beside path, mode 0600, and renames it
// over path, so a reader sees the old file or the new one, never half of one.
func writePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".agentd-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
