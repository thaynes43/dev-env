package testenv

// Fixture-only support for the history measurement tests. This does not start
// envtest, access Kubernetes, or accept an existing HOME as a fixture.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const measureFixtureFormat = "dev-env.synthetic-history-measure.v1"

type measureFile struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type measureManifest struct {
	Format   string                 `json:"format"`
	Kind     string                 `json:"kind"`
	Metadata json.RawMessage        `json:"metadata"`
	Files    map[string]measureFile `json:"files"`
}

// MeasureFixture owns one newly created private directory. Frozen control files
// are regular, single-link files; reuse verifies their recorded size and digest.
type MeasureFixture struct {
	Path string
	root *os.Root
	m    measureManifest
}

func CreateMeasureFixture(path, kind string) (*MeasureFixture, error) {
	if err := measurePath(path); err != nil {
		return nil, err
	}
	if err := os.Mkdir(path, 0o700); err != nil { // Never overwrite/reuse on prepare.
		return nil, err
	}
	return openMeasureRoot(path, kind)
}

func OpenMeasureFixture(path, kind string) (*MeasureFixture, error) {
	f, err := openMeasureRoot(path, kind)
	if err != nil {
		return nil, err
	}
	b, err := f.read("fixture.json", 64<<10)
	if err == nil {
		err = json.Unmarshal(b, &f.m)
	}
	if err == nil && (f.m.Format != measureFixtureFormat || f.m.Kind != kind || len(f.m.Files) == 0) {
		err = errors.New("not a sealed synthetic measurement fixture of the requested kind")
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func measurePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || path == os.Getenv("HOME") {
		return errors.New("fixture needs a new clean absolute private directory, never HOME")
	}
	for p := path; p != "/"; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) && p == path {
			continue
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture path component is not a real directory: %s", p)
		}
	}
	return nil
}

func openMeasureRoot(path, kind string) (*MeasureFixture, error) {
	if err := measurePath(path); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm() != 0o700 {
		return nil, errors.New("fixture root must be owned by this user and mode 0700")
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &MeasureFixture{Path: path, root: r, m: measureManifest{Format: measureFixtureFormat, Kind: kind}}, nil
}

func (f *MeasureFixture) Close() error { return f.root.Close() }

func (f *MeasureFixture) Seal(metadata any, files map[string][]byte) error {
	b, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	f.m.Metadata, f.m.Files = b, make(map[string]measureFile)
	for name, data := range files {
		if name == "fixture.json" {
			return errors.New("reserved fixture file")
		}
		if err := f.write(name, data); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		f.m.Files[name] = measureFile{Bytes: len(data), SHA256: hex.EncodeToString(sum[:])}
	}
	b, err = json.Marshal(f.m)
	if err != nil {
		return err
	}
	return f.write("fixture.json", b)
}

func (f *MeasureFixture) Metadata(dst any) error { return json.Unmarshal(f.m.Metadata, dst) }

func (f *MeasureFixture) Read(name string, maxBytes int) ([]byte, error) {
	want, ok := f.m.Files[name]
	if !ok || want.Bytes > maxBytes {
		return nil, errors.New("file not recorded or above fixture input bound")
	}
	b, err := f.read(name, maxBytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if len(b) != want.Bytes || hex.EncodeToString(sum[:]) != want.SHA256 {
		return nil, errors.New("frozen fixture changed")
	}
	return b, nil
}

func measureFilename(name string) bool {
	return name != "." && name != ".." && name != "" && filepath.Base(name) == name
}

func (f *MeasureFixture) write(name string, b []byte) error {
	if !measureFilename(name) {
		return errors.New("fixture control filename must be a single component")
	}
	w, err := f.openControl(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	_, writeErr := w.Write(b)
	return errors.Join(writeErr, w.Sync(), w.Close())
}

func (f *MeasureFixture) read(name string, maxBytes int) ([]byte, error) {
	if !measureFilename(name) || maxBytes < 0 {
		return nil, errors.New("invalid fixture read")
	}
	r, err := f.openControl(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	fi, err := r.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() || st.Nlink != 1 || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o400 || fi.Size() > int64(maxBytes) {
		return nil, errors.New("fixture input must be a bounded private regular single-link frozen file")
	}
	return io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
}

// Root.OpenFile may resolve an in-root symlink before applying O_NOFOLLOW.
// A single-component openat on the anchored directory preserves final-link
// refusal, including a concurrent replacement between stat and open.
func (f *MeasureFixture) openControl(name string, flags int, mode os.FileMode) (*os.File, error) {
	dir, err := f.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// MeasureMemory reports totals, not deltas. Missing self-cgroup telemetry stays
// explicit: it must not become a zero peak or a successful resource acceptance.
type MeasureMemory struct {
	RSSKiB       int64             `json:"processRSSKiB"`
	PeakRSSKiB   int64             `json:"processPeakRSSKiB"`
	Cgroup       string            `json:"selfCgroup,omitempty"`
	CgroupMemory map[string]string `json:"cgroupMemory,omitempty"`
	Errors       []string          `json:"errors,omitempty"`
}

func ReadMeasureMemory() MeasureMemory {
	m := MeasureMemory{}
	seenRSS, seenPeak := false, false
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		m.Errors = append(m.Errors, err.Error())
	} else {
		for _, line := range strings.Split(string(b), "\n") {
			v := strings.Fields(line)
			if len(v) >= 2 && (v[0] == "VmRSS:" || v[0] == "VmHWM:") {
				n, err := strconv.ParseInt(v[1], 10, 64)
				if err != nil {
					m.Errors = append(m.Errors, err.Error())
				} else if v[0] == "VmRSS:" {
					m.RSSKiB = n
					seenRSS = true
				} else {
					m.PeakRSSKiB = n
					seenPeak = true
				}
			}
		}
	}
	if !seenRSS || !seenPeak {
		m.Errors = append(m.Errors, "process RSS/peak unavailable")
	}
	b, err = os.ReadFile("/proc/self/cgroup")
	if err != nil {
		m.Errors = append(m.Errors, err.Error())
		return m
	}
	cgroups := b
	b, err = os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		m.Errors = append(m.Errors, err.Error())
		return m
	}
	m.Cgroup, err = selfMeasureCgroup(cgroups, b)
	if err != nil {
		m.Errors = append(m.Errors, err.Error())
		return m
	}
	m.CgroupMemory = map[string]string{}
	for _, name := range []string{"memory.current", "memory.peak", "memory.max", "memory.events"} {
		data, err := os.ReadFile(filepath.Join(m.Cgroup, name))
		if err != nil {
			m.Errors = append(m.Errors, name+": "+err.Error())
		} else {
			m.CgroupMemory[name] = strings.TrimSpace(string(data))
		}
	}
	return m
}

func selfMeasureCgroup(cgroups, mountinfo []byte) (string, error) {
	var own string
	for _, line := range strings.Split(string(cgroups), "\n") {
		if strings.HasPrefix(line, "0::") {
			own = strings.TrimPrefix(line, "0::")
		}
	}
	for _, line := range strings.Split(string(mountinfo), "\n") {
		v := strings.Fields(line)
		if len(v) < 8 || !strings.Contains(line, " - cgroup2 ") || !filepath.IsAbs(own) || filepath.Clean(own) != own {
			continue
		}
		rel, err := filepath.Rel(v[3], own)
		if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
			continue
		}
		return filepath.Join(v[4], rel), nil
	}
	return "", errors.New("self cgroup v2 path cannot be resolved; no parent/root cgroup fallback")
}
