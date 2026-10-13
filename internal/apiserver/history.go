package apiserver

// The archived-history reads (DESIGN-025 8, unit H6): a private Session's
// provider history, exported into the shelf's history archive before its HOME
// expires, read back under DESIGN-013's payload-read scope. Every read goes to
// the shelf by exec and takes one of the replica's shelf exec slots. The
// answers reveal nothing outside the caller's scope: a single read answers 404
// at once for a caller with the scope for no project, and 404 for an entry of
// a project outside it, as for one that does not exist; every caller in scope
// gets 503 while the shelf cannot answer. Off (the default), every history
// route answers 404, as if no history existed.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// HistoryArchive reads the shelf's history archive: shelf.HistoryReader,
// which runs `agentd ctl history-archive list|manifest|read` in the shelf pod.
// A refusal of agentd's is a *protocol.HistoryError.
type HistoryArchive interface {
	List(ctx context.Context, project string, limit int, after string) (protocol.HistoryList, error)
	Manifest(ctx context.Context, sessionUID, content string, offset, limit int) (protocol.HistoryManifestPage, error)
	Read(ctx context.Context, sessionUID, content, file string, offset int64) (protocol.HistoryReadResult, error)
}

// historyManifestOffsetMax bounds a manifest cursor: an entry has at most
// 100,000 files and 100,000 excluded names.
const historyManifestOffsetMax = 1 << 30

func historyHidden() error { return notFound("no such archived history") }

func historyUnavailable() error {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the history archive cannot answer now; try again later")
}

// historyRoutesOn is the history routes' identity guard: with the reads off
// they answer 404 before the caller is even classified.
func (s *Server) historyRoutesOn(Identity, *http.Request) error {
	if !s.HistoryReads {
		return historyHidden()
	}
	return nil
}

// historyScope is the set of projects whose archived history the caller may
// read: DESIGN-013's payload-read scope, unchanged (contextReadScope). A
// coordinator reads every payload project; a live Session reads its own
// project, if it is one. Nobody reads a history with no project.
func (s *Server) historyScope(ctx context.Context, c *caller) func(project string) bool {
	none := func(string) bool { return false }
	if s.Live == nil || len(s.ContextReadProjects) == 0 {
		return none
	}
	sess, err := s.contextCaller(ctx, c)
	if err != nil {
		return none
	}
	if c.kind == kindCoordinator {
		return func(p string) bool { return p != "" && slices.Contains(s.ContextReadProjects, p) }
	}
	snapshot, err := claimSnapshot(sess)
	if err != nil || !slices.Contains(s.ContextReadProjects, snapshot.Project()) {
		return none
	}
	own := snapshot.Project()
	return func(p string) bool { return p != "" && p == own }
}

// historySlot takes one of the replica's shelf exec slots for a read.
func (s *Server) historySlot() (func(), error) {
	if s.History == nil {
		return nil, historyUnavailable()
	}
	if s.ShelfSlots == nil {
		return func() {}, nil
	}
	release, ok := s.ShelfSlots.TryTake()
	if !ok {
		return nil, historyUnavailable()
	}
	return release, nil
}

// historyCode is agentd's refusal code, or "" for any other failure.
func historyCode(err error) string {
	var he *protocol.HistoryError
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

// historyShelfError is a shelf failure: 503, logged without any content.
func (s *Server) historyShelfError(r *http.Request, err error) error {
	s.Log.Info("the history archive could not answer", "path", r.URL.Path, "error", err.Error())
	return historyUnavailable()
}

func pageLimit(raw string, def, max int) (int, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		return 0, badRequest("limit must be between 1 and %d", max)
	}
	return n, nil
}

