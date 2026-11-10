package main

import (
	"slices"
	"testing"
)

func TestParentNotesFlagDefaultOffAndRequiresDurableInbox(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.parentNotes {
		t.Fatal("parent notes enabled by default", err)
	}
	if _, err := parseFlags([]string{"--enable-parent-notes"}); err == nil {
		t.Fatal("parent notes accepted without durable inbox")
	}
	bound := []string{"--enable-durable-inbox", "--inbox-projects=sample", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43"}
	o, err = parseFlags(bound)
	if err != nil || o.parentNotes {
		t.Fatal("durable inbox enabled parent notes", err)
	}
	o, err = parseFlags(append(slices.Clone(bound), "--enable-parent-notes"))
	if err != nil || !o.parentNotes || !o.inboxEnabled || o.inboxQuestions || o.coordinatorEnabled || len(o.inboxCoordinatorReadProjects) != 0 {
		t.Fatal("parent notes refused or widened another gate", err)
	}
}
