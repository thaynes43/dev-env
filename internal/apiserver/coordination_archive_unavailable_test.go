package apiserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// TestArchivedReadsWithoutAVolumeAnswerUnavailable is DESIGN-024 E2's archived
// read when the coordination archive is not a volume of its own. The real
// mount check refuses the read. The API answers unavailable and does not
// return the removed object's text. The E2 envtest remains.
func TestArchivedReadsWithoutAVolumeAnswerUnavailable(t *testing.T) {
	dir := t.TempDir()
	archive := &agentd.CoordArchive{Root: dir, Home: dir}

	t.Run("claim", func(t *testing.T) {
		f := coordinatorFixture(t)
		bindCatalogFixture(t, f)
		enableClaims(f)
		owner, reader := claimSession(t, f, "unavailable-owner", humanSA), claimSession(t, f, "unavailable-reader", clientSA)
		w := f.do(http.MethodPost, apiv1.TaskClaimsPath, owner, claimRequest("issue:331"))
		if w.Code != http.StatusCreated {
			t.Fatal(w.Code, w.Body.String())
		}
		claim, err := f.srv.TaskClaims.Get(context.Background(), decode[apiv1.TaskClaimResult](t, w).Claim.Name)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.c.Delete(context.Background(), claim); err != nil {
			t.Fatal(err)
		}
		useRealArchive(f, archive)
		w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"/"+claim.Name+"?project=sample", reader, nil)
		unavailableWithout(t, w, "issue:331")
		w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&archived=only", reader, nil)
		unavailableWithout(t, w, "issue:331")
	})

	t.Run("context", func(t *testing.T) {
		f := coordinatorFixture(t)
		bindCatalogFixture(t, f)
		enableContexts(f)
		source, reader := contextSession(t, f, "unavailable-source", humanSA), contextSession(t, f, "unavailable-context-reader", clientSA)
		w := f.do(http.MethodPost, apiv1.PublishedContextsPath, source, contextReq())
		if w.Code != http.StatusCreated {
			t.Fatal(w.Code, w.Body.String())
		}
		published, err := f.srv.PublishedContexts.Get(context.Background(), decode[apiv1.PublishContextResult](t, w).Metadata.Name)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.c.Delete(context.Background(), published); err != nil {
			t.Fatal(err)
		}
		useRealArchive(f, archive)
		w = f.do(http.MethodGet, apiv1.PublishedContextsPath+"/"+published.Name+"?project=sample", reader, nil)
		unavailableWithout(t, w, contextReq().Text)
		w = f.do(http.MethodGet, apiv1.PublishedContextsPath+"?project=sample&archived=only", reader, nil)
		unavailableWithout(t, w, contextReq().Text)
	})

	t.Run("message", func(t *testing.T) {
		f := coordinatorFixture(t)
		bindCatalogFixture(t, f)
		enableInbox(f)
		sender, recipient := claimSession(t, f, "unavailable-sender", humanSA), claimSession(t, f, "unavailable-recipient", clientSA)
		req := inboxRequest(f, "unavailable-recipient", "archive-unavailable")
		w := f.do(http.MethodPost, apiv1.InboxPath, sender, req)
		if w.Code != http.StatusCreated {
			t.Fatal(w.Code, w.Body.String())
		}
		message, err := f.srv.Inbox.Get(context.Background(), decode[apiv1.InboxResult](t, w).Message.Name)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.c.Delete(context.Background(), message); err != nil {
			t.Fatal(err)
		}
		useRealArchive(f, archive)
		w = f.do(http.MethodGet, apiv1.InboxPath+"/"+message.Name+"?project=sample", recipient, nil)
		unavailableWithout(t, w, req.Text)
		w = f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only", recipient, nil)
		unavailableWithout(t, w, req.Text)
		w = f.do(http.MethodGet, apiv1.InboxPath+"/"+message.Name+"/ack?project=sample", recipient, nil)
		unavailableWithout(t, w, req.Text)
	})
}

func unavailableWithout(t *testing.T, w *httptest.ResponseRecorder, secret string) {
	t.Helper()
	wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if strings.Contains(w.Body.String(), secret) {
		t.Fatalf("unavailable body contains the removed object: %s", w.Body.String())
	}
}
