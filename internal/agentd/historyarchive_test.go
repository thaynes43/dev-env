package agentd

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	testSessionUID = "6f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	testHomeUID    = "7a1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	testPodUID     = "8b1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
)

// archiveRig is a shelf: the archive at ~/.history-archive of its own HOME,
// the same shared volume as the export's rig, and a fake clock.
type archiveRig struct {
	a   HistoryArchive
	now time.Time
}

func newArchiveRig(t *testing.T, r *historyRig) *archiveRig {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, protocol.HistoryArchiveDirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	old := historyArchiveIsMounted
	historyArchiveIsMounted = func(root, _ string) error {
		_, err := os.Stat(root)
		return err
	}
	t.Cleanup(func() { historyArchiveIsMounted = old })
	ar := &archiveRig{now: time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)}
	ar.a = HistoryArchive{Root: root, Home: home, SharedDir: r.s.SharedDir, Now: func() time.Time { return ar.now }}
	return ar
}

// testContent is the operator's half of a content manifest, joined with a
// plan: a Verified rescue when the plan lists its bundle, else CleanAndPushed.
func testContent(t *testing.T, plan protocol.HistoryExportPlan, project, uid string) protocol.HistoryContent {
	t.Helper()
	sum, _, err := HistoryPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	result := protocol.HistoryRescueCleanAndPushed
	if plan.Git != nil {
		result = protocol.HistoryRescueVerified
	}
	c := protocol.HistoryContent{
		Format:   protocol.HistoryArchiveFormat,
		Session:  protocol.HistorySession{Namespace: "dev-agents", Name: testHistorySession, UID: uid},
		Project:  project,
		Provider: protocol.HistoryProviderClaude,
		HomeUID:  testHomeUID,
		Terminal: protocol.HistoryTerminal{PodUID: testPodUID, Phase: "Failed", Reason: "DeadlineExceeded", NodeName: "talosw01", ObservedAt: time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)},
		Rescue:   protocol.HistoryRescue{Stamp: "20261012-0102", Result: result, PodUID: testPodUID},
	}
	protocol.HistoryContentFromPlan(&c, plan, sum)
	return c
}

// putInput is put's stdin: the canonical content line, then the stream.
func putInput(t *testing.T, r *historyRig, c protocol.HistoryContent) []byte {
	t.Helper()
	line, err := protocol.CanonicalJSON(c)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.Write(line)
	buf.WriteByte('\n')
	if err := StreamHistoryExport(r.s, r.o, c.PlanSHA256, &buf); err != nil {
		t.Fatalf("stream: %v", err)
	}
	return buf.Bytes()
}

// putTestHistory exports the rig's HOME into the archive.
func putTestHistory(t *testing.T, r *historyRig, ar *archiveRig, project, uid string) (protocol.HistoryContent, protocol.HistoryPutResult) {
	t.Helper()
	c := testContent(t, r.plan(t), project, uid)
	res, err := ar.a.Put(bytes.NewReader(putInput(t, r, c)), "dev-env-operator test")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	return c, res
}