// listHistory is GET /v1/history?project=P[&limit=&continue=]: one project's
// archived histories, newest first. The scope for P is checked before the
// shelf is asked.
func (s *Server) listHistory(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	q := r.URL.Query()
	project := q.Get("project")
	if project == "" || !protocol.ValidHistoryProject(project) || !s.historyScope(ctx, c)(project) {
		return 0, nil, forbidden("archived history requires the payload scope of its project and a live owned identity")
	}
	limit, err := pageLimit(q.Get("limit"), apiv1.DefaultHistoryListLimit, apiv1.MaxHistoryListLimit)
	if err != nil {
		return 0, nil, err
	}
	after := q.Get("continue")
	if after != "" && !protocol.ValidHistoryCursor(after) {
		return 0, nil, badRequest("continue is not a cursor of this list")
	}
	release, err := s.historySlot()
	if err != nil {
		return 0, nil, err
	}
	defer release()
	list, err := s.History.List(ctx, project, limit, after)
	if err != nil {
		return 0, nil, s.historyShelfError(r, err)
	}
	if list.Project != project || len(list.Items) > limit || (list.Next != "" && !protocol.ValidHistoryCursor(list.Next)) {
		return 0, nil, s.historyShelfError(r, errors.New("the shelf answered a list that was not asked for"))
	}
	out := apiv1.HistoryList{Archived: true, Project: project, Items: make([]apiv1.HistorySummary, 0, len(list.Items)), Continue: list.Next}
	for _, it := range list.Items {
		v := apiv1.HistorySummary{Archived: true, ArchivedAt: it.ArchivedAt, Project: project, SessionUID: it.SessionUID, ContentSHA256: it.ContentSHA256}
		if it.Error != "" {
			v.Error = apiv1.HistoryCorrupt
		} else {
			v.Session, v.Provider, v.Bytes, v.Files = it.Session, it.Provider, it.Bytes, it.Files
			if t := it.Terminal; t != nil {
				v.Terminal = &apiv1.HistoryTerminal{PodUID: t.PodUID, Phase: t.Phase, Reason: t.Reason, NodeName: t.NodeName, ObservedAt: t.ObservedAt}
			}
		}
		out.Items = append(out.Items, v)
	}
	return http.StatusOK, out, nil
}

// historyEntry reads one page of an entry's manifest for a caller whose scope
// was found, and hides an entry outside it: 404 for one that does not exist,
// one of another project and one with no project alike. Nothing of the entry
// comes back before its project is checked.
func (s *Server) historyEntry(ctx context.Context, r *http.Request, readable func(string) bool, uid, content string, offset, limit int) (protocol.HistoryManifestPage, error) {
	page, err := s.History.Manifest(ctx, uid, content, offset, limit)
	switch {
	case historyCode(err) == protocol.HistoryErrNotFound:
		return page, historyHidden()
	case err != nil:
		return page, s.historyShelfError(r, err)
	case page.Session.UID != uid || (content != "" && page.ContentSHA256 != content) || !protocol.ValidSHA256(page.ContentSHA256):
		return page, s.historyShelfError(r, errors.New("the shelf answered for another entry"))
	case !readable(page.Project):
		return protocol.HistoryManifestPage{}, historyHidden()
	}
	return page, nil
}

// singleHistoryRead checks what both single reads check first: the caller has
// the scope for some project (404 at once otherwise), the Session UID and the
// content digest are well formed, and a shelf slot is free.
func (s *Server) singleHistoryRead(ctx context.Context, r *http.Request, c *caller) (func(string) bool, string, string, error) {
	readable := s.historyScope(ctx, c)
	if !slices.ContainsFunc(s.ContextReadProjects, readable) {
		return nil, "", "", historyHidden()
	}
	uid, content := r.PathValue("session"), r.URL.Query().Get("content")
	if !protocol.ValidKubeUID(uid) {
		return nil, "", "", badRequest("%q is not a Session UID", uid)
	}
	if content != "" && !protocol.ValidSHA256(content) {
		return nil, "", "", badRequest("content must be an entry's SHA-256")
	}
	return readable, uid, content, nil
}

// getHistory is GET /v1/history/{session}[?content=&limit=&continue=]: one
// page of an archived history's manifest, the Session's newest entry unless
// content names another.
func (s *Server) getHistory(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	readable, uid, content, err := s.singleHistoryRead(ctx, r, c)
	if err != nil {
		return 0, nil, err
	}
	q := r.URL.Query()
	limit, err := pageLimit(q.Get("limit"), apiv1.DefaultHistoryManifestLimit, apiv1.MaxHistoryManifestLimit)
	if err != nil {
		return 0, nil, err
	}
	offset := 0
	if raw := q.Get("continue"); raw != "" {
		if offset, err = strconv.Atoi(raw); err != nil || offset < 1 || offset > historyManifestOffsetMax {
			return 0, nil, badRequest("continue is not a cursor of this manifest")
		}
	}
	release, err := s.historySlot()
	if err != nil {
		return 0, nil, err
	}
	defer release()
	page, err := s.historyEntry(ctx, r, readable, uid, content, offset, limit)
	if err != nil {
		return 0, nil, err
	}
	if page.Offset != offset || len(page.Files)+len(page.Excluded) > limit {
		return 0, nil, s.historyShelfError(r, errors.New("the shelf answered a page that was not asked for"))
	}
	return http.StatusOK, historyManifestView(page), nil
}

