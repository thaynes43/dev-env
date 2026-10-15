package contextpublication

import (
	"errors"
	"testing"
)

func TestContextArchiveProjectMaskedIsStillNotFound(t *testing.T) {
	if !errors.Is(ErrProjectMasked, ErrNotFound) {
		t.Fatal("project mask must preserve existing not-found contract")
	}
}