func TestHistoryArchivePutStoresAndIsIdempotent(t *testing.T) {
	r := newHistoryRig(t)
	r.o.Rescue = writeTestRescue(t, r, "20261012-0102", nil)
	ar := newArchiveRig(t, r)
	c, res := putTestHistory(t, r, ar, "dev-env", testSessionUID)
	if res.Status != protocol.HistoryPutStored || res.Files != len(c.Files) || res.GitFiles != 2 || !strings.HasPrefix(res.Entry, "v1/dev-env/"+testSessionUID+"/") {
		t.Fatalf("put: %+v", res)
	}
	entry := filepath.Join(ar.a.Root, filepath.FromSlash(res.Entry))
	for _, rel := range []string{"content.json", "entry.json", "blocks/0.json", "files/claude/history.jsonl", "files/log/" + testHistorySession + ".log", "git/20261012-0102/dev-env.bundle", "git/20261012-0102/manifest.json"} {
		if _, err := os.Lstat(filepath.Join(entry, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(entry, "files/claude/file-history")); err == nil {
		t.Errorf("an excluded subtree was archived")
	}
	idx := filepath.Join(ar.a.Root, "index", "dev-env", "2026-11", "20261112T010000.000000000Z-"+testSessionUID+"-"+res.ContentSHA256[:16]+".json")
	if _, err := os.Lstat(idx); err != nil {
		t.Errorf("index: %v", err)
	}
	if v, err := ar.a.Verify(testSessionUID, res.ContentSHA256); err != nil || v.Files != len(c.Files) || v.GitFiles != 2 {
		t.Errorf("verify: %+v %v", v, err)
	}

	// The same put again, an hour later (a retry after a restart): exists,
	// with the same digest, and nothing rewritten.
	before, _ := os.ReadFile(filepath.Join(entry, "entry.json"))
	ar.now = ar.now.Add(time.Hour)
	again, err := ar.a.Put(bytes.NewReader(putInput(t, r, c)), "dev-env-operator other")
	if err != nil || again.Status != protocol.HistoryPutExists || again.ContentSHA256 != res.ContentSHA256 {
		t.Fatalf("second put: %+v %v", again, err)
	}
	after, _ := os.ReadFile(filepath.Join(entry, "entry.json"))
	if !bytes.Equal(before, after) {
		t.Errorf("a second put rewrote entry.json")
	}

	// Another content manifest at the same entry is a conflict, and nothing
	// is written over it.
	other := c
	other.Terminal.Reason = "Evicted"
	line, _ := protocol.CanonicalJSON(other)
	if err := os.WriteFile(filepath.Join(entry, "content.json"), line, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ar.a.Put(bytes.NewReader(putInput(t, r, c)), "x"); historyCode(err) != protocol.HistoryErrConflict {
		t.Errorf("put over another manifest: %v, want conflict", err)
	}
	if got, _ := os.ReadFile(filepath.Join(entry, "content.json")); !bytes.Equal(got, line) {
		t.Errorf("a conflicting put wrote over content.json")
	}

	// An entry whose content.json cannot be read is damaged, not another
	// manifest: corrupt, and still nothing is written.
	if err := os.Remove(filepath.Join(entry, "content.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ar.a.Put(bytes.NewReader(putInput(t, r, c)), "x"); historyCode(err) != protocol.HistoryErrCorrupt {
		t.Errorf("put over an entry without content.json: %v, want corrupt", err)
	}
	if _, err := os.Lstat(filepath.Join(entry, "content.json")); err == nil {
		t.Errorf("a put wrote into a damaged entry")
	}
}

func TestHistoryArchivePutRefusesStreamsThatDifferFromTheManifest(t *testing.T) {
	r := newHistoryRig(t)
	ar := newArchiveRig(t, r)
	plan := r.plan(t)
	c := testContent(t, plan, "dev-env", testSessionUID)
	good := putInput(t, r, c)
	nl := bytes.IndexByte(good, '\n')
	line, stream := good[:nl], good[nl+1:]

	// retar rewrites the stream through edit, which sees each entry's header
	// and bytes in order and returns what to write (nil drops it).
	retar := func(t *testing.T, edit func(i int, h *tar.Header, b []byte) [][2]any) []byte {
		t.Helper()
		var out bytes.Buffer
		out.Write(line)
		out.WriteByte('\n')
		tw := tar.NewWriter(&out)
		tr := tar.NewReader(bytes.NewReader(stream))
		for i := 0; ; i++ {
			h, err := tr.Next()
			if err != nil {
				break
			}
			var b bytes.Buffer
			_, _ = b.ReadFrom(tr)
			for _, w := range edit(i, h, b.Bytes()) {
				hh, bb := w[0].(*tar.Header), w[1].([]byte)
				hh.Size = int64(len(bb))
				if err := tw.WriteHeader(hh); err != nil {
					t.Fatal(err)
				}
				_, _ = tw.Write(bb)
			}
		}
		_ = tw.Close()
		return out.Bytes()
	}
	keep := func(_ int, h *tar.Header, b []byte) [][2]any { return [][2]any{{h, b}} }
	withLine := func(c protocol.HistoryContent) []byte {
		l, _ := protocol.CanonicalJSON(c)
		return append(append(l, '\n'), stream...)
	}
	cases := map[string][]byte{
		"an extra entry": retar(t, func(i int, h *tar.Header, b []byte) [][2]any {
			if i == len(c.Files)-1 {
				return [][2]any{{h, b}, {&tar.Header{Typeflag: tar.TypeReg, Name: "claude/zz-extra", Mode: 0o600}, []byte("x")}}
			}
			return keep(i, h, b)
		}),
		"a missing entry": retar(t, func(i int, h *tar.Header, b []byte) [][2]any {
			if i == 3 {
				return nil
			}
			return keep(i, h, b)
		}),
		"a changed byte": retar(t, func(i int, h *tar.Header, b []byte) [][2]any {
			if i == 0 {
				b = append([]byte{}, b...)
				b[0] ^= 1
			}
			return keep(i, h, b)
		}),
		"entries out of order": retar(t, func(i int, h *tar.Header, b []byte) [][2]any {
			if i == 0 {
				h.Name = c.Files[1].Name()
			}
			return keep(i, h, b)
		}),
		"a link entry": retar(t, func(i int, h *tar.Header, b []byte) [][2]any {
			if i == 0 {
				return [][2]any{{&tar.Header{Typeflag: tar.TypeSymlink, Name: h.Name, Linkname: "/etc/passwd"}, []byte{}}}
			}
			return keep(i, h, b)
		}),
		"a truncated stream":        append(append(append([]byte{}, line...), '\n'), stream[:len(stream)/2]...),
		"a manifest not canonical":  append([]byte(" "+string(line)+"\n"), stream...),
		"an unknown manifest field": append([]byte(strings.Replace(string(line), `{"bytes"`, `{"admin":true,"bytes"`, 1)+"\n"), stream...),
	}
	bad := func(edit func(*protocol.HistoryContent)) []byte {
		cc := c
		cc.Files = append([]protocol.HistoryFile(nil), c.Files...)
		edit(&cc)
		return withLine(cc)
	}
	cases["a .. path"] = bad(func(cc *protocol.HistoryContent) { cc.Files[0].Path = "../escape" })
	cases["an absolute path"] = bad(func(cc *protocol.HistoryContent) { cc.Files[0].Path = "/etc/passwd" })
	cases["a credential name"] = bad(func(cc *protocol.HistoryContent) { cc.Files[0].Path = "decisions/auth.json" })
	cases["an unknown root"] = bad(func(cc *protocol.HistoryContent) { cc.Files[0].Root = "ssh" })
	cases["a nested log"] = bad(func(cc *protocol.HistoryContent) {
		cc.Files[len(cc.Files)-1].Path = "a/b.log"
	})
	cases["another format"] = bad(func(cc *protocol.HistoryContent) { cc.Format = "v0" })
	cases["a bad Session UID"] = bad(func(cc *protocol.HistoryContent) { cc.Session.UID = "../x" })
	cases["a project path"] = bad(func(cc *protocol.HistoryContent) { cc.Project = "../x" })
	cases["a wrong total"] = bad(func(cc *protocol.HistoryContent) { cc.Bytes++ })
	cases["block lists in the manifest"] = bad(func(cc *protocol.HistoryContent) { cc.Files[0].Blocks = []string{strings.Repeat("0", 64)} })

	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ar.a.Put(bytes.NewReader(input), "x")
			if historyCode(err) != protocol.HistoryErrInvalid {
				t.Errorf("put: %v, want invalid", err)
			}
			if _, err := os.Lstat(filepath.Join(ar.a.Root, "v1")); err == nil {
				t.Errorf("a refused put published an entry")
			}
		})
	}
}

// TestHistoryArchivePutRefusesManifestsThatProveLess: verify compares an
// entry only with its own manifest, and H6 deletes the HOME after verify, so
// put refuses a manifest that does not show a whole export behind a rescue
// that lets the HOME go.
func TestHistoryArchivePutRefusesManifestsThatProveLess(t *testing.T) {
	r := newHistoryRig(t)
	r.o.Rescue = writeTestRescue(t, r, "20261012-0102", nil)
	ar := newArchiveRig(t, r)
	c := testContent(t, r.plan(t), "dev-env", testSessionUID)
	good := putInput(t, r, c)
	stream := good[bytes.IndexByte(good, '\n')+1:]
	bad := func(edit func(*protocol.HistoryContent)) []byte {
		cc := c
		cc.Roots = append([]protocol.HistoryRoot(nil), c.Roots...)
		g := *c.Git
		cc.Git = &g
		edit(&cc)
		l, err := protocol.CanonicalJSON(cc)
		if err != nil {
			t.Fatal(err)
		}
		return append(append(l, '\n'), stream...)
	}
	cases := map[string][]byte{
		"a Failed rescue":          bad(func(cc *protocol.HistoryContent) { cc.Rescue.Result = "Failed" }),
		"a NoWorkAdmitted rescue":  bad(func(cc *protocol.HistoryContent) { cc.Rescue.Result = "NoWorkAdmitted" }),
		"a result in another case": bad(func(cc *protocol.HistoryContent) { cc.Rescue.Result = "verified" }),
		"a Verified rescue without its bundle": bad(func(cc *protocol.HistoryContent) {
			cc.Git = nil
		}),
		"another rescue's bundle": bad(func(cc *protocol.HistoryContent) { cc.Git.Rescue = testHistorySession + "/20261012-0999" }),
		"a bundle of an earlier minute's rescue": bad(func(cc *protocol.HistoryContent) {
			cc.Rescue.Stamp = "20261012-0103"
		}),
		"a stamp with a directory's suffix": bad(func(cc *protocol.HistoryContent) { cc.Rescue.Stamp = "20261012-0102-2" }),
		"agentd's root elsewhere": bad(func(cc *protocol.HistoryContent) {
			for i := range cc.Roots {
				if cc.Roots[i].Name == protocol.HistoryRootAgentd {
					cc.Roots[i].Dir = ".agentd-old"
				}
			}
		}),
		"another Session's log": bad(func(cc *protocol.HistoryContent) {
			for i := range cc.Roots {
				if cc.Roots[i].Name == protocol.HistoryRootLog {
					cc.Roots[i].Dir = "work/other.log"
				}
			}
		}),
	}
	// A root with nothing in it, left out: the manifest no longer shows that
	// CODEX_HOME was looked at.
	rn := newHistoryRig(t)
	rn.o.Rescue = writeTestRescue(t, rn, "20261012-0102", nil)
	rn.o.CodexHome = ".codex-never-made"
	cn := testContent(t, rn.plan(t), "dev-env", testSessionUID)
	gn := putInput(t, rn, cn)
	var kept []protocol.HistoryRoot
	for _, root := range cn.Roots {
		if root.Name != protocol.HistoryRootCodex {
			kept = append(kept, root)
		}
	}
	cn.Roots = kept
	ln, err := protocol.CanonicalJSON(cn)
	if err != nil {
		t.Fatal(err)
	}
	cases["a missing root"] = append(append(ln, '\n'), gn[bytes.IndexByte(gn, '\n')+1:]...)

	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ar.a.Put(bytes.NewReader(input), "x"); historyCode(err) != protocol.HistoryErrInvalid {
				t.Errorf("put: %v, want invalid", err)
			}
			if _, err := os.Lstat(filepath.Join(ar.a.Root, "v1")); err == nil {
				t.Errorf("a refused put published an entry")
			}
		})
	}

	// A later rescue in the same minute has the directory <stamp>-2, which
	// stands for the rescue <stamp>.
	r2 := newHistoryRig(t)
	writeTestRescue(t, r2, "20261012-0102", nil)
	r2.o.Rescue = writeTestRescue(t, r2, "20261012-0102-2", nil)
	ar2 := newArchiveRig(t, r2)
	c2 := testContent(t, r2.plan(t), "dev-env", testSessionUID)
	if res, err := ar2.a.Put(bytes.NewReader(putInput(t, r2, c2)), "x"); err != nil || res.Status != protocol.HistoryPutStored || res.GitFiles != 2 {
		t.Errorf("put of the rescue's -2 directory: %+v %v", res, err)
	}
}

