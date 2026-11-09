package controller

import (
	"strings"
	"testing"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/eventnote"
)

// A failed rescue lists a git error for every repo; its Event note must stay
// inside the API's 1024-byte limit or the server rejects the Event.
func TestEventNoteIsBoundedToTheAPILimit(t *testing.T) {
	rec := events.NewFakeRecorder(1)
	r := &Reconciler{Recorder: rec}
	note := "the rescue 20261109-1006 failed: " + strings.Repeat("fetch repo: Could not resolve host: github.com é界; ", 60)
	r.event(&v1alpha1.AgentSession{}, corev1.EventTypeWarning, "Rescue", "FetchFailed", note)
	got := <-rec.Events
	body := strings.TrimPrefix(got, corev1.EventTypeWarning+" FetchFailed ")
	if len(body) > eventnote.MaxBytes || !utf8.ValidString(body) {
		t.Fatalf("event note is %d bytes, valid UTF-8 %v", len(body), utf8.ValidString(body))
	}
	if !strings.HasPrefix(body, "the rescue 20261109-1006 failed: fetch repo") || !strings.HasSuffix(body, eventnote.Marker) {
		t.Fatalf("event note lost its prefix or marker: %q", body)
	}
}
