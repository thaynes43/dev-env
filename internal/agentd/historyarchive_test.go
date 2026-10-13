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
// plan.
func testContent(t *testing.T, plan protocol.HistoryExportPlan, project, uid string) protocol.HistoryContent {
	t.Helper()
	sum, _, err := HistoryPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	c := protocol.HistoryContent{
		Format:   protocol.HistoryArchiveFormat,
		Session:  protocol.HistorySession{Namespace: "dev-agents", Name: testHistorySession, UID: uid},
		Project:  project,
		Provider: protocol.HistoryProviderClaude,
		HomeUID:  testHomeUID,
		Terminal: protocol.HistoryTerminal{PodUID: testPodUID, Phase: "Failed", Reason: "DeadlineExceeded", NodeName: "talosw01", ObservedAt: time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)},
		Rescue:   protocol.HistoryRescue{Stamp: "20261012-0102", Result: "Verified", PodUID: testPodUID},
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

func TestHistoryArchiveSizeCaps(t *testing.T) {
	r := newHistoryRig(t)
	ar := newArchiveRig(t, r)
	c := testContent(t, r.plan(t), "dev-env", testSessionUID)
	input := putInput(t, r, c)
	old := historyBounds
	t.Cleanup(func() { historyBounds = old })
	for name, lower := range map[string]func(){
		"one file over its bound": func() { historyBounds.fileBytes = 1 << 20 },
		"over the total":          func() { historyBounds.totalBytes = 2 << 20 },
		"too many files":          func() { historyBounds.files = 10 },
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
	if _, err := ar.a.Verify(testSessionUID, res.ContentSHA256[:63]+"0"); historyCode(err) == "" {
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

	// A put that crashed mid-stream leaves its incoming directory.
	if _, err := ar.a.Put(bytes.NewReader(input[:len(input)/2]), "x"); historyCode(err) != protocol.HistoryErrInvalid {
		t.Fatalf("a cut stream: %v", err)
	}
	incoming := filepath.Join(ar.a.Root, protocol.HistoryArchiveIncoming)
	debris, _ := os.ReadDir(incoming)
	if len(debris) != 1 {
		t.Fatalf("debris: %v", debris)
	}
	old := filepath.Join(incoming, debris[0].Name())
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
}