func TestHistoryArchiveSizeCaps(t *testing.T) {
	r := newHistoryRig(t)
	ar := newArchiveRig(t, r)
	c := testContent(t, r.plan(t), "dev-env", testSessionUID)
	input := putInput(t, r, c)
	old := historyBounds
	t.Cleanup(func() { historyBounds = old })
	for name, lower := range map[string]func(){
		"one file over its bound":           func() { historyBounds.fileBytes = 1 << 20 },
		"over the total":                    func() { historyBounds.totalBytes = 2 << 20 },
		"too many files":                    func() { historyBounds.files = 10 },
		"a content manifest over its bound": func() { historyBounds.contentBytes = 1 << 10 },
	} {
		t.Run(name, func(t *testing.T) {
			historyBounds = old
			lower()
			if _, err := ar.a.Put(bytes.NewReader(input), "x"); historyCode(err) != protocol.HistoryErrInvalid {
				t.Errorf("put: %v, want invalid", err)
			}
		})
	}
}

func TestHistoryArchiveVerifyDetectsTamper(t *testing.T) {
	cases := map[string]func(t *testing.T, entry string){
		"a changed byte in a file": func(t *testing.T, entry string) {
			p := filepath.Join(entry, "files/claude/history.jsonl")
			b, _ := os.ReadFile(p)
			b[0] ^= 1
			_ = os.WriteFile(p, b, 0o600)
		},
		"a changed block list": func(t *testing.T, entry string) {
			_ = os.WriteFile(filepath.Join(entry, "blocks/0.json"), []byte(`["`+strings.Repeat("0", 64)+`"]`), 0o600)
		},
		"a changed content.json": func(t *testing.T, entry string) {
			p := filepath.Join(entry, "content.json")
			b, _ := os.ReadFile(p)
			_ = os.WriteFile(p, bytes.Replace(b, []byte("DeadlineExceeded"), []byte("DeadlineExceedeD"), 1), 0o600)
		},
		"a changed entry.json": func(t *testing.T, entry string) {
			_ = os.WriteFile(filepath.Join(entry, "entry.json"), []byte(`{}`), 0o600)
		},
		"a file added": func(t *testing.T, entry string) {
			_ = os.WriteFile(filepath.Join(entry, "files/claude/planted"), []byte("x"), 0o600)
		},
		"a file removed": func(t *testing.T, entry string) {
			_ = os.Remove(filepath.Join(entry, "files/codex/history.jsonl"))
		},
		"a file replaced by a link": func(t *testing.T, entry string) {
			p := filepath.Join(entry, "files/codex/history.jsonl")
			_ = os.Remove(p)
			_ = os.Symlink("/etc/hostname", p)
		},
		"a changed Git file": func(t *testing.T, entry string) {
			_ = os.WriteFile(filepath.Join(entry, "git/20261012-0102/dev-env.bundle"), []byte("other"), 0o600)
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			r := newHistoryRig(t)
			r.o.Rescue = writeTestRescue(t, r, "20261012-0102", nil)
			ar := newArchiveRig(t, r)
			_, res := putTestHistory(t, r, ar, "dev-env", testSessionUID)
			tamper(t, filepath.Join(ar.a.Root, filepath.FromSlash(res.Entry)))
			if _, err := ar.a.Verify(testSessionUID, res.ContentSHA256); historyCode(err) != protocol.HistoryErrCorrupt {
				t.Errorf("verify: %v, want corrupt", err)
			}
		})
	}
	r := newHistoryRig(t)
	ar := newArchiveRig(t, r)
	if _, err := ar.a.Verify(testSessionUID, strings.Repeat("a", 64)); historyCode(err) != protocol.HistoryErrNotFound {
		t.Errorf("verify of nothing: %v, want not_found", err)
	}
	_, res := putTestHistory(t, r, ar, "", testSessionUID)
	other := res.ContentSHA256[:63] + "0"
	if other == res.ContentSHA256 {
		other = res.ContentSHA256[:63] + "1"
	}
	if _, err := ar.a.Verify(testSessionUID, other); historyCode(err) != protocol.HistoryErrCorrupt {
		t.Errorf("verify of another digest with the same prefix answered no error")
	}
	if _, err := ar.a.Verify(testSessionUID, res.ContentSHA256); err != nil || !strings.HasPrefix(res.Entry, "v1/_none/") {
		t.Errorf("a history with no project: %v %s", err, res.Entry)
	}
}

