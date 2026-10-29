package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// These records are fixed synthetic presentation data. No session, token,
// network reader or server participates in their rendering.
func asksPreviewRecords(now time.Time) []apiv1.Ask {
	info := apiv1.Ask{ID: "ask-0000000000000001", Type: apiv1.AskTypeInfo, Question: "Which fixture should the agent use for the next check?", Options: []apiv1.AskOption{{ID: "small", Label: "Small fixture", Description: "A bounded check with synthetic data.\nRuns entirely offline."}, {ID: "full", Label: "Full fixture", Description: "A larger synthetic sample for the same check."}}, Recommended: "small", AllowFreeText: true, Context: apiv1.AskContext{SessionName: "synthetic-codex-agent", SessionUID: "00000000-1111-2222-3333-444444444444", WorkPackage: 258, Links: []string{"https://github.com/thaynes43/dev-env", "https://example.com/synthetic/context", "https://github.com.evil.example/fixture"}}, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(23 * time.Hour), State: apiv1.AskStateOpen}
	approval := info
	approval.ID = "ask-0000000000000002"
	approval.Type = apiv1.AskTypeApproval
	approval.Question = "Approve running the synthetic preview check?"
	approval.Context.SessionName = "synthetic-claude-agent"
	approval.ExpiresAt = now.Add(2 * time.Hour)
	approval.Scope = &apiv1.AskScope{Action: "Run one synthetic preview check", Target: "Offline fixture only", ExpiresAt: now.Add(2 * time.Hour)}
	answered := approval
	answered.ID = "ask-0000000000000003"
	answered.State = apiv1.AskStateAnswered
	approved := true
	answered.Answer = &apiv1.AskAnswer{Text: "Use the bounded synthetic fixture.", Approved: &approved, AnsweredBy: apiv1.AskActor{Issuer: "https://identity.example/fixture-only/", Subject: "synthetic-reader", DisplayName: "Fixture reader"}, AnsweredAt: now.Add(-30 * time.Minute)}
	expired := approval
	expired.ID = "ask-0000000000000004"
	expired.State = apiv1.AskStateExpired
	expired.ExpiresAt = now.Add(-time.Minute)
	expired.Scope = &apiv1.AskScope{Action: "Run the earlier synthetic check", Target: "Expired fixture", ExpiresAt: now.Add(-time.Minute)}
	withdrawn := info
	withdrawn.ID, withdrawn.State = "ask-0000000000000005", apiv1.AskStateWithdrawn
	withdrawn.Question = "Which retired synthetic fixture should be used?"
	denied := answered
	denied.ID, denied.Question = "ask-0000000000000006", "Approve the larger synthetic check?"
	deniedAnswer := *answered.Answer
	declined := false
	deniedAnswer.Approved, deniedAnswer.Text = &declined, "Keep this check bounded."
	denied.Answer = &deniedAnswer
	answeredInfo := info
	answeredInfo.ID, answeredInfo.State = "ask-0000000000000007", apiv1.AskStateAnswered
	answeredInfo.Answer = &apiv1.AskAnswer{OptionID: "small", Text: "Use the small fixture.", AnsweredBy: answered.Answer.AnsweredBy, AnsweredAt: answered.Answer.AnsweredAt}
	pastExpiry := info
	pastExpiry.ID, pastExpiry.Question = "ask-0000000000000008", "Which fixture was needed for the earlier check?"
	pastExpiry.ExpiresAt = now.Add(-time.Minute) // Stored open; rendered as Expired.
	expiredApproval := answered
	expiredApproval.ID, expiredApproval.Question = "ask-0000000000000009", "Approve the earlier synthetic preview check?"
	expiredApproval.ExpiresAt, expiredApproval.Scope = expired.ExpiresAt, expired.Scope
	return []apiv1.Ask{answered, info, approval, expired, withdrawn, denied, answeredInfo, pastExpiry, expiredApproval}
}

func writeAsks(dir string) error {
	now := time.Date(2026, 10, 29, 19, 5, 0, 0, time.UTC)
	fixtures := []struct {
		name string
		page portal.AsksPage
	}{
		{"asks.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, Complete: true}},
		{"asks-empty.html", portal.AsksPage{Connected: true, Complete: true}},
		{"asks-disconnected.html", portal.AsksPage{Notice: "Asks are not connected"}},
		{"asks-unavailable.html", portal.AsksPage{Notice: "Asks are unavailable. Open asks could not be read. The operator did not answer within 12 s. Checked 29 Oct 2026, 19:05:00 UTC.", Retry: true}},
		{"asks-history-page.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, Complete: true, HistoryNotice: "Only one page of history is shown. Older history may be cut off.", OlderHref: "/portal/projects/dev-env/environments/internal/asks?cursor=synthetic-older"}},
		{"asks-selected.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, Complete: true, SelectedID: "ask-0000000000000003"}},
		{"asks-validation.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, SelectedID: "ask-0000000000000001", AnswerErrorID: "ask-0000000000000001", AnswerError: "Write an answer or choose an option."}},
		{"asks-partial.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, Notice: "This is a partial read. More asks may need attention."}},
		{"asks-withdrawn.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, Complete: true, SelectedID: "ask-0000000000000005"}},
		{"asks-denied.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, Complete: true, SelectedID: "ask-0000000000000006"}},
		{"asks-answered-info.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, Complete: true, SelectedID: "ask-0000000000000007"}},
		{"asks-expired-open.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, Complete: true, SelectedID: "ask-0000000000000008"}},
		{"asks-approval-expired.html", portal.AsksPage{Asks: asksPreviewRecords(now), Connected: true, Complete: true, SelectedID: "ask-0000000000000009"}},
	}
	outputs := make([][]byte, len(fixtures))
	for i, f := range fixtures {
		f.page.Preview, f.page.Now, f.page.PortalHost = true, now, "portal.example"
		body, _, err := portal.RenderAsks(f.page, 896<<10)
		if err != nil {
			return err
		}
		outputs[i] = body
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("create new preview directory: %w", err)
	}
	for i, f := range fixtures {
		file, err := os.OpenFile(filepath.Join(dir, f.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(outputs[i])
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
