package inbox

import (
	"testing"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func TestValidArchiveMetadataSeparatesTextFromLiveValidation(t *testing.T) {
	spec := validMessage()
	spec.Text = ""
	if !ValidArchiveMetadata(spec) || ValidMessage(spec) {
		t.Fatal("archive projection did not separate text validation")
	}
	for _, mutate := range []func(*coord.InboxMessageSpec){
		func(s *coord.InboxMessageSpec) { s.Text = "not metadata" },
		func(s *coord.InboxMessageSpec) { s.Key = "bad key" },
		func(s *coord.InboxMessageSpec) { s.Recipient.UID = "" },
		func(s *coord.InboxMessageSpec) { s.RequestedModel = "" },
	} {
		bad := spec
		mutate(&bad)
		if ValidArchiveMetadata(bad) {
			t.Fatal("invalid archive metadata accepted")
		}
	}
}