func TestHistoryArchivePartialPutRecoversAfterARestart(t *testing.T) {
	r := newHistoryRig(t)
	ar := newArchiveRig(t, r)
	c := testContent(t, r.plan(t), "dev-env", testSessionUID)
	input := putInput(t, r, c)

	// A refused put, such as one whose stream was cut, leaves nothing.
	if _, err := ar.a.Put(bytes.NewReader(input[:len(input)/2]), "x"); historyCode(err) != protocol.HistoryErrInvalid {
		t.Fatalf("a cut stream: %v", err)
	}
	incoming := filepath.Join(ar.a.Root, protocol.HistoryArchiveIncoming)
	if debris, _ := os.ReadDir(incoming); len(debris) != 0 {
		t.Fatalf("a refused put left debris: %v", debris)
	}
	// A put that crashed mid-stream leaves its incoming directory.
	old := filepath.Join(incoming, "0123456789abcdef0123456789abcdef")
	writeTestFile(t, filepath.Join(old, "files", "claude", "history.jsonl"), "half")
	if err := os.Chtimes(old, ar.now.Add(-2*time.Hour), ar.now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	young := filepath.Join(incoming, "young")
	if err := os.Mkdir(young, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(young, ar.now.Add(-10*time.Minute), ar.now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// The operator restarts and puts again, through a new shelf process.
	again := HistoryArchive{Root: ar.a.Root, Home: ar.a.Home, SharedDir: ar.a.SharedDir, Now: ar.a.Now}
	res, err := again.Put(bytes.NewReader(input), "x")
	if err != nil || res.Status != protocol.HistoryPutStored {
		t.Fatalf("put after a crash: %+v %v", res, err)
	}
	if _, err := os.Lstat(old); err == nil {
		t.Errorf("debris older than an hour was kept")
	}
	if _, err := os.Lstat(young); err != nil {
		t.Errorf("debris younger than an hour was removed: %v", err)
	}
	if _, err := again.Verify(testSessionUID, res.ContentSHA256); err != nil {
		t.Errorf("verify: %v", err)
	}

	// A put that stopped between the rename and the index: the next put
	// answers exists and writes the index.
	idxDir := filepath.Join(ar.a.Root, "index", "dev-env")
	if err := os.RemoveAll(idxDir); err != nil {
		t.Fatal(err)
	}
	res2, err := again.Put(bytes.NewReader(input), "x")
	if err != nil || res2.Status != protocol.HistoryPutExists {
		t.Fatalf("put after a lost index: %+v %v", res2, err)
	}
	list, err := again.List("dev-env", 0, "")
	if err != nil || len(list.Items) != 1 || list.Items[0].ContentSHA256 != res.ContentSHA256 {
		t.Errorf("list after the repair: %+v %v", list, err)
	}

	// A put that stopped between linking the index file and removing its
	// .incoming copy leaves the index file with two links: list still reads
	// it, until a put an hour later removes the copy.
	months, _ := os.ReadDir(idxDir)
	if len(months) != 1 {
		t.Fatalf("index months: %v", months)
	}
	files, _ := os.ReadDir(filepath.Join(idxDir, months[0].Name()))
	if len(files) != 1 {
		t.Fatalf("index files: %v", files)
	}
	leftover := filepath.Join(incoming, "fedcba9876543210fedcba9876543210")
	if err := os.Mkdir(leftover, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(idxDir, months[0].Name(), files[0].Name()), filepath.Join(leftover, "index.json")); err != nil {
		t.Fatal(err)
	}
	list, err = again.List("dev-env", 0, "")
	if err != nil || len(list.Items) != 1 || list.Items[0].Error != "" || list.Items[0].ContentSHA256 != res.ContentSHA256 {
		t.Errorf("list with an index file's leftover link: %+v %v", list, err)
	}
	if err := os.RemoveAll(leftover); err != nil {
		t.Fatal(err)
	}

	// An index write that fails removes its own .incoming directory, and the
	// next put repairs the index.
	if err := os.RemoveAll(idxDir); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, idxDir, "in the way")
	if _, err := again.Put(bytes.NewReader(input), "x"); err == nil {
		t.Fatalf("a put whose index cannot be written succeeded")
	}
	left, _ := os.ReadDir(incoming)
	for _, d := range left {
		if d.Name() != "young" {
			t.Errorf("a failed index write left %s in .incoming", d.Name())
		}
	}
	if err := os.Remove(idxDir); err != nil {
		t.Fatal(err)
	}
	if res3, err := again.Put(bytes.NewReader(input), "x"); err != nil || res3.Status != protocol.HistoryPutExists {
		t.Errorf("put after a failed index write: %+v %v", res3, err)
	}
	if list, err := again.List("dev-env", 0, ""); err != nil || len(list.Items) != 1 {
		t.Errorf("list after the index repair: %+v %v", list, err)
	}
}

func TestHistoryArchiveGetListAndRead(t *testing.T) {
	r := newHistoryRig(t)
	ar := newArchiveRig(t, r)
	uids := []string{
		"11111111-4a5b-4c6d-8e7f-0a1b2c3d4e5f",
		"22222222-4a5b-4c6d-8e7f-0a1b2c3d4e5f",
		"33333333-4a5b-4c6d-8e7f-0a1b2c3d4e5f",
	}
	var sums []string
	for i, uid := range uids {
		ar.now = time.Date(2026, 10+time.Month(i), 20, 0, 0, 0, 0, time.UTC)
		_, res := putTestHistory(t, r, ar, "dev-env", uid)
		sums = append(sums, res.ContentSHA256)
	}
	_, _ = putTestHistory(t, r, ar, "other", "44444444-4a5b-4c6d-8e7f-0a1b2c3d4e5f")

	// Newest first, a page at a time.
	page1, err := ar.a.List("dev-env", 2, "")
	if err != nil || len(page1.Items) != 2 || page1.Items[0].SessionUID != uids[2] || page1.Items[1].SessionUID != uids[1] || page1.Next == "" {
		t.Fatalf("page 1: %+v %v", page1, err)
	}
	if it := page1.Items[0]; it.Provider != "claude" || it.Terminal == nil || it.Terminal.Phase != "Failed" || it.Files == 0 || it.Session != testHistorySession {
		t.Errorf("summary: %+v", it)
	}
	page2, err := ar.a.List("dev-env", 2, page1.Next)
	if err != nil || len(page2.Items) != 1 || page2.Items[0].SessionUID != uids[0] || page2.Next != "" {
		t.Fatalf("page 2: %+v %v", page2, err)
	}
	if empty, err := ar.a.List("nothing-here", 0, ""); err != nil || len(empty.Items) != 0 {
		t.Errorf("an empty project: %+v %v", empty, err)
	}
	for _, bad := range []struct {
		project, after string
		limit          int
	}{{"../x", "", 0}, {"dev-env", "", 101}, {"dev-env", "../../x", 0}, {"_none", "", 0}} {
		if _, err := ar.a.List(bad.project, bad.limit, bad.after); historyCode(err) != protocol.HistoryErrInvalid {
			t.Errorf("list %+v: %v, want invalid", bad, err)
		}
	}

	// A damaged entry is listed as corrupt, without its content.
	entry := filepath.Join(ar.a.Root, "v1", "dev-env", uids[1], sums[1][:16])
	if err := os.WriteFile(filepath.Join(entry, "content.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	page1, _ = ar.a.List("dev-env", 2, "")
	if it := page1.Items[1]; it.Error != protocol.HistoryErrCorrupt || it.Provider != "" || it.SessionUID != uids[1] {
		t.Errorf("a damaged entry: %+v", it)
	}

	// get: the newest entry of a Session, or the one named.
	g, err := ar.a.Get(uids[0], "")
	if err != nil || g.ContentSHA256 != sums[0] || g.Content.Session.UID != uids[0] || g.Entry.ArchivedBy != "dev-env-operator test" {
		t.Errorf("get: %+v %v", g.Entry, err)
	}
	if _, err := ar.a.Get(uids[1], sums[1]); historyCode(err) != protocol.HistoryErrCorrupt {
		t.Errorf("get of a damaged entry: %v", err)
	}

	// read: one page of a 2.5 MiB file, across a block boundary, and to its
	// end.
	const big = "claude/projects/-home-dev/big.jsonl"
	want, _ := os.ReadFile(r.path(".claude/projects/-home-dev/big.jsonl"))
	p, err := ar.a.Read(uids[0], sums[0], big, 0)
	if err != nil || len(p.Data) != protocol.MaxHistoryReadBytes || !bytes.Equal(p.Data, want[:protocol.MaxHistoryReadBytes]) || p.Next != protocol.MaxHistoryReadBytes || p.EOF {
		t.Fatalf("read page 1: next %d eof %v len %d %v", p.Next, p.EOF, len(p.Data), err)
	}
	across := int64(protocol.HistoryBlockSize - 100)
	p, err = ar.a.Read(uids[0], sums[0], big, across)
	if err != nil || !bytes.Equal(p.Data, want[across:across+protocol.MaxHistoryReadBytes]) {
		t.Fatalf("read across blocks: %v", err)
	}
	last := int64(len(want)) - 10
	p, err = ar.a.Read(uids[0], sums[0], big, last)
	if err != nil || !bytes.Equal(p.Data, want[last:]) || !p.EOF || p.Next != int64(len(want)) {
		t.Fatalf("read the end: %+v %v", p.EOF, err)
	}
	if p, err := ar.a.Read(uids[0], sums[0], big, int64(len(want))); err != nil || len(p.Data) != 0 || !p.EOF {
		t.Errorf("read at the end: %v", err)
	}
	if _, err := ar.a.Read(uids[0], sums[0], big, int64(len(want))+1); historyCode(err) != protocol.HistoryErrInvalid {
		t.Errorf("read past the end: %v", err)
	}
	if _, err := ar.a.Read(uids[0], sums[0], "claude/file-history/x", 0); historyCode(err) != protocol.HistoryErrNotFound {
		t.Errorf("read of an excluded name: %v", err)
	}
	// A changed byte in the second block refuses a page that needs it, and
	// still serves one that does not.
	entry0 := filepath.Join(ar.a.Root, "v1", "dev-env", uids[0], sums[0][:16])
	f, err := os.OpenFile(filepath.Join(entry0, "files", filepath.FromSlash(big)), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteAt([]byte{'X'}, protocol.HistoryBlockSize+5)
	_ = f.Close()
	if _, err := ar.a.Read(uids[0], sums[0], big, across); historyCode(err) != protocol.HistoryErrCorrupt {
		t.Errorf("read of a changed block: %v, want corrupt", err)
	}
	if _, err := ar.a.Read(uids[0], sums[0], big, 0); err != nil {
		t.Errorf("read of an unchanged block: %v", err)
	}
}

// manifest (DESIGN-025 8, H6) pages an entry's verified manifest: the
// header and counts on every page, then the files and the excluded names in
// one sequence, so neither the operator nor the API holds a whole
// content.json. An offset past the end is an empty page, never an error.
func TestHistoryArchiveManifestPages(t *testing.T) {
	r := newHistoryRig(t)
	ar := newArchiveRig(t, r)
	c, put := putTestHistory(t, r, ar, "dev-env", testSessionUID)
	total := len(c.Files) + len(c.Excluded)
	if len(c.Files) < 2 || len(c.Excluded) < 2 {
		t.Fatalf("the rig's history is too small to page: %d files, %d excluded", len(c.Files), len(c.Excluded))
	}

	head, err := ar.a.Manifest(testSessionUID, "", 0, 0)
	if err != nil {
		t.Fatalf("header only: %v", err)
	}
	if head.ContentSHA256 != put.ContentSHA256 || head.Entry.ContentSHA256 != put.ContentSHA256 || head.Entry.ArchivedBy != "dev-env-operator test" ||
		head.Session != c.Session || head.Project != "dev-env" || head.Provider != c.Provider || head.HomeUID != c.HomeUID ||
		head.Terminal != c.Terminal || head.Rescue != c.Rescue || head.Bytes != c.Bytes || head.PlanSHA256 != c.PlanSHA256 ||
		head.FileCount != len(c.Files) || head.ExcludedCount != len(c.Excluded) || len(head.Roots) != len(c.Roots) {
		t.Errorf("header: %+v", head)
	}
	if len(head.Files) != 0 || len(head.Excluded) != 0 || head.Next != 0 || head.Offset != 0 {
		t.Errorf("limit 0 prints the header only: %+v", head)
	}

	// Pages of two, files first, then the excluded names, in the manifest's
	// order.
	var files []protocol.HistoryFile
	var excluded []protocol.HistoryExcluded
	offset, pages := 0, 0
	for {
		p, err := ar.a.Manifest(testSessionUID, put.ContentSHA256, offset, 2)
		if err != nil {
			t.Fatalf("page at %d: %v", offset, err)
		}
		if p.Offset != offset || len(p.Files)+len(p.Excluded) > 2 || p.FileCount != len(c.Files) {
			t.Fatalf("page at %d: %+v", offset, p)
		}
		files, excluded = append(files, p.Files...), append(excluded, p.Excluded...)
		pages++
		if p.Next == 0 {
			break
		}
		if p.Next != offset+2 {
			t.Fatalf("page at %d: next %d", offset, p.Next)
		}
		offset = p.Next
	}
	if pages != (total+1)/2 || len(files) != len(c.Files) || len(excluded) != len(c.Excluded) {
		t.Fatalf("%d pages, %d files, %d excluded; want %d, %d, %d", pages, len(files), len(excluded), (total+1)/2, len(c.Files), len(c.Excluded))
	}
	for i := range files {
		if files[i].Name() != c.Files[i].Name() || files[i].SHA256 != c.Files[i].SHA256 || files[i].Bytes != c.Files[i].Bytes || len(files[i].Blocks) != 0 {
			t.Errorf("file %d: %+v, want %+v", i, files[i], c.Files[i])
		}
	}
	for i := range excluded {
		if excluded[i] != c.Excluded[i] {
			t.Errorf("excluded %d: %+v, want %+v", i, excluded[i], c.Excluded[i])
		}
	}

	// Past the end, and far past it: an empty last page.
	for _, off := range []int{total, total + 1, 1 << 40} {
		p, err := ar.a.Manifest(testSessionUID, "", off, protocol.MaxHistoryManifestPage)
		if err != nil || len(p.Files) != 0 || len(p.Excluded) != 0 || p.Next != 0 || p.FileCount != len(c.Files) {
			t.Errorf("offset %d: %+v %v", off, p, err)
		}
	}
	// One page holds them all.
	if p, err := ar.a.Manifest(testSessionUID, "", 0, protocol.MaxHistoryManifestPage); err != nil || len(p.Files)+len(p.Excluded) != total || p.Next != 0 {
		t.Errorf("one page: %d items, next %d, %v", len(p.Files)+len(p.Excluded), p.Next, err)
	}

	for _, bad := range []struct {
		uid, sum      string
		offset, limit int
	}{
		{testSessionUID, "", -1, 1},
		{testSessionUID, "", 0, -1},
		{testSessionUID, "", 0, protocol.MaxHistoryManifestPage + 1},
		{"not-a-uid", "", 0, 1},
		{testSessionUID, "abc", 0, 1},
	} {
		if _, err := ar.a.Manifest(bad.uid, bad.sum, bad.offset, bad.limit); historyCode(err) != protocol.HistoryErrInvalid {
			t.Errorf("manifest %+v: %v, want invalid", bad, err)
		}
	}
	if _, err := ar.a.Manifest("99999999-4a5b-4c6d-8e7f-0a1b2c3d4e5f", "", 0, 1); historyCode(err) != protocol.HistoryErrNotFound {
		t.Errorf("an unknown Session: %v, want not_found", err)
	}
	if _, err := ar.a.Manifest(testSessionUID, strings.Repeat("a", 64), 0, 1); historyCode(err) != protocol.HistoryErrNotFound {
		t.Errorf("an unknown digest: %v, want not_found", err)
	}

	// A damaged content.json is corrupt, and nothing of it is printed.
	entry := filepath.Join(ar.a.Root, "v1", "dev-env", testSessionUID, put.ContentSHA256[:16])
	if err := os.WriteFile(filepath.Join(entry, "content.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := ar.a.Manifest(testSessionUID, "", 0, 1); historyCode(err) != protocol.HistoryErrCorrupt || p.Project != "" || len(p.Files) != 0 {
		t.Errorf("a damaged entry: %+v %v, want corrupt and nothing else", p, err)
	}
}

func TestHistoryArchiveNeedsItsVolume(t *testing.T) {
	r := newHistoryRig(t)
	ar := newArchiveRig(t, r)
	historyArchiveIsMounted = func(string, string) error { return errors.New("not mounted") }
	if _, err := ar.a.Put(strings.NewReader(""), "x"); historyCode(err) != protocol.HistoryErrNoArchive {
		t.Errorf("put: %v", err)
	}
	if _, err := ar.a.Verify(testSessionUID, strings.Repeat("a", 64)); historyCode(err) != protocol.HistoryErrNoArchive {
		t.Errorf("verify: %v", err)
	}
	if _, err := ar.a.List("dev-env", 0, ""); historyCode(err) != protocol.HistoryErrNoArchive {
		t.Errorf("list: %v", err)
	}
	if _, err := ar.a.Get(testSessionUID, ""); historyCode(err) != protocol.HistoryErrNoArchive {
		t.Errorf("get: %v", err)
	}
	if _, err := ar.a.Read(testSessionUID, strings.Repeat("a", 64), "x/y", 0); historyCode(err) != protocol.HistoryErrNoArchive {
		t.Errorf("read: %v", err)
	}
	if _, err := ar.a.Manifest(testSessionUID, "", 0, 1); historyCode(err) != protocol.HistoryErrNoArchive {
		t.Errorf("manifest: %v", err)
	}
}

func TestHistoryArchiveChecksTheGitRescueItCopies(t *testing.T) {
	r := newHistoryRig(t)
	r.o.Rescue = writeTestRescue(t, r, "20261012-0102", nil)
	ar := newArchiveRig(t, r)
	c := testContent(t, r.plan(t), "dev-env", testSessionUID)
	input := putInput(t, r, c)
	// The bundle changes on the shared volume between the plan and the put.
	writeTestFile(t, filepath.Join(r.s.SharedDir, "rescue", testHistorySession, "20261012-0102", "dev-env.bundle"), "changed")
	if _, err := ar.a.Put(bytes.NewReader(input), "x"); historyCode(err) != protocol.HistoryErrInvalid {
		t.Errorf("put with a changed bundle: %v, want invalid", err)
	}
	if _, err := os.Lstat(filepath.Join(ar.a.Root, "v1")); err == nil {
		t.Errorf("a refused put published an entry")
	}

	// The rescue directory swapped for a link to a copy with the same bytes,
	// which agents can do on the shared volume: put never copies through it.
	r2 := newHistoryRig(t)
	r2.o.Rescue = writeTestRescue(t, r2, "20261012-0102", nil)
	ar2 := newArchiveRig(t, r2)
	c2 := testContent(t, r2.plan(t), "dev-env", testSessionUID)
	input2 := putInput(t, r2, c2)
	dir := filepath.Join(r2.s.SharedDir, "rescue", testHistorySession)
	if err := os.Rename(filepath.Join(dir, "20261012-0102"), filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("moved", filepath.Join(dir, "20261012-0102")); err != nil {
		t.Fatal(err)
	}
	if _, err := ar2.a.Put(bytes.NewReader(input2), "x"); historyCode(err) != protocol.HistoryErrInvalid {
		t.Errorf("put through a linked rescue directory: %v, want invalid", err)
	}
	if _, err := os.Lstat(filepath.Join(ar2.a.Root, "v1")); err == nil {
		t.Errorf("a refused put published an entry")
	}
}
