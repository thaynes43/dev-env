package apiserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// codexShelf is the archived shelf with Codex provider and one JSONL page.
// Read keeps the embedded digest, path, offset, byte count, and EOF.
type codexShelf struct{ *fakeHistory }

func (s *codexShelf) Manifest(ctx context.Context, uid, content string, offset, limit int) (protocol.HistoryManifestPage, error) {
	p, err := s.fakeHistory.Manifest(ctx, uid, content, offset, limit)
	if err != nil {
		return protocol.HistoryManifestPage{}, err
	}
	p.Provider = "codex"
	for i := range p.Files {
		p.Files[i].Root = strings.ReplaceAll(p.Files[i].Root, "claude", "codex")
		p.Files[i].Path = strings.ReplaceAll(p.Files[i].Path, "claude", "codex")
	}
	for i := range p.Roots {
		p.Roots[i].Name = strings.ReplaceAll(p.Roots[i].Name, "claude", "codex")
		p.Roots[i].Dir = strings.ReplaceAll(p.Roots[i].Dir, "claude", "codex")
	}
	return p, nil
}

func (s *codexShelf) Read(ctx context.Context, uid, content, file string, offset int64) (protocol.HistoryReadResult, error) {
	page, err := s.fakeHistory.Read(ctx, uid, content, file, offset)
	if err != nil || offset != 0 {
		return page, err
	}
	page.Data = []byte(`{}
`)
	return page, nil
}

func TestReadyPoolClaudeArchivedBytes(t *testing.T) {
	f, h, tok := historyFixture(t)
	sum := histSum(histSample)
	res := f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample)+"?limit=2", tok, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("manifest status %d", res.Code)
	}
	man := decode[apiv1.HistoryManifest](t, res)
	if !man.Archived || man.Provider != "claude" || man.ContentSHA256 != sum || man.Session.UID != histSample || man.Project != "sample" || man.Terminal.Phase != "Failed" || man.Terminal.Reason != "DeadlineExceeded" {
		t.Fatalf("%+v", man)
	}
	if len(man.Files) == 0 || man.Files[0].Root+"/"+man.Files[0].Path != "claude/projects/a.jsonl" || man.Files[0].SHA256 != sum || man.Files[0].Bytes != 3 {
		t.Fatalf("%+v", man.Files)
	}
	p := apiv1.HistoryFilePath(histSample) + "?offset=0&path=" + url.QueryEscape("claude/projects/a.jsonl")
	res = f.do(http.MethodGet, p, tok, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("file status %d", res.Code)
	}
	got := decode[apiv1.HistoryFilePage](t, res)
	if !got.Archived || got.Path != "claude/projects/a.jsonl" || got.SessionUID != histSample || got.ContentSHA256 != sum || got.FileSHA256 != sum || got.Offset != 0 || string(got.Data) != "abc" || got.Bytes != 3 || !got.EOF {
		t.Fatalf("%+v %q", got, got.Data)
	}
	want := "read " + histSample + " " + sum + " claude/projects/a.jsonl 0"
	if !strings.Contains(strings.Join(h.calls, "\n"), want) {
		t.Fatal(h.calls)
	}
}

func TestReadyPoolCodexBytesAndShelfRefusal(t *testing.T) {
	f, h, tok := historyFixture(t)
	f.srv.History = &codexShelf{fakeHistory: h}
	sum := histSum(histSample)
	res := f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample)+"?limit=2", tok, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("manifest status %d", res.Code)
	}
	man := decode[apiv1.HistoryManifest](t, res)
	if !man.Archived || man.Provider != "codex" || man.ContentSHA256 != sum || man.Session.UID != histSample || man.Terminal.Phase != "Failed" {
		t.Fatalf("%+v", man)
	}
	if len(man.Files) == 0 || man.Files[0].Root+"/"+man.Files[0].Path != "codex/projects/a.jsonl" || man.Files[0].SHA256 != sum || man.Files[0].Bytes != 3 {
		t.Fatalf("%+v", man.Files)
	}
	archived := man.Files[0].Root + "/" + man.Files[0].Path
	p := apiv1.HistoryFilePath(histSample) + "?offset=0&path=" + url.QueryEscape(archived)
	res = f.do(http.MethodGet, p, tok, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("file status %d", res.Code)
	}
	got := decode[apiv1.HistoryFilePage](t, res)
	if !got.Archived || got.Path != archived || got.SessionUID != histSample || got.ContentSHA256 != sum || got.FileSHA256 != sum || got.Offset != 0 || string(got.Data) != `{}
` || got.Bytes != 3 || !got.EOF {
		t.Fatalf("%+v %q", got, got.Data)
	}
	h.readErr = errors.New("shelf unavailable")
	res = f.do(http.MethodGet, p, tok, nil)
	if res.Code == http.StatusOK {
		t.Fatal("shelf failure returned a file page")
	}
	body := res.Body.String()
	if strings.Contains(body, `{}
`) || strings.Contains(body, "abc") {
		t.Fatalf("%s", body)
	}
	if len(h.calls) == 0 || !strings.HasPrefix(h.calls[len(h.calls)-1], "read ") {
		t.Fatal(h.calls)
	}
}
