package contextpublication

import (
	"testing"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func TestValidArchiveMetadataSeparatesTextFromImmutableMetadata(t *testing.T) {
	spec := sampleSpec()
	spec.Text = ""
	if !ValidArchiveMetadata(spec) {
		t.Fatal("valid projected metadata refused")
	}
	if ValidSpec(spec) {
		t.Fatal("full validator accepted text-stripped projection")
	}
	for _, mutate := range []func(*coord.PublishedContextSpec){
		func(s *coord.PublishedContextSpec) { s.ContentSHA256 = "bad" },
		func(s *coord.PublishedContextSpec) { s.ByteCount = 0 },
		func(s *coord.PublishedContextSpec) { s.Identity.Key = "bad key" },
		func(s *coord.PublishedContextSpec) { s.Source.UID = "other" },
	} {
		bad := spec
		mutate(&bad)
		if ValidArchiveMetadata(bad) {
			t.Fatal("invalid metadata accepted")
		}
	}
}