func historyManifestView(p protocol.HistoryManifestPage) apiv1.HistoryManifest {
	t := p.Terminal
	m := apiv1.HistoryManifest{
		Archived: true, ArchivedAt: p.Entry.ArchivedAt, ContentSHA256: p.ContentSHA256,
		Session: apiv1.HistorySession{Namespace: p.Session.Namespace, Name: p.Session.Name, UID: p.Session.UID},
		Project: p.Project, Provider: p.Provider, HomeUID: p.HomeUID,
		Terminal: apiv1.HistoryTerminal{PodUID: t.PodUID, Phase: t.Phase, Reason: t.Reason, NodeName: t.NodeName, ObservedAt: t.ObservedAt},
		Rescue:   apiv1.HistoryRescue{Stamp: p.Rescue.Stamp, Result: p.Rescue.Result, PodUID: p.Rescue.PodUID},
		Roots:    make([]apiv1.HistoryRoot, 0, len(p.Roots)), Bytes: p.Bytes, FileCount: p.FileCount, ExcludedCount: p.ExcludedCount, Offset: p.Offset,
		Files: make([]apiv1.HistoryFile, 0, len(p.Files)), Excluded: make([]apiv1.HistoryExcluded, 0, len(p.Excluded)),
	}
	for _, r := range p.Roots {
		m.Roots = append(m.Roots, apiv1.HistoryRoot{Name: r.Name, Dir: r.Dir, Absent: r.Absent})
	}
	if g := p.Git; g != nil {
		m.Git = &apiv1.HistoryGit{Rescue: g.Rescue, Files: make([]apiv1.HistoryGitFile, 0, len(g.Files))}
		for _, f := range g.Files {
			m.Git.Files = append(m.Git.Files, apiv1.HistoryGitFile{Name: f.Name, Bytes: f.Bytes, SHA256: f.SHA256})
		}
	}
	for _, f := range p.Files {
		m.Files = append(m.Files, apiv1.HistoryFile{Root: f.Root, Path: f.Path, Bytes: f.Bytes, SHA256: f.SHA256, ModTime: f.ModTime})
	}
	for _, e := range p.Excluded {
		m.Excluded = append(m.Excluded, apiv1.HistoryExcluded{Root: e.Root, Path: e.Path, Class: e.Class, Bytes: e.Bytes, Partial: e.Partial})
	}
	if p.Next > 0 {
		m.Continue = strconv.Itoa(p.Next)
	}
	return m
}

// readHistoryFile is GET /v1/history/{session}/file?path=&offset=[&content=]:
// one page of one archived file, at most 64 KiB. The entry's header comes
// first, so its project is checked before the file is read at all: an offset
// or a path outside a file of another project's entry tells nothing.
func (s *Server) readHistoryFile(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	readable, uid, content, err := s.singleHistoryRead(ctx, r, c)
	if err != nil {
		return 0, nil, err
	}
	q := r.URL.Query()
	file := q.Get("path")
	root, rel, ok := strings.Cut(file, "/")
	if code, _ := protocol.CheckHistoryPath(rel); !ok || !protocol.ValidHistoryRoot(root) || code != "" {
		return 0, nil, badRequest("path must be an archived file's ROOT/PATH, ROOT one of claude, codex, agentd or log")
	}
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 || offset > protocol.MaxHistoryFileBytes {
		return 0, nil, badRequest("offset must be a byte offset in the file, 0 or more")
	}
	release, err := s.historySlot()
	if err != nil {
		return 0, nil, err
	}
	defer release()
	head, err := s.historyEntry(ctx, r, readable, uid, content, 0, 0)
	if err != nil {
		return 0, nil, err
	}
	page, err := s.History.Read(ctx, uid, head.ContentSHA256, file, offset)
	switch code := historyCode(err); {
	case code == protocol.HistoryErrNotFound:
		return 0, nil, notFound("the archived history has no file %s", file)
	case code == protocol.HistoryErrInvalid:
		return 0, nil, badRequest("offset %d is outside the file", offset)
	case err != nil:
		return 0, nil, s.historyShelfError(r, err)
	case page.ContentSHA256 != head.ContentSHA256 || page.File != file || page.Offset != offset || len(page.Data) > apiv1.MaxHistoryFilePage:
		return 0, nil, s.historyShelfError(r, errors.New("the shelf answered for another file"))
	}
	return http.StatusOK, apiv1.HistoryFilePage{
		Archived: true, ArchivedAt: head.Entry.ArchivedAt, SessionUID: uid, ContentSHA256: head.ContentSHA256, Path: file,
		FileSHA256: page.FileSHA256, Bytes: page.Bytes, Offset: page.Offset, Next: page.Next, EOF: page.EOF, Data: page.Data,
	}, nil
}
